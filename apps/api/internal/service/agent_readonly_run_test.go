package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingAgentObserver struct {
	mu           sync.Mutex
	observations []agentexecution.Observation
}

func (observer *recordingAgentObserver) ObserveAgent(observation agentexecution.Observation) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(observer.observations, observation)
}

func (observer *recordingAgentObserver) snapshot() []agentexecution.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]agentexecution.Observation(nil), observer.observations...)
}

func TestAgentReadonlyObservabilityEmitsOnePostCommitRunOutcomeWithoutPrivateText(t *testing.T) {
	store := &memoryReadonlyStore{}
	observer := &recordingAgentObserver{}
	var logs bytes.Buffer
	current := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	config := defaultReadonlyUnitConfig()
	config.Now = func() time.Time { return current }
	config.Observer = observer
	config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	runs := newReadonlyUnitService(t, store, config)
	mutation := readonlyMutation()
	input := validReadonlyStart(agentprotocol.ExecutionModeBackground)
	input.Intent = "calendar title canary Cookie=private api_key=secret"
	created, err := runs.Create(context.Background(), mutation, input)
	if err != nil {
		t.Fatal(err)
	}
	record := store.created
	store.record = &record
	token := uuid.New()
	claimed, err := runs.Claim(context.Background(), mutation.UserID, uuid.MustParse(string(created.RunID)), token, false)
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(2 * time.Second)
	actor := agentexecution.Actor{UserID: mutation.UserID, Token: token, Mode: agentprotocol.ExecutionModeBackground}
	failure := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeToolFailed, Message: "provider canary Cookie=private api_key=secret", Retryable: false}
	if err = runs.Fail(context.Background(), actor, claimed.Run.ID, failure); err != nil {
		t.Fatal(err)
	}
	if err = runs.Fail(context.Background(), actor, claimed.Run.ID, failure); err != nil {
		t.Fatal(err)
	}

	observations := observer.snapshot()
	if len(observations) != 1 {
		t.Fatalf("run observations = %#v, want one newly committed terminal", observations)
	}
	want := agentexecution.Observation{
		Kind: "run", Mode: "background", ModelProfile: "readonly-default", Outcome: "failed",
		ErrorCode: "tool_failed", Duration: 2 * time.Second, UsageComplete: true,
	}
	if observations[0] != want {
		t.Fatalf("run observation = %#v, want %#v", observations[0], want)
	}
	logged := logs.String()
	for _, required := range []string{"agent run completed", "runId=" + claimed.Run.ID.String(), "profile=readonly-default", "errorCode=tool_failed", "durationMs=2000"} {
		if !strings.Contains(logged, required) {
			t.Errorf("controlled log missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"calendar title canary", "Cookie=private", "api_key=secret", failure.Message} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("controlled log exposed %q: %s", forbidden, logged)
		}
	}
}

func TestAgentReadonlyCancellationLatencyUsesStoredTransitionAndExactActor(t *testing.T) {
	requestedAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	userID, runID, token := uuid.New(), uuid.New(), uuid.New()
	code := string(agentprotocol.ErrorCodeCancelled)
	store := &memoryReadonlyStore{record: &agentexecution.Record{
		Run:       model.AgentRun{ID: runID, Status: string(agentprotocol.ReadonlyRunViewStatusStopped), ErrorCode: &code, FinishedAt: &requestedAt},
		Execution: agentexecution.Execution{UserID: userID, RunID: runID, Token: token, Mode: agentprotocol.ExecutionModeBackground},
	}}
	runs := newReadonlyUnitService(t, store, defaultReadonlyUnitConfig())
	actor := agentexecution.Actor{UserID: userID, Token: token, Mode: agentprotocol.ExecutionModeBackground}

	latency, present, err := runs.CancellationLatency(context.Background(), actor, runID, requestedAt.Add(240*time.Millisecond))
	if err != nil || !present || latency != 240*time.Millisecond {
		t.Fatalf("CancellationLatency() = %s, %t, %v; want 240ms, true, nil", latency, present, err)
	}
	wrongActor := actor
	wrongActor.Token = uuid.New()
	if _, present, err = runs.CancellationLatency(context.Background(), wrongActor, runID, requestedAt.Add(time.Second)); err == nil || present {
		t.Fatalf("CancellationLatency(wrong actor) = present %t, error %v; want absent authorization error", present, err)
	}
	store.record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusFailed)
	if _, present, err = runs.CancellationLatency(context.Background(), actor, runID, requestedAt.Add(time.Second)); err != nil || present {
		t.Fatalf("CancellationLatency(non-cancel terminal) = present %t, error %v; want absent nil", present, err)
	}
}

func TestAgentReadonlyCancelFailureLogsControlledMetadata(t *testing.T) {
	tests := []struct {
		name          string
		lastStage     string
		errorCategory string
		configure     func(*memoryReadonlyStore, error)
	}{
		{
			name: "load internal error", lastStage: "load_run", errorCategory: "internal",
			configure: func(store *memoryReadonlyStore, injected error) { store.getErr = injected },
		},
		{
			name: "save wrapped deadline", lastStage: "save_cancel", errorCategory: "deadline_exceeded",
			configure: func(store *memoryReadonlyStore, injected error) {
				store.saveErr = errors.Join(injected, context.DeadlineExceeded)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryReadonlyStore{}
			observer := &recordingAgentObserver{}
			var logs bytes.Buffer
			config := defaultReadonlyUnitConfig()
			config.Observer = observer
			config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			runs := newReadonlyUnitService(t, store, config)
			mutation := readonlyMutation()
			created, err := runs.Create(context.Background(), mutation, validReadonlyStart(agentprotocol.ExecutionModeForeground))
			if err != nil {
				t.Fatal(err)
			}
			record := store.created
			store.record = &record
			mutation.MutationID, mutation.RequestID = uuid.New(), uuid.New()
			secret := errors.New("cancel persistence Cookie=private sql=secret")
			test.configure(store, secret)

			_, err = runs.Cancel(context.Background(), mutation, uuid.MustParse(string(created.RunID)), int64(created.Version))
			if !errors.Is(err, secret) {
				t.Fatal("Cancel() did not preserve the injected error identity")
			}
			if store.record == nil {
				t.Fatal("failed Cancel() cleared the stored record")
			}
			if store.record.Run.Status != string(agentprotocol.ReadonlyRunViewStatusReady) || store.record.Run.FinishedAt != nil || store.record.Run.ErrorCode != nil {
				t.Fatalf("failed Cancel() changed terminal metadata: status=%s finished=%t error=%t", store.record.Run.Status, store.record.Run.FinishedAt != nil, store.record.Run.ErrorCode != nil)
			}
			if observations := observer.snapshot(); len(observations) != 0 {
				t.Fatalf("failed Cancel() emitted %d terminal observations", len(observations))
			}

			logged := logs.String()
			if count := strings.Count(logged, "agent readonly cancellation failed"); count != 1 {
				t.Fatalf("controlled cancellation log count = %d, want 1", count)
			}
			for _, required := range []string{
				"runId=" + string(created.RunID), "requestId=" + mutation.RequestID.String(),
				"lastStage=" + test.lastStage, "errorCategory=" + test.errorCategory, "contextState=active",
			} {
				if !strings.Contains(logged, required) {
					t.Errorf("controlled cancellation log missing %q", required)
				}
			}
			for index, forbidden := range []string{secret.Error(), "Cookie=private", "sql=secret", context.DeadlineExceeded.Error()} {
				if strings.Contains(logged, forbidden) {
					t.Errorf("controlled cancellation log exposed forbidden marker %d", index)
				}
			}
		})
	}
}

func TestAgentReadonlyCancelFailureCorrelatesEffectiveRequestID(t *testing.T) {
	explicitRequestID := uuid.MustParse("11111111-2222-4333-8444-555555555555")
	tests := []struct {
		name      string
		requestID uuid.UUID
	}{
		{name: "default request ID", requestID: uuid.Nil},
		{name: "explicit request ID", requestID: explicitRequestID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryReadonlyStore{}
			var logs bytes.Buffer
			config := defaultReadonlyUnitConfig()
			config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			config = configuredReadonlyUnitConfig(t, store, config)
			auditWriter, ok := config.AuditWriter.(*memoryAuditWriter)
			if !ok {
				t.Fatal("readonly unit config did not retain its audit writer")
			}
			runs, err := service.NewAgentReadonlyService(config)
			if err != nil {
				t.Fatal(err)
			}
			mutation := readonlyMutation()
			created, err := runs.Create(context.Background(), mutation, validReadonlyStart(agentprotocol.ExecutionModeForeground))
			if err != nil {
				t.Fatal(err)
			}
			record := store.created
			store.record = &record
			mutation.MutationID, mutation.RequestID = uuid.New(), test.requestID
			auditWriter.audits = nil
			injected := errors.New("controlled audit failure")
			auditWriter.recordErr = injected

			// The immediate transactor exposes the real Command audit boundary here;
			// it does not model database rollback or commit behavior.
			_, err = runs.Cancel(context.Background(), mutation, uuid.MustParse(string(created.RunID)), int64(created.Version))
			if !errors.Is(err, injected) {
				t.Fatal("Cancel() did not preserve the audit failure identity")
			}
			if len(auditWriter.audits) != 1 {
				t.Fatalf("Cancel() audit count = %d, want 1", len(auditWriter.audits))
			}
			effectiveRequestID := auditWriter.audits[0].RequestID
			if effectiveRequestID == uuid.Nil {
				t.Fatal("Cancel() sent a nil request ID to the command audit boundary")
			}
			if test.requestID != uuid.Nil && effectiveRequestID != test.requestID {
				t.Fatal("Cancel() changed the explicit request ID at the command audit boundary")
			}
			logged := logs.String()
			if count := strings.Count(logged, "agent readonly cancellation failed"); count != 1 {
				t.Fatalf("controlled cancellation log count = %d, want 1", count)
			}
			if !strings.Contains(logged, "requestId="+effectiveRequestID.String()) {
				t.Fatal("cancellation diagnostic request ID did not match the command audit boundary")
			}
		})
	}
}

func TestAgentReadonlyObservabilityCoversForegroundAndBackgroundTerminalOwners(t *testing.T) {
	t.Run("foreground cancel", func(t *testing.T) {
		store, observer, current, runs, mutation := newObservedReadonlyRun(t, agentprotocol.ExecutionModeForeground)
		mutation.MutationID, mutation.RequestID = uuid.New(), uuid.New()
		*current = current.Add(time.Second)
		view, err := runs.Cancel(context.Background(), mutation, store.created.Run.ID, 1)
		if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusStopped {
			t.Fatalf("Cancel() view=%#v error=%v", view, err)
		}
		assertSingleRunObservation(t, observer, "foreground", "cancelled", "cancelled", time.Second)
	})

	t.Run("foreground finish", func(t *testing.T) {
		store, observer, current, runs, mutation := newObservedReadonlyRun(t, agentprotocol.ExecutionModeForeground)
		mutation.MutationID, mutation.RequestID = uuid.New(), uuid.New()
		*current = current.Add(2 * time.Second)
		view, err := runs.Finish(context.Background(), mutation, store.created.Run.ID, 1, agentprotocol.ReadonlyRunFinish{
			Phase: agentprotocol.ReadonlyRunFinishPhaseCompleted, Summary: "done", Steps: []agentprotocol.ReadonlyRunFinishStepsElem{},
		})
		if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusCompleted {
			t.Fatalf("Finish() view=%#v error=%v", view, err)
		}
		assertSingleRunObservation(t, observer, "foreground", "completed", "", 2*time.Second)
	})

	t.Run("background complete", func(t *testing.T) {
		store, observer, current, runs, mutation := newObservedReadonlyRun(t, agentprotocol.ExecutionModeBackground)
		token := uuid.New()
		record := store.created
		record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusAnalyzing)
		record.Execution.Token = token
		started := *current
		record.Run.StartedAt = &started
		store.record = &record
		*current = current.Add(3 * time.Second)
		state := agentprotocol.RuntimeState{
			ProtocolVersion: record.Execution.ProtocolVersion, RunID: record.Run.ID.String(), ExecutionMode: record.Execution.Mode,
			Phase: agentprotocol.RuntimePhaseCompleted, Messages: []agentprotocol.Message{},
			CapabilitySnapshot: record.Execution.Capabilities, Budget: record.Execution.Budget, Usage: agentprotocol.Usage{},
		}
		actor := agentexecution.Actor{UserID: mutation.UserID, Token: token, Mode: agentprotocol.ExecutionModeBackground}
		if err := runs.Complete(context.Background(), actor, record.Run.ID, state, nil); err != nil {
			t.Fatal(err)
		}
		assertSingleRunObservation(t, observer, "background", "completed", "", 3*time.Second)
	})
}

func TestAgentReadonlyForegroundTerminalLogIncludesFrozenSkillDigest(t *testing.T) {
	store := &memoryReadonlyStore{}
	var logs bytes.Buffer
	config := defaultReadonlyUnitConfig()
	config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	runs := newReadonlyUnitService(t, store, config)
	mutation := readonlyMutation()
	input := validReadonlyStart(agentprotocol.ExecutionModeForeground)
	input.Intent = "intent canary Cookie=private api_key=secret"
	if _, err := runs.Create(context.Background(), mutation, input); err != nil {
		t.Fatal(err)
	}
	record := store.created
	store.record = &record
	mutation.MutationID, mutation.RequestID = uuid.New(), uuid.New()
	view, err := runs.Finish(context.Background(), mutation, record.Run.ID, 1, agentprotocol.ReadonlyRunFinish{
		Phase:   agentprotocol.ReadonlyRunFinishPhaseCompleted,
		Summary: "summary canary Cookie=private api_key=secret",
		Steps:   []agentprotocol.ReadonlyRunFinishStepsElem{},
	})
	if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusCompleted {
		t.Fatalf("Finish() view=%#v error=%v", view, err)
	}

	logged := logs.String()
	if strings.Count(logged, "agent run completed") != 1 {
		t.Fatalf("terminal lifecycle log count != 1: %s", logged)
	}
	for _, required := range []string{
		"runId=" + record.Run.ID.String(), "mode=foreground", "profile=readonly-default",
		"outcome=completed", "skillDigest=sha256:test",
	} {
		if !strings.Contains(logged, required) {
			t.Errorf("terminal lifecycle log missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"intent canary", "summary canary", "Cookie=private", "api_key=secret"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("terminal lifecycle log exposed %q: %s", forbidden, logged)
		}
	}
}

func TestAgentReadonlyObservabilityEmitsNewTimeoutButNotTerminalGet(t *testing.T) {
	store, observer, current, runs, mutation := newObservedReadonlyRun(t, agentprotocol.ExecutionModeForeground)
	*current = current.Add(3 * time.Minute)
	view, err := runs.Get(context.Background(), mutation.UserID, store.created.Run.ID)
	if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusFailed || view.Error == nil || view.Error.Code != agentprotocol.ErrorCodeTimeout {
		t.Fatalf("first Get() view=%#v error=%v", view, err)
	}
	assertSingleRunObservation(t, observer, "foreground", "failed", "timeout", 3*time.Minute)
	if _, err = runs.Get(context.Background(), mutation.UserID, store.created.Run.ID); err != nil {
		t.Fatal(err)
	}
	if observations := observer.snapshot(); len(observations) != 1 {
		t.Fatalf("terminal Get observations = %#v, want no replay", observations)
	}
}

func newObservedReadonlyRun(t testing.TB, mode agentprotocol.ExecutionMode) (*memoryReadonlyStore, *recordingAgentObserver, *time.Time, *service.AgentReadonlyService, service.MutationContext) {
	t.Helper()
	store := &memoryReadonlyStore{}
	observer := &recordingAgentObserver{}
	current := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	config := defaultReadonlyUnitConfig()
	config.Now = func() time.Time { return current }
	config.Observer = observer
	runs := newReadonlyUnitService(t, store, config)
	mutation := readonlyMutation()
	if _, err := runs.Create(context.Background(), mutation, validReadonlyStart(mode)); err != nil {
		t.Fatal(err)
	}
	record := store.created
	store.record = &record
	return store, observer, &current, runs, mutation
}

func assertSingleRunObservation(t testing.TB, observer *recordingAgentObserver, mode, outcome, code string, duration time.Duration) {
	t.Helper()
	observations := observer.snapshot()
	if len(observations) != 1 {
		t.Fatalf("run observations = %#v, want one", observations)
	}
	got := observations[0]
	if got.Kind != "run" || got.Mode != mode || got.ModelProfile != "readonly-default" || got.Outcome != outcome ||
		got.ErrorCode != code || got.Duration != duration || !got.UsageComplete {
		t.Fatalf("run observation = %#v", got)
	}
}

func TestReadonlyServiceRejectsExplicitEmptyEntityIDsAtCreateBoundary(t *testing.T) {
	runs := &memoryReadonlyStore{}
	readonly := newReadonlyUnitService(t, runs, defaultReadonlyUnitConfig())
	input := validReadonlyStart(agentprotocol.ExecutionModeForeground)
	input.Scope.EntityIds = []string{}

	_, err := readonly.Create(context.Background(), readonlyMutation(), input)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Create() error = %v, want ErrValidation", err)
	}
	if runs.createCalls != 0 {
		t.Fatalf("Create() reached store %d times", runs.createCalls)
	}
}

func TestReadonlyServiceFreezesConfigurationAtConstructorBoundary(t *testing.T) {
	runs := &memoryReadonlyStore{}
	config := defaultReadonlyUnitConfig()
	readonly := newReadonlyUnitService(t, runs, config)

	foreground := config.Capabilities[agentprotocol.ExecutionModeForeground]
	foreground.ToolIds[0] = "mutated.tool"
	foreground.Skills[0].Name = "mutated-skill"
	foreground.Scope.Domains[0] = "mutated-domain"
	config.Capabilities[agentprotocol.ExecutionModeForeground] = foreground
	config.Profiles[0] = "mutated-profile"

	view, err := readonly.Create(context.Background(), readonlyMutation(), validReadonlyStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runs.created.Execution.Capabilities.ToolIds, []string{"skill_list", "skill_load", "dayorder.calendar.read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored tools = %#v", got)
	}
	if got := runs.created.Execution.Capabilities.Skills; len(got) != 1 || got[0].Name != "calendar-overview" {
		t.Fatalf("stored skills = %#v", got)
	}
	if got := runs.created.Execution.Capabilities.Scope.Domains; len(got) != 1 || got[0] != "calendar" {
		t.Fatalf("stored scope = %#v", runs.created.Execution.Capabilities.Scope)
	}
	if view.ModelProfile != "readonly-default" {
		t.Fatalf("view profile = %q", view.ModelProfile)
	}
}

func TestReadonlyServiceRequiresTheCompleteFrozenToolGrant(t *testing.T) {
	valid := []string{"skill_list", "skill_load", "dayorder.calendar.read"}
	tests := []struct {
		name      string
		ids       []string
		wantError bool
	}{
		{name: "complete", ids: valid},
		{name: "permuted complete", ids: []string{"dayorder.calendar.read", "skill_load", "skill_list"}},
		{name: "missing meta", ids: []string{"skill_load", "dayorder.calendar.read"}, wantError: true},
		{name: "extra", ids: []string{"skill_list", "skill_load", "dayorder.calendar.read", "unexpected"}, wantError: true},
		{name: "duplicate", ids: []string{"skill_list", "skill_load", "dayorder.calendar.read", "skill_list"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := defaultReadonlyUnitConfig()
			for mode, snapshot := range config.Capabilities {
				snapshot.ToolIds = append([]string(nil), test.ids...)
				config.Capabilities[mode] = snapshot
			}
			config = configuredReadonlyUnitConfig(t, &memoryReadonlyStore{}, config)
			_, err := service.NewAgentReadonlyService(config)
			if test.wantError && err == nil {
				t.Fatal("NewAgentReadonlyService accepted an incomplete or non-exact Tool grant")
			}
			if !test.wantError && err != nil {
				t.Fatalf("NewAgentReadonlyService rejected complete Tool grant: %v", err)
			}
		})
	}
}

func TestReadonlyServiceCreatesAndIsolatesTheCompleteFrozenToolGrantInBothModes(t *testing.T) {
	for _, mode := range []agentprotocol.ExecutionMode{agentprotocol.ExecutionModeForeground, agentprotocol.ExecutionModeBackground} {
		t.Run(string(mode), func(t *testing.T) {
			store := &memoryReadonlyStore{}
			config := defaultReadonlyUnitConfig()
			for configuredMode, snapshot := range config.Capabilities {
				snapshot.ToolIds = []string{"skill_list", "skill_load", "dayorder.calendar.read"}
				config.Capabilities[configuredMode] = snapshot
			}
			readonly := newReadonlyUnitService(t, store, config)
			configured := config.Capabilities[mode]
			configured.ToolIds[0] = "mutated.after.constructor"
			config.Capabilities[mode] = configured

			view, err := readonly.Create(context.Background(), readonlyMutation(), validReadonlyStart(mode))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"skill_list", "skill_load", "dayorder.calendar.read"}
			if !reflect.DeepEqual(view.CapabilitySnapshot.ToolIds, want) || !reflect.DeepEqual(store.created.Execution.Capabilities.ToolIds, want) {
				t.Fatalf("frozen Tool grants: view=%v stored=%v want=%v", view.CapabilitySnapshot.ToolIds, store.created.Execution.Capabilities.ToolIds, want)
			}
			view.CapabilitySnapshot.ToolIds[0] = "mutated.after.create"
			if !reflect.DeepEqual(store.created.Execution.Capabilities.ToolIds, want) {
				t.Fatalf("returned Tool grant aliases stored record: %v", store.created.Execution.Capabilities.ToolIds)
			}
		})
	}
}

func TestReadonlyCompleteProjectsCommittedAssistantTextAsSummary(t *testing.T) {
	store := &memoryReadonlyStore{}
	runs := newReadonlyUnitService(t, store, defaultReadonlyUnitConfig())
	created, err := runs.Create(context.Background(), readonlyMutation(), validReadonlyStart(agentprotocol.ExecutionModeBackground))
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.New()
	record := store.created
	record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusAnalyzing)
	record.Execution.Token = token
	store.record = &record
	text := "3 calendar events: Planning; Focus; Review."
	state := agentprotocol.RuntimeState{
		ProtocolVersion: record.Execution.ProtocolVersion, RunID: string(created.RunID),
		ExecutionMode: record.Execution.Mode, Phase: agentprotocol.RuntimePhaseCompleted,
		Messages: []agentprotocol.Message{
			{Role: agentprotocol.MessageRoleUser, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &record.Run.Intent}}},
			{Role: agentprotocol.MessageRoleAssistant, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &text}}},
		},
		CapabilitySnapshot: record.Execution.Capabilities, Budget: record.Execution.Budget,
		Usage: agentprotocol.Usage{},
	}
	actor := agentexecution.Actor{UserID: record.Execution.UserID, Token: token, Mode: agentprotocol.ExecutionModeBackground}
	if err = runs.Complete(context.Background(), actor, record.Run.ID, state, nil); err != nil {
		t.Fatal(err)
	}
	if store.saved.Run.Summary == nil || *store.saved.Run.Summary != text {
		t.Fatalf("persisted summary = %v, want committed assistant text %q", store.saved.Run.Summary, text)
	}
}

func TestReadonlyCompletedSummaryUsesOnlyTheLastCommittedAssistantMessage(t *testing.T) {
	committedFirst, committedSecond := "committed ", "answer"
	unconfirmed := "unconfirmed draft"
	got := completeReadonlySummary(t, []agentprotocol.Message{
		{Role: agentprotocol.MessageRoleUser, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &unconfirmed}}},
		{Role: agentprotocol.MessageRoleAssistant, Content: []agentprotocol.ContentBlock{
			{Type: agentprotocol.ContentBlockTypeText, Text: &committedFirst},
			{Type: agentprotocol.ContentBlockTypeText, Text: &committedSecond},
		}},
	}, &unconfirmed)
	if got != "committed answer" {
		t.Fatalf("completed summary = %q, want last committed assistant text", got)
	}
}

func TestReadonlyCompletedSummaryExcludesPrivateRolesAndUnconfirmedDraft(t *testing.T) {
	private, unconfirmed := "private user payload", "unconfirmed draft"
	got := completeReadonlySummary(t, []agentprotocol.Message{{
		Role:    agentprotocol.MessageRoleUser,
		Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &private}},
	}}, &unconfirmed)
	if got != "" {
		t.Fatalf("completed summary = %q, want no user or draft content", got)
	}
}

func TestReadonlyCompletedSummaryBoundsMultibyteTextByUnicodeLength(t *testing.T) {
	long := strings.Repeat("日", 8001)
	got := completeReadonlySummary(t, []agentprotocol.Message{{
		Role:    agentprotocol.MessageRoleAssistant,
		Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &long}},
	}}, nil)
	if utf8.RuneCountInString(got) != 8000 || got != strings.Repeat("日", 8000) {
		t.Fatalf("completed multibyte summary rune count = %d, want 8000", utf8.RuneCountInString(got))
	}
}

func completeReadonlySummary(t testing.TB, messages []agentprotocol.Message, draft *string) string {
	t.Helper()
	store := &memoryReadonlyStore{}
	runs := newReadonlyUnitService(t, store, defaultReadonlyUnitConfig())
	created, err := runs.Create(context.Background(), readonlyMutation(), validReadonlyStart(agentprotocol.ExecutionModeBackground))
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.New()
	record := store.created
	record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusAnalyzing)
	record.Execution.Token = token
	store.record = &record
	state := agentprotocol.RuntimeState{
		ProtocolVersion: record.Execution.ProtocolVersion, RunID: string(created.RunID),
		ExecutionMode: record.Execution.Mode, Phase: agentprotocol.RuntimePhaseCompleted,
		Messages: messages, AssistantDraft: draft, CapabilitySnapshot: record.Execution.Capabilities,
		Budget: record.Execution.Budget, Usage: agentprotocol.Usage{},
	}
	actor := agentexecution.Actor{UserID: record.Execution.UserID, Token: token, Mode: agentprotocol.ExecutionModeBackground}
	if err = runs.Complete(context.Background(), actor, record.Run.ID, state, nil); err != nil {
		t.Fatal(err)
	}
	if store.saved.Run.Summary == nil {
		t.Fatal("completed run has no summary")
	}
	return *store.saved.Run.Summary
}

func TestReadonlyServiceUsesBoundedDefaultBudget(t *testing.T) {
	runs := &memoryReadonlyStore{}
	config := defaultReadonlyUnitConfig()
	config.Budget = agentprotocol.Budget{}
	readonly := newReadonlyUnitService(t, runs, config)

	view, err := readonly.Create(context.Background(), readonlyMutation(), validReadonlyStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	want := agentprotocol.Budget{MaxSteps: 8, MaxTokens: 16000, MaxDurationMs: 120000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2}
	if view.Budget != want {
		t.Fatalf("default budget = %#v, want %#v", view.Budget, want)
	}
	deadline, err := time.Parse(time.RFC3339Nano, string(view.DeadlineAt))
	if err != nil {
		t.Fatalf("parse deadline: %v", err)
	}
	if wantDeadline := config.Now().Add(120 * time.Second); !deadline.Equal(wantDeadline) {
		t.Fatalf("deadline = %s, want %s", deadline, wantDeadline)
	}
}

func TestReadonlyServiceRejectsInvalidCustomBudget(t *testing.T) {
	control := configuredReadonlyUnitConfig(t, &memoryReadonlyStore{}, defaultReadonlyUnitConfig())
	if _, err := service.NewAgentReadonlyService(control); err != nil {
		t.Fatalf("NewAgentReadonlyService(valid budget) error = %v", err)
	}

	tests := []struct {
		name   string
		budget agentprotocol.Budget
	}{
		{name: "partial", budget: agentprotocol.Budget{MaxSteps: 1}},
		{name: "over maximum", budget: agentprotocol.Budget{MaxSteps: 9, MaxTokens: 16000, MaxDurationMs: 120000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := configuredReadonlyUnitConfig(t, &memoryReadonlyStore{}, defaultReadonlyUnitConfig())
			config.Budget = test.budget
			if _, err := service.NewAgentReadonlyService(config); err == nil || !strings.Contains(err.Error(), "budget") {
				t.Fatalf("NewAgentReadonlyService(invalid budget) error = %v, want budget error", err)
			}
		})
	}
}

func TestReadonlyFinishRejectsStepThatCannotBePersisted(t *testing.T) {
	readonly := newReadonlyUnitService(t, &memoryReadonlyStore{}, defaultReadonlyUnitConfig())
	input := completedReadonlyFinish("summary")
	input.Steps = []agentprotocol.ReadonlyRunFinishStepsElem{{Title: " ", Detail: "detail"}}
	_, err := readonly.Finish(context.Background(), readonlyMutation(), uuid.New(), 1, input)
	if !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Finish() error = %v, want ErrValidation", err)
	}
}

func TestReadonlyClaimAndActorTokenBoundaries(t *testing.T) {
	readonly := newReadonlyUnitService(t, &memoryReadonlyStore{}, defaultReadonlyUnitConfig())
	if _, err := readonly.Claim(context.Background(), uuid.New(), uuid.New(), uuid.Nil, false); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Claim(nil token) error = %v, want ErrValidation", err)
	}
	if err := readonly.Fail(context.Background(), agentexecution.Actor{UserID: uuid.New(), Mode: agentprotocol.ExecutionModeBackground}, uuid.New(), agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "failed"}); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Fail(background nil token) error = %v, want ErrValidation", err)
	}
	if err := readonly.Fail(context.Background(), agentexecution.Actor{UserID: uuid.New(), Token: uuid.New(), Mode: agentprotocol.ExecutionModeForeground}, uuid.New(), agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "failed"}); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Fail(foreground token) error = %v, want ErrValidation", err)
	}
}

func TestReadonlyServiceRejectsCapabilityExpansion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*service.AgentReadonlyConfig)
	}{
		{name: "missing background mode", mutate: func(config *service.AgentReadonlyConfig) {
			delete(config.Capabilities, agentprotocol.ExecutionModeBackground)
		}},
		{name: "write tool", mutate: func(config *service.AgentReadonlyConfig) {
			snapshot := config.Capabilities[agentprotocol.ExecutionModeForeground]
			snapshot.ToolIds = []string{"dayorder.calendar.write"}
			config.Capabilities[agentprotocol.ExecutionModeForeground] = snapshot
		}},
		{name: "unapproved skill", mutate: func(config *service.AgentReadonlyConfig) {
			snapshot := config.Capabilities[agentprotocol.ExecutionModeForeground]
			snapshot.Skills[0].Name = "calendar-management"
			config.Capabilities[agentprotocol.ExecutionModeForeground] = snapshot
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := defaultReadonlyUnitConfig()
			runs := &memoryReadonlyStore{}
			transactor := immediateReadonlyTransactor{}
			idempotency, _ := service.NewIdempotencyService(&memoryMutationStore{})
			syncWriter, auditWriter, outboxWriter := &memorySyncWriter{}, &memoryAuditWriter{}, &memoryOutboxWriter{}
			commands, _ := service.NewCommandService(transactor, idempotency, syncWriter, auditWriter, outboxWriter)
			config.Store, config.Transactor, config.Commands = runs, transactor, commands
			config.SyncWriter, config.AuditWriter = syncWriter, auditWriter
			test.mutate(&config)
			if _, err := service.NewAgentReadonlyService(config); err == nil {
				t.Fatal("NewAgentReadonlyService() accepted expanded capabilities")
			}
		})
	}
}

func TestReadonlyCreateRejectsBlankIntentBeforePersistence(t *testing.T) {
	runs := &memoryReadonlyStore{}
	readonly := newReadonlyUnitService(t, runs, defaultReadonlyUnitConfig())
	input := validReadonlyStart(agentprotocol.ExecutionModeForeground)
	input.Intent = "  "
	if _, err := readonly.Create(context.Background(), readonlyMutation(), input); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Create(blank intent) error = %v, want ErrValidation", err)
	}
	if runs.createCalls != 0 {
		t.Fatalf("Create(blank intent) reached store %d times", runs.createCalls)
	}
}

func TestReadonlyCreateStartsBudgetAfterAccountAdmission(t *testing.T) {
	admittedAt := time.Date(2026, 9, 7, 12, 0, 10, 0, time.UTC)
	current := admittedAt.Add(-10 * time.Second)
	runs := &memoryReadonlyStore{onLock: func() { current = admittedAt }}
	config := defaultReadonlyUnitConfig()
	config.Now = func() time.Time { return current }
	config.Budget = agentprotocol.Budget{MaxSteps: 1, MaxTokens: 1, MaxDurationMs: 1000, MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 1}
	readonly := newReadonlyUnitService(t, runs, config)
	view, err := readonly.Create(context.Background(), readonlyMutation(), validReadonlyStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, string(view.DeadlineAt))
	if err != nil {
		t.Fatal(err)
	}
	if want := admittedAt.Add(time.Second); !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
}

func defaultReadonlyUnitConfig() service.AgentReadonlyConfig {
	capabilities := make(map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot, 2)
	for _, mode := range []agentprotocol.ExecutionMode{agentprotocol.ExecutionModeForeground, agentprotocol.ExecutionModeBackground} {
		capabilities[mode] = agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: mode,
			ToolIds: []string{"skill_list", "skill_load", "dayorder.calendar.read"},
			Skills:  []agentprotocol.SkillRef{{Name: "calendar-overview", Version: "1.0.0", Digest: "sha256:test"}},
			Scope:   agentprotocol.AgentScope{Domains: []string{"calendar"}},
		}
	}
	return service.AgentReadonlyConfig{
		Capabilities: capabilities, Profiles: []string{"readonly-default"},
		Now: func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) },
	}
}

func newReadonlyUnitService(t testing.TB, runs *memoryReadonlyStore, config service.AgentReadonlyConfig) *service.AgentReadonlyService {
	t.Helper()
	config = configuredReadonlyUnitConfig(t, runs, config)
	readonly, err := service.NewAgentReadonlyService(config)
	if err != nil {
		t.Fatal(err)
	}
	return readonly
}

func configuredReadonlyUnitConfig(t testing.TB, runs *memoryReadonlyStore, config service.AgentReadonlyConfig) service.AgentReadonlyConfig {
	t.Helper()
	transactor := immediateReadonlyTransactor{}
	idempotency, err := service.NewIdempotencyService(&memoryMutationStore{})
	if err != nil {
		t.Fatal(err)
	}
	syncWriter := &memorySyncWriter{}
	auditWriter := &memoryAuditWriter{}
	outboxWriter := &memoryOutboxWriter{}
	commands, err := service.NewCommandService(transactor, idempotency, syncWriter, auditWriter, outboxWriter)
	if err != nil {
		t.Fatal(err)
	}
	config.Store = runs
	config.Transactor = transactor
	config.Commands = commands
	config.SyncWriter = syncWriter
	config.AuditWriter = auditWriter
	return config
}

func validReadonlyStart(mode agentprotocol.ExecutionMode) agentprotocol.ReadonlyRunStart {
	from := "2026-09-07T00:00:00Z"
	to := "2026-09-08T00:00:00Z"
	return agentprotocol.ReadonlyRunStart{
		Intent: "summarize my calendar", ExecutionMode: mode,
		Scope:    agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
		Timezone: "Asia/Shanghai", ModelProfile: "readonly-default",
	}
}

func readonlyMutation() service.MutationContext {
	return service.MutationContext{UserID: uuid.New(), DeviceID: uuid.New(), MutationID: uuid.New(), RequestID: uuid.New()}
}

type immediateReadonlyTransactor struct{}

func (immediateReadonlyTransactor) WithUser(ctx context.Context, _ uuid.UUID, operation func(context.Context, database.Tx) error) error {
	return operation(ctx, readonlyTestTx{})
}

type readonlyTestTx struct{}

func (readonlyTestTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (readonlyTestTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (readonlyTestTx) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (readonlyTestTx) Commit(context.Context) error                            { return nil }
func (readonlyTestTx) Rollback(context.Context) error                          { return nil }

type memoryReadonlyStore struct {
	createCalls int
	created     agentexecution.Record
	record      *agentexecution.Record
	saved       agentexecution.Record
	onLock      func()
	getErr      error
	saveErr     error
}

func (store *memoryReadonlyStore) LockAccount(context.Context, database.Tx, uuid.UUID) error {
	if store.onLock != nil {
		store.onLock()
	}
	return nil
}
func (store *memoryReadonlyStore) Create(_ context.Context, _ database.Tx, record agentexecution.Record) error {
	store.createCalls++
	store.created = record
	return nil
}

func (store *memoryReadonlyStore) Get(context.Context, database.Tx, uuid.UUID, uuid.UUID, bool) (agentexecution.Record, error) {
	if store.getErr != nil {
		return agentexecution.Record{}, store.getErr
	}
	if store.record != nil {
		return *store.record, nil
	}
	return agentexecution.Record{}, model.ErrNotFound
}
func (*memoryReadonlyStore) Active(context.Context, database.Tx, uuid.UUID) ([]agentexecution.Record, error) {
	return nil, nil
}
func (*memoryReadonlyStore) CountCreatedSince(context.Context, database.Tx, uuid.UUID, time.Time) (int, error) {
	return 0, nil
}
func (store *memoryReadonlyStore) Save(_ context.Context, _ database.Tx, record agentexecution.Record, _ int64, _ uuid.UUID) error {
	if store.saveErr != nil {
		return store.saveErr
	}
	store.saved = record
	store.record = &store.saved
	return nil
}
func (*memoryReadonlyStore) Operations(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]agentexecution.Operation, error) {
	return nil, nil
}
func (*memoryReadonlyStore) PutOperation(context.Context, database.Tx, agentexecution.Operation, string) error {
	return nil
}
func (*memoryReadonlyStore) AddRefs(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentSourceRefDraft) error {
	return nil
}
func (*memoryReadonlyStore) AddSteps(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentStepDraft) error {
	return nil
}

type memoryMutationStore struct {
	mu        sync.Mutex
	mutations map[uuid.UUID]model.ClientMutation
}

func (store *memoryMutationStore) Claim(_ context.Context, _ database.Tx, draft model.ClientMutationDraft) (model.ClientMutation, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.mutations == nil {
		store.mutations = make(map[uuid.UUID]model.ClientMutation)
	}
	if existing, ok := store.mutations[draft.MutationID]; ok {
		return existing, false, nil
	}
	mutation := model.ClientMutation{ID: draft.ID, UserID: draft.UserID, DeviceID: draft.DeviceID, MutationID: draft.MutationID, RequestHash: append([]byte(nil), draft.RequestHash...), ExpiresAt: draft.ExpiresAt}
	store.mutations[draft.MutationID] = mutation
	return mutation, true, nil
}

func (store *memoryMutationStore) Complete(_ context.Context, _ database.Tx, userID, id uuid.UUID, status int, body []byte) (model.ClientMutation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, mutation := range store.mutations {
		if mutation.ID == id && mutation.UserID == userID {
			mutation.ResponseStatus = &status
			mutation.ResponseBody = append([]byte(nil), body...)
			store.mutations[key] = mutation
			return mutation, nil
		}
	}
	return model.ClientMutation{}, model.ErrNotFound
}

type memorySyncWriter struct{}

func (*memorySyncWriter) Record(context.Context, database.Tx, uuid.UUID, []model.SyncChangeDraft) error {
	return nil
}

type memoryAuditWriter struct {
	audits    []model.AuditDraft
	recordErr error
}

func (writer *memoryAuditWriter) Record(_ context.Context, _ database.Tx, _ uuid.UUID, audits []model.AuditDraft) error {
	writer.audits = append(writer.audits, audits...)
	return writer.recordErr
}

type memoryOutboxWriter struct{}

func (*memoryOutboxWriter) Record(context.Context, database.Tx, uuid.UUID, []model.OutboxDraft) error {
	return nil
}
