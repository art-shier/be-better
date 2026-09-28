package agenthost

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentgateway"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agentruntime"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/worker"

	"github.com/google/uuid"
)

type recordingHostObserver struct {
	mu           sync.Mutex
	observations []agentexecution.Observation
	provider     chan timedProviderObservation
}

type timedProviderObservation struct {
	observation agentexecution.Observation
	emittedAt   time.Time
}

func (observer *recordingHostObserver) ObserveAgent(observation agentexecution.Observation) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(observer.observations, observation)
	if observation.Kind == "provider" && observer.provider != nil {
		select {
		case observer.provider <- timedProviderObservation{observation: observation, emittedAt: time.Now()}:
		default:
		}
	}
}

func (observer *recordingHostObserver) snapshot() []agentexecution.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]agentexecution.Observation(nil), observer.observations...)
}

func TestAgentBackgroundObservabilityOwnsOneSlotAndSkipsTerminalRedelivery(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fake, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: backgroundWindow()})
	if err != nil {
		t.Fatal(err)
	}
	observer := &recordingHostObserver{}
	var logs bytes.Buffer
	host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, fake, backgroundHarnessObservability{
		observer: observer, logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err = host.Process(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err = host.Process(ctx, event); err != nil {
		t.Fatal(err)
	}
	var slots []agentexecution.Observation
	for _, observation := range observer.snapshot() {
		if observation.Kind == "background_slot" {
			slots = append(slots, observation)
		}
	}
	if len(slots) != 2 || slots[0].Outcome != "started" || slots[1].Outcome != "completed" {
		t.Fatalf("background slot observations = %#v, want one owned admission/release", slots)
	}
	if slots[0].Mode != "background" || slots[0].ModelProfile != "readonly-default" || slots[0].QueueWait <= 0 {
		t.Fatalf("background admission observation = %#v", slots[0])
	}
	logged := logs.String()
	for _, required := range []string{"agent background slot acquired", "agent background slot released", "runId=" + event.AggregateID.String(), "profile=readonly-default"} {
		if !strings.Contains(logged, required) {
			t.Errorf("background log missing %q: %s", required, logged)
		}
	}
}

func TestAgentBackgroundUserCancellationStopsCalendarDependencyWithinOneSecond(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store := &blockingHostCalendarStore{entered: make(chan struct{}), exited: make(chan struct{})}
	cursors, err := service.NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	calendarService, err := service.NewCalendarService(store, workerServices.Transactor, &service.CommandService{}, cursors)
	if err != nil {
		t.Fatal(err)
	}
	calendarRead, err := service.NewAgentCalendarReadService(workerServices.Runs, calendarService)
	if err != nil {
		t.Fatal(err)
	}
	fake, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: backgroundWindow()})
	if err != nil {
		t.Fatal(err)
	}
	observer := &recordingHostObserver{}
	host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, fake, backgroundHarnessObservability{
		observer: observer, calendar: calendarRead,
	})
	result := make(chan error, 1)
	go func() { result <- host.Process(ctx, event) }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Calendar dependency did not start")
	}
	view, err := api.Runs.Get(ctx, fixture.UserA, event.AggregateID)
	if err != nil {
		t.Fatal(err)
	}
	cancelEnteredAt := time.Now()
	_, err = api.Runs.Cancel(ctx, service.MutationContext{
		UserID: fixture.UserA, DeviceID: fixture.DeviceA, MutationID: uuid.New(), RequestID: uuid.New(),
	}, event.AggregateID, int64(view.Version))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.exited:
		if elapsed := time.Since(cancelEnteredAt); elapsed > time.Second {
			t.Fatalf("Runs.Cancel entry to actual Calendar dependency exit = %s, want <=1s", elapsed)
		}
	case <-time.After(time.Until(cancelEnteredAt.Add(time.Second))):
		t.Fatal("actual Calendar dependency did not cooperatively exit within one second of Runs.Cancel entry")
	}
	if err = waitBackgroundResult(result); err != nil {
		t.Fatal(err)
	}
	var cancellation *agentexecution.Observation
	for _, observation := range observer.snapshot() {
		if observation.Kind == "tool" && observation.ErrorCode == string(agentprotocol.ErrorCodeCancelled) {
			copy := observation
			cancellation = &copy
		}
	}
	if cancellation == nil || cancellation.CancelLatency <= 0 || cancellation.CancelLatency > time.Second {
		t.Fatalf("Calendar cancellation observation = %#v, want stored transition to actual exit within one second", cancellation)
	}
}

type blockingHostCalendarStore struct {
	entered chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func (*blockingHostCalendarStore) CreateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent) (model.CalendarEvent, error) {
	return model.CalendarEvent{}, errors.New("unexpected CreateEvent")
}
func (*blockingHostCalendarStore) GetEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.CalendarEvent, error) {
	return model.CalendarEvent{}, errors.New("unexpected GetEvent")
}
func (store *blockingHostCalendarStore) ListEvents(ctx context.Context, _ database.Tx, _ uuid.UUID, _, _ *time.Time, _ *model.ResourcePosition, _ int) ([]model.CalendarEvent, error) {
	store.once.Do(func() { close(store.entered) })
	<-ctx.Done()
	close(store.exited)
	return nil, ctx.Err()
}
func (*blockingHostCalendarStore) UpdateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent, int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	return model.CalendarEvent{}, nil, errors.New("unexpected UpdateEvent")
}
func (*blockingHostCalendarStore) DeleteEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	return model.CalendarEvent{}, nil, errors.New("unexpected DeleteEvent")
}
func (*blockingHostCalendarStore) CreateReminder(context.Context, database.Tx, uuid.UUID, model.CalendarReminder) (model.CalendarReminder, error) {
	return model.CalendarReminder{}, errors.New("unexpected CreateReminder")
}
func (*blockingHostCalendarStore) ListReminders(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.CalendarReminder, error) {
	return nil, errors.New("unexpected ListReminders")
}
func (*blockingHostCalendarStore) DeleteReminder(context.Context, database.Tx, uuid.UUID, uuid.UUID, uuid.UUID) (model.CalendarReminder, error) {
	return model.CalendarReminder{}, errors.New("unexpected DeleteReminder")
}

type countingFakeAdapter struct {
	fake     *agentprovider.Fake
	calls    atomic.Int64
	mu       sync.Mutex
	requests []agentprotocol.ModelTurnRequest
}

func (adapter *countingFakeAdapter) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest, options agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	adapter.calls.Add(1)
	adapter.mu.Lock()
	adapter.requests = append(adapter.requests, request)
	adapter.mu.Unlock()
	return adapter.fake.Stream(ctx, request, options)
}

func (adapter *countingFakeAdapter) firstRequest() agentprotocol.ModelTurnRequest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.requests[0]
}

func TestBackgroundReadonlyProcessesPersistedIntentAndDoesNotReplayTerminalRun(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	from, to := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	start := agentprotocol.ReadonlyRunStart{
		Intent: "summarize the persisted calendar window", ExecutionMode: agentprotocol.ExecutionModeBackground,
		Scope:    agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
		Timezone: "UTC", ModelProfile: "readonly-default",
	}
	created, err := api.Runs.Create(ctx, service.MutationContext{
		UserID: fixture.UserA, DeviceID: fixture.DeviceA, MutationID: uuid.New(), RequestID: uuid.New(),
	}, start)
	if err != nil {
		t.Fatal(err)
	}

	repository, err := postgresstore.NewOutboxRepository(fixture.Worker)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repository.Claim(ctx, 1, uuid.New(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].AggregateID.String() != string(created.RunID) {
		t.Fatalf("claimed events = %#v", events)
	}

	fake, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: agentprotocol.CalendarReadInput{
		Start: agentprotocol.DateTime(from), End: agentprotocol.DateTime(to),
	}})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &countingFakeAdapter{fake: fake}
	calendar, err := service.NewAgentCalendarReadService(workerServices.Runs, workerServices.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := agentgateway.New(agentgateway.Config{
		Runs:     workerServices.Runs,
		Profiles: []agentgateway.Profile{{ID: "readonly-default", Model: "fixture-model", Adapter: adapter}},
		Tools:    readonlyGatewayTools(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewBackground(Config{Runs: workerServices.Runs, Calendar: calendar, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := host.ProcessWithTrace(ctx, events[0])
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || trace.ModelTurns != 4 || len(trace.Inputs) == 0 || len(trace.Effects) == 0 {
		t.Fatalf("ProcessWithTrace() trace = %#v", trace)
	}
	view, err := api.Runs.Get(ctx, fixture.UserA, events[0].AggregateID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != agentprotocol.ReadonlyRunViewStatusCompleted {
		t.Fatalf("run status = %s, want completed", view.Status)
	}
	if view.Summary == nil || *view.Summary != "0 calendar events." {
		t.Fatalf("run summary = %v, want Fake final text", view.Summary)
	}
	if !reflect.DeepEqual(view.CapabilitySnapshot, created.CapabilitySnapshot) {
		t.Fatalf("terminal snapshot = %#v, created snapshot = %#v", view.CapabilitySnapshot, created.CapabilitySnapshot)
	}
	first := adapter.firstRequest()
	if len(first.Messages) < 2 || first.Messages[1].Role != agentprotocol.MessageRoleUser ||
		len(first.Messages[1].Content) != 1 || first.Messages[1].Content[0].Text == nil ||
		*first.Messages[1].Content[0].Text != start.Intent {
		t.Fatalf("first model input did not use persisted intent: %#v", first.Messages)
	}
	toolIDs := make([]string, len(first.Tools))
	for index, tool := range first.Tools {
		toolIDs[index] = tool.ID
	}
	if want := []string{"dayorder.calendar.read", "skill_list", "skill_load"}; !reflect.DeepEqual(toolIDs, want) {
		t.Fatalf("first model Tool IDs = %v, want %v", toolIDs, want)
	}
	before := adapter.calls.Load()
	if before == 0 {
		t.Fatal("Fake Adapter was not called")
	}
	trace, err = host.ProcessWithTrace(ctx, events[0])
	if err != nil {
		t.Fatal(err)
	}
	if trace != nil {
		t.Fatalf("terminal replay trace = %#v, want nil", trace)
	}
	if adapter.calls.Load() != before {
		t.Fatal("terminal run executed again")
	}
}

type blockingReadonlyRuns struct {
	authorizeEntered chan struct{}
	authorizeCause   chan error
	getDeadline      chan time.Duration
}

func (runs *blockingReadonlyRuns) Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) (agentexecution.Record, error) {
	panic("unexpected Claim")
}

func (runs *blockingReadonlyRuns) Authorize(ctx context.Context, _ agentexecution.Actor, _ uuid.UUID) (agentexecution.Record, error) {
	close(runs.authorizeEntered)
	<-ctx.Done()
	runs.authorizeCause <- context.Cause(ctx)
	return agentexecution.Record{}, ctx.Err()
}

func (runs *blockingReadonlyRuns) Get(ctx context.Context, _, _ uuid.UUID) (agentprotocol.ReadonlyRunView, error) {
	if runs.getDeadline != nil {
		deadline, _ := ctx.Deadline()
		runs.getDeadline <- time.Until(deadline)
	}
	return agentprotocol.ReadonlyRunView{}, errors.New("database unavailable")
}

func (runs *blockingReadonlyRuns) Complete(context.Context, agentexecution.Actor, uuid.UUID, agentprotocol.RuntimeState, []model.AgentStepDraft) error {
	panic("unexpected Complete")
}

func TestBackgroundMonitorCancelsAnInFlightAuthorizationAndJoinsOnParentShutdown(t *testing.T) {
	runs := &blockingReadonlyRuns{authorizeEntered: make(chan struct{}), authorizeCause: make(chan error, 1)}
	cancelCause := make(chan error, 1)
	host := &Background{runs: runs, cancelTurn: func(_ uuid.UUID, cause error) { cancelCause <- cause }}
	parent, cancelParent := context.WithCancel(context.Background())
	runCtx, stop := context.WithCancelCause(context.WithoutCancel(parent))
	done := make(chan struct{})
	go host.monitor(parent, runCtx, stop, done, agentexecution.Actor{}, uuid.New())

	select {
	case <-runs.authorizeEntered:
	case <-time.After(time.Second):
		stop(agentruntime.StopCause{Kind: "interrupted"})
		<-done
		t.Fatal("monitor did not enter Authorize")
	}
	cancelParent()
	select {
	case <-done:
	case <-time.After(time.Second):
		stop(agentruntime.StopCause{Kind: "interrupted"})
		<-done
		t.Fatal("monitor did not cancel the in-flight Authorize and join")
	}
	var stopCause agentruntime.StopCause
	if cause := <-runs.authorizeCause; !errors.As(cause, &stopCause) || stopCause.Kind != "interrupted" {
		t.Fatalf("Authorize cancellation cause = %v, want interrupted", cause)
	}
	var gatewayCause *agentexecution.Error
	if cause := <-cancelCause; !errors.As(cause, &gatewayCause) || gatewayCause.Agent.Code != agentprotocol.ErrorCodeInternalError {
		t.Fatalf("Gateway cancellation cause = %v, want controlled internal error", cause)
	}
}

func TestBackgroundRunErrorReconciliationUsesTheBoundedFinalizationWindow(t *testing.T) {
	runs := &blockingReadonlyRuns{getDeadline: make(chan time.Duration, 1)}
	host := &Background{runs: runs}
	want := errors.New("driver failed")
	if err := host.reconcileRunError(uuid.New(), uuid.New(), want); !errors.Is(err, want) {
		t.Fatalf("reconcileRunError error = %v, want driver failure", err)
	}
	if remaining := <-runs.getDeadline; remaining <= 0 || remaining > backgroundFinalizeWindow {
		t.Fatalf("Get deadline remaining = %s, want within %s", remaining, backgroundFinalizeWindow)
	}
}

type completingReadonlyRuns struct {
	completeErrors []error
	states         []agentprotocol.RuntimeState
	view           agentprotocol.ReadonlyRunView
}

func (runs *completingReadonlyRuns) Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) (agentexecution.Record, error) {
	panic("unexpected Claim")
}

func (runs *completingReadonlyRuns) Authorize(context.Context, agentexecution.Actor, uuid.UUID) (agentexecution.Record, error) {
	panic("unexpected Authorize")
}

func (runs *completingReadonlyRuns) Get(context.Context, uuid.UUID, uuid.UUID) (agentprotocol.ReadonlyRunView, error) {
	return runs.view, nil
}

func (runs *completingReadonlyRuns) Complete(_ context.Context, _ agentexecution.Actor, _ uuid.UUID, state agentprotocol.RuntimeState, _ []model.AgentStepDraft) error {
	runs.states = append(runs.states, state)
	index := len(runs.states) - 1
	if index < len(runs.completeErrors) {
		return runs.completeErrors[index]
	}
	return nil
}

func TestBackgroundCompleteRetriesTheOriginalTerminalResultWithinOneWindow(t *testing.T) {
	transient := errors.New("commit failed")
	runs := &completingReadonlyRuns{
		completeErrors: []error{transient, nil},
		view:           agentprotocol.ReadonlyRunView{Status: agentprotocol.ReadonlyRunViewStatusAnalyzing},
	}
	host := &Background{runs: runs}
	state := agentprotocol.RuntimeState{ProtocolVersion: "2.0", RunID: uuid.NewString(), Phase: agentprotocol.RuntimePhaseFailed}
	if err := host.complete(agentexecution.Actor{}, uuid.New(), state); err != nil {
		t.Fatal(err)
	}
	if len(runs.states) != 2 || !reflect.DeepEqual(runs.states[0], state) || !reflect.DeepEqual(runs.states[1], state) {
		t.Fatalf("Complete states = %#v, want the same terminal result twice", runs.states)
	}
}

func TestBackgroundCompleteAcknowledgesDurableTerminalAfterCommitResponseFailure(t *testing.T) {
	runs := &completingReadonlyRuns{
		completeErrors: []error{errors.New("commit response lost")},
		view:           agentprotocol.ReadonlyRunView{Status: agentprotocol.ReadonlyRunViewStatusFailed},
	}
	host := &Background{runs: runs}
	if err := host.complete(agentexecution.Actor{}, uuid.New(), agentprotocol.RuntimeState{}); err != nil {
		t.Fatal(err)
	}
	if len(runs.states) != 1 {
		t.Fatalf("Complete calls = %d, want 1 after durable terminal reconciliation", len(runs.states))
	}
}

func TestBackgroundCompleteDoesNotRetryAStaleToken(t *testing.T) {
	runs := &completingReadonlyRuns{
		completeErrors: []error{model.ErrConflict},
		view:           agentprotocol.ReadonlyRunView{Status: agentprotocol.ReadonlyRunViewStatusAnalyzing},
	}
	host := &Background{runs: runs}
	if err := host.complete(agentexecution.Actor{}, uuid.New(), agentprotocol.RuntimeState{}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("complete error = %v, want conflict", err)
	}
	if len(runs.states) != 1 {
		t.Fatalf("Complete calls = %d, want no stale-token retry", len(runs.states))
	}
}

type claimingReadonlyRuns struct {
	record      agentexecution.Record
	err         error
	claimCalls  int
	redelivered bool
}

func (runs *claimingReadonlyRuns) Claim(_ context.Context, _, _, _ uuid.UUID, redelivered bool) (agentexecution.Record, error) {
	runs.claimCalls++
	runs.redelivered = redelivered
	return runs.record, runs.err
}

func (runs *claimingReadonlyRuns) Authorize(context.Context, agentexecution.Actor, uuid.UUID) (agentexecution.Record, error) {
	panic("unexpected Authorize")
}

func (runs *claimingReadonlyRuns) Get(context.Context, uuid.UUID, uuid.UUID) (agentprotocol.ReadonlyRunView, error) {
	panic("unexpected Get")
}

func (runs *claimingReadonlyRuns) Complete(context.Context, agentexecution.Actor, uuid.UUID, agentprotocol.RuntimeState, []model.AgentStepDraft) error {
	panic("unexpected Complete")
}

func TestBackgroundValidatesClaimAuthorityBeforeLoadingTheRun(t *testing.T) {
	runID := uuid.New()
	valid := model.OutboxEvent{
		ID: uuid.New(), UserID: uuid.New(), EventType: "agent.readonly.run.requested", AggregateType: "agent_run", AggregateID: runID,
		Payload: []byte(`{"formatVersion":1,"runId":"` + runID.String() + `"}`), Attempts: 1, LockToken: uuid.New(),
	}
	tests := []struct {
		name   string
		mutate func(*model.OutboxEvent)
	}{
		{name: "event ID", mutate: func(event *model.OutboxEvent) { event.ID = uuid.Nil }},
		{name: "event type", mutate: func(event *model.OutboxEvent) { event.EventType = "agent.run.requested" }},
		{name: "user", mutate: func(event *model.OutboxEvent) { event.UserID = uuid.Nil }},
		{name: "aggregate type", mutate: func(event *model.OutboxEvent) { event.AggregateType = "task" }},
		{name: "aggregate ID", mutate: func(event *model.OutboxEvent) { event.AggregateID = uuid.Nil }},
		{name: "lock token", mutate: func(event *model.OutboxEvent) { event.LockToken = uuid.Nil }},
		{name: "attempts", mutate: func(event *model.OutboxEvent) { event.Attempts = 0 }},
		{name: "payload run ID", mutate: func(event *model.OutboxEvent) {
			event.Payload = []byte(`{"formatVersion":1,"runId":"` + uuid.NewString() + `"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			test.mutate(&event)
			runs := &claimingReadonlyRuns{}
			host := &Background{runs: runs}
			if err := host.Process(context.Background(), event); err == nil {
				t.Fatal("Process accepted an invalid claimed event")
			}
			if runs.claimCalls != 0 {
				t.Fatalf("invalid event reached Claim %d times", runs.claimCalls)
			}
		})
	}
}

func TestBackgroundAcknowledgesTerminalRedeliveryWithoutExecution(t *testing.T) {
	runID := uuid.New()
	event := model.OutboxEvent{
		ID: uuid.New(), UserID: uuid.New(), EventType: "agent.readonly.run.requested", AggregateType: "agent_run", AggregateID: runID,
		Payload: []byte(`{"formatVersion":1,"runId":"` + runID.String() + `"}`), Attempts: 2, LockToken: uuid.New(),
	}
	runs := &claimingReadonlyRuns{record: agentexecution.Record{Run: model.AgentRun{Status: string(agentprotocol.ReadonlyRunViewStatusFailed)}}}
	host := &Background{runs: runs}
	if err := host.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if runs.claimCalls != 1 || !runs.redelivered {
		t.Fatalf("Claim calls=%d redelivered=%t, want one redelivery Claim", runs.claimCalls, runs.redelivered)
	}
}

func TestBackgroundReturnsClaimInfrastructureFailure(t *testing.T) {
	runID := uuid.New()
	event := model.OutboxEvent{
		ID: uuid.New(), UserID: uuid.New(), EventType: "agent.readonly.run.requested", AggregateType: "agent_run", AggregateID: runID,
		Payload: []byte(`{"formatVersion":1,"runId":"` + runID.String() + `"}`), Attempts: 1, LockToken: uuid.New(),
	}
	want := errors.New("database unavailable")
	host := &Background{runs: &claimingReadonlyRuns{err: want}}
	if err := host.Process(context.Background(), event); !errors.Is(err, want) {
		t.Fatalf("Process error = %v, want Claim infrastructure failure", err)
	}
}

func TestNewBackgroundRejectsMissingDependencies(t *testing.T) {
	if _, err := NewBackground(Config{}); err == nil {
		t.Fatal("NewBackground accepted missing dependencies")
	}
}

type lateResultAdapter struct {
	started chan struct{}
	exited  chan struct{}
	once    sync.Once
	calls   atomic.Int64
}

func (adapter *lateResultAdapter) Stream(ctx context.Context, _ agentprotocol.ModelTurnRequest, _ agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	adapter.calls.Add(1)
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		if adapter.exited != nil {
			defer close(adapter.exited)
		}
		adapter.once.Do(func() { close(adapter.started) })
		<-ctx.Done()
		late := "late result must not be published"
		if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &late}, nil) {
			return
		}
		reason := agentprotocol.ProviderEventStopReasonEndTurn
		yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: &reason, Usage: &agentprotocol.Usage{}}, nil)
	}
}

func TestBackgroundCancellationAndInterruptionDoNotPublishLateResultsOrReplay(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Run("persisted user cancellation", func(t *testing.T) {
		adapter := &lateResultAdapter{started: make(chan struct{}), exited: make(chan struct{})}
		observer := &recordingHostObserver{provider: make(chan timedProviderObservation, 1)}
		host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, adapter, backgroundHarnessObservability{observer: observer})
		result := make(chan error, 1)
		go func() { result <- host.Process(ctx, event) }()
		select {
		case <-adapter.started:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not start")
		}
		view, err := api.Runs.Get(ctx, fixture.UserA, event.AggregateID)
		if err != nil {
			t.Fatal(err)
		}
		cancelEnteredAt := time.Now()
		_, err = api.Runs.Cancel(ctx, service.MutationContext{
			UserID: fixture.UserA, DeviceID: fixture.DeviceA, MutationID: uuid.New(), RequestID: uuid.New(),
		}, event.AggregateID, int64(view.Version))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-adapter.exited:
			if elapsed := time.Since(cancelEnteredAt); elapsed > time.Second {
				t.Fatalf("Runs.Cancel entry to actual Provider exit = %s, want <=1s", elapsed)
			}
		case <-time.After(time.Until(cancelEnteredAt.Add(time.Second))):
			t.Fatal("actual Provider did not cooperatively exit within one second of Runs.Cancel entry")
		}
		if err = waitBackgroundResult(result); err != nil {
			t.Fatal(err)
		}
		// Process may return before the Provider's bounded accounting cleanup
		// emits its observation. Wait for that event, not for an arbitrary sleep;
		// the actual Provider exit above must still occur within one second.
		observeBy := cancelEnteredAt.Add(backgroundFinalizeWindow)
		observationTimer := time.NewTimer(time.Until(observeBy))
		defer observationTimer.Stop()
		var recorded timedProviderObservation
		select {
		case recorded = <-observer.provider:
		case <-observationTimer.C:
			// Process may have returned after both signals became ready. Use
			// the emission timestamp instead of randomly rejecting a buffered event.
			select {
			case recorded = <-observer.provider:
			default:
				t.Fatal("Provider did not emit a cancellation observation within the bounded finalization window")
			}
		}
		if recorded.emittedAt.After(observeBy) {
			t.Fatal("Provider cancellation observation exceeded the bounded finalization window")
		}
		cancellation := recorded.observation
		if cancellation.ErrorCode != string(agentprotocol.ErrorCodeCancelled) || cancellation.CancelLatency <= 0 || cancellation.CancelLatency > time.Second ||
			cancellation.Usage != (agentprotocol.Usage{}) || cancellation.UsageComplete {
			t.Fatalf("Provider cancellation observation = %#v, want stored transition to actual exit <=1s without fabricated Usage", cancellation)
		}
		stopped, err := api.Runs.Get(ctx, fixture.UserA, event.AggregateID)
		if err != nil || stopped.Status != agentprotocol.ReadonlyRunViewStatusStopped || stopped.Error == nil || stopped.Error.Code != agentprotocol.ErrorCodeCancelled {
			t.Fatalf("cancelled run = %#v, error=%v", stopped, err)
		}
		if stopped.Summary != nil && *stopped.Summary == "late result must not be published" {
			t.Fatal("late provider result was published after cancellation")
		}
	})

	t.Run("parent shutdown is interrupted and not replayed", func(t *testing.T) {
		adapter := &lateResultAdapter{started: make(chan struct{})}
		host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, adapter)
		processCtx, cancelProcess := context.WithCancel(ctx)
		result := make(chan error, 1)
		go func() { result <- host.Process(processCtx, event) }()
		select {
		case <-adapter.started:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not start")
		}
		cancelProcess()
		if err := waitBackgroundResult(result); err != nil {
			t.Fatal(err)
		}
		failed, err := api.Runs.Get(ctx, fixture.UserA, event.AggregateID)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Error == nil {
			t.Fatalf("interrupted run status=%q has no error", failed.Status)
		}
		// Gateway cancellation and Runtime completion can persist the interruption
		// in either order; the first durable terminal result remains authoritative.
		interruptedMessage := failed.Error.Message == "runtime interrupted" || failed.Error.Message == "execution_interrupted"
		if failed.Status != agentprotocol.ReadonlyRunViewStatusFailed ||
			failed.Error.Code != agentprotocol.ErrorCodeInternalError || failed.Error.Retryable || !interruptedMessage {
			t.Fatalf("interrupted run status=%q code=%q message=%q retryable=%t; want non-retryable failed/internal_error interruption",
				failed.Status, failed.Error.Code, failed.Error.Message, failed.Error.Retryable)
		}
		if failed.Summary != nil && *failed.Summary == "late result must not be published" {
			t.Fatal("late provider result was published after interruption")
		}
		before := adapter.calls.Load()
		event.Attempts++
		if err = host.Process(ctx, event); err != nil {
			t.Fatal(err)
		}
		if adapter.calls.Load() != before {
			t.Fatal("interrupted operation was replayed")
		}
		var state string
		if err = fixture.Migrator.QueryRow(ctx, `SELECT state FROM dayorder.agent_run_operations WHERE run_id=$1 AND kind='provider_turn'`, event.AggregateID).Scan(&state); err != nil {
			t.Fatal("read interrupted provider operation")
		}
		if state != "unknown" {
			t.Fatalf("interrupted provider operation state = %q, want unknown", state)
		}
	})
}

func TestBackgroundPersistsBusinessFailureAndPrioritizesExpiredDeadline(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Run("business failure is acknowledged", func(t *testing.T) {
		fake, err := agentprovider.NewFake(agentprovider.FakeConfig{
			Window: backgroundWindow(), Fault: string(agentprotocol.ErrorCodeProtocolIncompatible),
		})
		if err != nil {
			t.Fatal(err)
		}
		host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, fake)
		if err = host.Process(ctx, event); err != nil {
			t.Fatal(err)
		}
		failed, err := api.Runs.Get(ctx, fixture.UserA, event.AggregateID)
		if err != nil || failed.Status != agentprotocol.ReadonlyRunViewStatusFailed || failed.Error == nil || failed.Error.Code != agentprotocol.ErrorCodeProtocolIncompatible {
			t.Fatalf("business-failed run = %#v, error=%v", failed, err)
		}
	})

	t.Run("deadline before claim wins without provider execution", func(t *testing.T) {
		fake, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: backgroundWindow()})
		if err != nil {
			t.Fatal(err)
		}
		adapter := &countingFakeAdapter{fake: fake}
		host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, adapter)
		if _, err = fixture.Migrator.Exec(ctx, `UPDATE dayorder.agent_run_executions SET deadline=now()-interval '1 second' WHERE run_id=$1`, event.AggregateID); err != nil {
			t.Fatal("expire background run")
		}
		if err = host.Process(ctx, event); err != nil {
			t.Fatal(err)
		}
		failed, err := api.Runs.Get(ctx, fixture.UserA, event.AggregateID)
		if err != nil || failed.Status != agentprotocol.ReadonlyRunViewStatusFailed || failed.Error == nil || failed.Error.Code != agentprotocol.ErrorCodeTimeout {
			t.Fatalf("expired run = %#v, error=%v", failed, err)
		}
		if adapter.calls.Load() != 0 {
			t.Fatalf("expired run provider calls = %d, want 0", adapter.calls.Load())
		}
	})
}

type retryingOutbox struct {
	event         model.OutboxEvent
	claimCalls    int
	completeCalls int
}

func (repository *retryingOutbox) Claim(_ context.Context, _ int, token uuid.UUID, _ time.Duration) ([]model.OutboxEvent, error) {
	repository.claimCalls++
	event := repository.event
	event.Attempts += repository.claimCalls - 1
	event.LockToken = token
	return []model.OutboxEvent{event}, nil
}

func (repository *retryingOutbox) Complete(context.Context, uuid.UUID, uuid.UUID) error {
	repository.completeCalls++
	if repository.completeCalls == 1 {
		return errors.New("outbox completion failed")
	}
	return nil
}

func (repository *retryingOutbox) Retry(context.Context, model.OutboxRetry) error { return nil }

func TestBackgroundRunnerRetriesOutboxCompletionWithoutReplayingTheModel(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fake, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: backgroundWindow()})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &countingFakeAdapter{fake: fake}
	host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, adapter)
	repository := &retryingOutbox{event: event}
	handler, err := worker.NewReadonlyAgentHandler(host)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := worker.NewRunnerWithOptions(repository, map[string]worker.Handler{"agent.readonly.run.requested": handler}, worker.RunnerOptions{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunOnce(ctx); err == nil {
		t.Fatal("first RunOnce did not expose outbox Complete failure")
	}
	before := adapter.calls.Load()
	if before == 0 {
		t.Fatal("first delivery did not execute the provider")
	}
	if _, err = runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if adapter.calls.Load() != before || repository.completeCalls != 2 {
		t.Fatalf("redelivery provider calls=%d before=%d Complete calls=%d", adapter.calls.Load(), before, repository.completeCalls)
	}
}

func TestBackgroundRedeliveryMarksAPreviouslyExecutingRunInterruptedWithoutReplaying(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fake, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: backgroundWindow()})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &countingFakeAdapter{fake: fake}
	host, event := newBackgroundHarness(t, ctx, fixture, api, workerServices, adapter)
	claimed, err := workerServices.Runs.Claim(ctx, event.UserID, event.AggregateID, event.LockToken, false)
	if err != nil {
		t.Fatal(err)
	}
	actor := agentexecution.Actor{UserID: event.UserID, Token: event.LockToken, Mode: agentprotocol.ExecutionModeBackground}
	operation, err := workerServices.Runs.BeginOperation(ctx, actor, event.AggregateID, "provider_turn", "crashed-turn", []byte(`{}`), 100)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "running" || claimed.Run.Status != string(agentprotocol.ReadonlyRunViewStatusAnalyzing) {
		t.Fatalf("pre-crash claim=%#v operation=%#v", claimed, operation)
	}

	event.Attempts++
	event.LockToken = uuid.New()
	if err = host.Process(ctx, event); err != nil {
		t.Fatal(err)
	}
	failed, err := api.Runs.Get(ctx, event.UserID, event.AggregateID)
	if err != nil || failed.Status != agentprotocol.ReadonlyRunViewStatusFailed || failed.Error == nil ||
		failed.Error.Code != agentprotocol.ErrorCodeInternalError || failed.Error.Message != "execution_interrupted" {
		t.Fatalf("redelivered executing run = %#v, error=%v", failed, err)
	}
	if adapter.calls.Load() != 0 {
		t.Fatalf("redelivered executing run provider calls = %d, want 0", adapter.calls.Load())
	}
	var operationState string
	var usageComplete bool
	if err = fixture.Migrator.QueryRow(ctx, `SELECT state, usage_complete FROM dayorder.agent_run_operations WHERE run_id=$1 AND kind='provider_turn' AND operation_id='crashed-turn'`, event.AggregateID).Scan(&operationState, &usageComplete); err != nil {
		t.Fatal("read uncertain provider operation")
	}
	if operationState != "running" || usageComplete {
		t.Fatalf("uncertain provider operation state=%q usageComplete=%t", operationState, usageComplete)
	}
}

func TestBackgroundRejectsRuntimeBudgetThatCanOverlapOutboxRecovery(t *testing.T) {
	runID, userID, token := uuid.New(), uuid.New(), uuid.New()
	record := agentexecution.Record{
		Run: model.AgentRun{ID: runID, Status: string(agentprotocol.ReadonlyRunViewStatusAnalyzing)},
		Execution: agentexecution.Execution{
			UserID: userID, RunID: runID, Token: token, Mode: agentprotocol.ExecutionModeBackground,
			ProtocolVersion: "2.0", RuntimeVersion: "2.0.0", ModelProfile: "readonly-default",
			Deadline: time.Now().Add(time.Minute), Budget: agentprotocol.Budget{MaxDurationMs: 295000},
		},
	}
	event := model.OutboxEvent{UserID: userID, AggregateID: runID, LockToken: token}
	if err := validateClaimedReadonlyRecord(record, event); err == nil {
		t.Fatal("validateClaimedReadonlyRecord accepted budget plus finalization equal to the outbox stale window")
	}
}

type backgroundHarnessObservability struct {
	observer agentexecution.Observer
	logger   *slog.Logger
	calendar *service.AgentCalendarReadService
}

func newBackgroundHarness(t testing.TB, ctx context.Context, fixture *agenttest.Database, api, workerServices agenttest.Services, adapter agentprovider.Adapter, options ...backgroundHarnessObservability) (*Background, model.OutboxEvent) {
	t.Helper()
	var observed backgroundHarnessObservability
	if len(options) > 0 {
		observed = options[0]
	}
	window := backgroundWindow()
	from, to := string(window.Start), string(window.End)
	created, err := api.Runs.Create(ctx, service.MutationContext{
		UserID: fixture.UserA, DeviceID: fixture.DeviceA, MutationID: uuid.New(), RequestID: uuid.New(),
	}, agentprotocol.ReadonlyRunStart{
		Intent: "summarize the persisted calendar window", ExecutionMode: agentprotocol.ExecutionModeBackground,
		Scope:    agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
		Timezone: "UTC", ModelProfile: "readonly-default",
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(created.RunID))
	repository, err := postgresstore.NewOutboxRepository(fixture.Worker)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repository.Claim(ctx, 10, uuid.New(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var event model.OutboxEvent
	for _, candidate := range events {
		if candidate.AggregateID == runID {
			event = candidate
			break
		}
	}
	if event.ID == uuid.Nil {
		t.Fatalf("created run %s had no claimed outbox event: %#v", runID, events)
	}
	calendar, err := service.NewAgentCalendarReadService(workerServices.Runs, workerServices.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	if observed.calendar != nil {
		calendar = observed.calendar
	}
	gateway, err := agentgateway.New(agentgateway.Config{
		Runs:     workerServices.Runs,
		Profiles: []agentgateway.Profile{{ID: "readonly-default", Model: "fixture-model", Adapter: adapter}},
		Tools:    readonlyGatewayTools(t),
		Observer: observed.observer,
		Logger:   observed.logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewBackground(Config{
		Runs: workerServices.Runs, Calendar: calendar, Gateway: gateway,
		Observer: observed.observer, Logger: observed.logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return host, event
}

func backgroundWindow() agentprotocol.CalendarReadInput {
	return agentprotocol.CalendarReadInput{
		Start: agentprotocol.DateTime(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)),
		End:   agentprotocol.DateTime(time.Now().UTC().Add(time.Hour).Format(time.RFC3339)),
	}
}

func waitBackgroundResult(result <-chan error) error {
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("background Process did not return")
	}
}

func readonlyGatewayTools(t testing.TB) []agentprotocol.ToolSpec {
	t.Helper()
	meta := agentskill.MetaBindings(nil, agentprotocol.CapabilitySnapshot{}, nil, agenttool.Policy{})
	calendar, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return []agentprotocol.ToolSpec{meta[0].Spec(), meta[1].Spec(), calendar}
}
