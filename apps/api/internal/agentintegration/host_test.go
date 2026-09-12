package agentintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/worker"

	"github.com/google/uuid"
)

type lifecycleFixture struct {
	name       string
	closeErr   error
	closeCalls int
}

func (fixture *lifecycleFixture) DatabaseName() string { return fixture.name }

func (fixture *lifecycleFixture) Close(context.Context) error {
	fixture.closeCalls++
	return fixture.closeErr
}

func TestOwnedIntegrationFixtureLogsGuardedCleanupOnlyAfterSuccessfulClose(t *testing.T) {
	for _, test := range []struct {
		name        string
		closeErr    error
		wantSuccess bool
	}{
		{name: "verified close", wantSuccess: true},
		{name: "failed close", closeErr: errors.New("private connection detail")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			fixture := &lifecycleFixture{name: "dayorder_agent_it_0123456789abcdef0123456789abcdef", closeErr: test.closeErr}

			err := closeOwnedIntegrationFixture(context.Background(), logger, fixture, "confighub")

			if !errors.Is(err, test.closeErr) {
				t.Fatalf("close error = %v, want %v", err, test.closeErr)
			}
			if fixture.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", fixture.closeCalls)
			}
			logOutput := output.String()
			hasSuccess := strings.Contains(logOutput, `"outcome":"guarded_cleanup"`)
			if hasSuccess != test.wantSuccess {
				t.Fatalf("guarded cleanup success log = %v, want %v; output=%s", hasSuccess, test.wantSuccess, logOutput)
			}
			if !strings.Contains(logOutput, `"databaseName":"dayorder_agent_it_0123456789abcdef0123456789abcdef"`) {
				t.Fatalf("database identity missing from lifecycle log: %s", logOutput)
			}
			if !strings.Contains(logOutput, `"databaseSource":"confighub"`) {
				t.Fatalf("database source missing from lifecycle log: %s", logOutput)
			}
			if strings.Contains(logOutput, "private connection detail") {
				t.Fatalf("private cleanup error leaked into lifecycle log: %s", logOutput)
			}
		})
	}
}

func TestAgentIntegrationHostRejectsProductionBeforeDatabase(t *testing.T) {
	t.Setenv("DAYORDER_AGENT_TEST_DB_SOURCE", "not-a-database-source")
	_, err := Start(context.Background(), Config{Environment: config.Production})
	if err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("production integration host error = %v", err)
	}
}

func TestAgentIntegrationHostRejectsNonLoopbackOriginBeforeDatabase(t *testing.T) {
	t.Setenv("DAYORDER_AGENT_TEST_DB_SOURCE", "not-a-database-source")
	_, err := Start(context.Background(), Config{
		Environment: config.Test, AllowedOrigins: []string{"http://example.com:5173"},
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback origin error = %v", err)
	}
}

func TestAgentIntegrationHostReturnsSelectedDatabaseBackendFailure(t *testing.T) {
	t.Setenv("DAYORDER_AGENT_TEST_DB_SOURCE", "confighub")
	t.Setenv("db_address", "db.invalid")
	t.Setenv("db_port", "5432")
	t.Setenv("db_username", "fixture_admin")
	t.Setenv("db_password", "")
	t.Setenv("db_migrator_password", "migrator-secret")
	t.Setenv("db_api_password", "api-secret")
	t.Setenv("db_worker_password", "worker-secret")

	_, err := Start(context.Background(), Config{
		Environment: config.Test, AllowedOrigins: []string{"http://127.0.0.1:5173"},
	})
	if err == nil || !strings.Contains(err.Error(), "db_password") {
		t.Fatalf("selected ConfigHub backend failure = %v", err)
	}
}

func TestDiagnosticRequestBoundaryRejectsRebindingHostAndRequiresPostOrigin(t *testing.T) {
	allowed, err := allowedLoopbackOrigins([]string{"http://127.0.0.1:5173"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, host, origin string
		want                       bool
	}{
		{name: "same-origin GET omits Origin", method: "GET", host: "127.0.0.1:5173", want: true},
		{name: "allowed GET Origin", method: "GET", host: "localhost:5173", origin: "http://127.0.0.1:5173", want: true},
		{name: "rebinding Host", method: "GET", host: "attacker.example", want: false},
		{name: "untrusted GET Origin", method: "GET", host: "127.0.0.1:5173", origin: "http://localhost:9999", want: false},
		{name: "POST omits Origin", method: "POST", host: "127.0.0.1:5173", want: false},
		{name: "POST allowed Origin", method: "POST", host: "127.0.0.1:5173", origin: "http://127.0.0.1:5173", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://"+test.host+"/__test/config", nil)
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if got := trustedDiagnosticRequest(request, allowed); got != test.want {
				t.Fatalf("trustedDiagnosticRequest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFaultControlAcceptsOnlyFixedScenarios(t *testing.T) {
	control := &faultControl{}
	for _, fault := range []string{
		"tool_timeout", "run_timeout", "provider_429", "provider_disconnect",
		"complete_once", "commit_once", "interrupted",
	} {
		if err := control.Set(fault); err != nil {
			t.Fatalf("Set(%q) error = %v", fault, err)
		}
	}
	for _, fault := range []string{"", "SELECT 1", "https://example.com", "complete_twice"} {
		if err := control.Set(fault); err == nil {
			t.Fatalf("Set(%q) accepted", fault)
		}
	}
}

func TestDiagnosticHandlerEnforcesOriginAndSessionMatrix(t *testing.T) {
	allowed, err := allowedLoopbackOrigins([]string{"http://127.0.0.1:5173"})
	if err != nil {
		t.Fatal(err)
	}
	handler := newControlHandler(http.NotFoundHandler(), nil, nil, nil, fixtureConfig{}, &faultControl{}, allowed)
	tests := []struct {
		name, method, path, origin, contentType string
		want                                    int
	}{
		{name: "public config same origin", method: http.MethodGet, path: "/__test/config", want: http.StatusOK},
		{name: "public config allowed origin", method: http.MethodGet, path: "/__test/config", origin: "http://127.0.0.1:5173", want: http.StatusOK},
		{name: "config rejects untrusted origin", method: http.MethodGet, path: "/__test/config", origin: "http://127.0.0.1:9999", want: http.StatusForbidden},
		{name: "fault requires origin", method: http.MethodPost, path: "/__test/fault", contentType: "application/json", want: http.StatusForbidden},
		{name: "fault requires session", method: http.MethodPost, path: "/__test/fault", origin: "http://127.0.0.1:5173", contentType: "application/json", want: http.StatusUnauthorized},
		{name: "run diagnostics require session", method: http.MethodGet, path: "/__test/runs/" + uuid.NewString(), want: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://127.0.0.1:8080"+test.path, strings.NewReader(`{"fault":"provider_429"}`))
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body_bytes=%d", response.Code, test.want, response.Body.Len())
			}
		})
	}
}

func TestAgentTestConstructorsReturnErrorsWithoutTestingTB(t *testing.T) {
	if _, err := agenttest.NewDatabase(context.Background(), nil); err == nil {
		t.Fatal("NewDatabase(nil) accepted")
	}
	if _, err := agenttest.BuildServices(nil, config.DatabaseRoleAPI, agenttest.ServicesOptions{}); err == nil {
		t.Fatal("BuildServices(nil) accepted")
	}
	for _, role := range []config.DatabaseRole{"", config.DatabaseRoleMigrator, "dayorder_unknown"} {
		if _, err := agenttest.BuildServices(&agenttest.Database{}, role, agenttest.ServicesOptions{}); err == nil {
			t.Fatalf("BuildServices(role=%q) accepted", role)
		}
	}
}

func TestControlledCalendarStoreScopesTimeoutToTaggedRunAndOwner(t *testing.T) {
	owner := uuid.New()
	otherOwner := uuid.New()
	targetRun := uuid.New()
	otherRun := uuid.New()
	tests := []struct {
		name      string
		contextID uuid.UUID
		userID    uuid.UUID
		fault     string
		wantBlock bool
	}{
		{name: "target run and owner", contextID: targetRun, userID: owner, fault: "tool_timeout", wantBlock: true},
		{name: "missing run tag", userID: owner, fault: "tool_timeout"},
		{name: "different run", contextID: otherRun, userID: owner, fault: "tool_timeout"},
		{name: "different owner", contextID: targetRun, userID: otherOwner, fault: "tool_timeout"},
		{name: "different fault", contextID: targetRun, userID: owner, fault: "provider_429"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := committedFaultControl(owner, targetRun, test.fault)
			base := &recordingCalendarStore{}
			store := controlledCalendarStore{CalendarStore: base, control: control}
			ctx := context.Background()
			if test.contextID != uuid.Nil {
				ctx = withFaultRun(ctx, test.contextID)
			}
			ctx, cancel := context.WithCancel(ctx)
			if test.wantBlock {
				cancel()
			}
			_, err := store.ListEvents(ctx, nil, test.userID, nil, nil, nil, 20)
			cancel()
			if test.wantBlock {
				if !errors.Is(err, context.Canceled) || base.calls.Load() != 0 {
					t.Fatalf("target timeout err=%v delegate calls=%d", err, base.calls.Load())
				}
				return
			}
			if err != nil || base.calls.Load() != 1 {
				t.Fatalf("unrelated calendar err=%v delegate calls=%d", err, base.calls.Load())
			}
		})
	}
}

func TestRunContextBoundariesTagOnlyCanonicalCalendarAndReadonlyEvent(t *testing.T) {
	runID := uuid.New()
	var httpRun uuid.UUID
	application := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		httpRun, _ = faultRunFromContext(request.Context())
	})
	handler := newControlHandler(application, nil, nil, nil, fixtureConfig{}, &faultControl{}, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/runs/"+runID.String()+"/tools/calendar-read", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if httpRun != runID {
		t.Fatalf("HTTP calendar run context=%s want=%s", httpRun, runID)
	}

	var workerRun uuid.UUID
	wrapped := runContextHandler{base: workerHandlerFunc(func(ctx context.Context, _ model.OutboxEvent) error {
		workerRun, _ = faultRunFromContext(ctx)
		return nil
	})}
	event := model.OutboxEvent{EventType: "agent.readonly.run.requested", AggregateID: runID}
	if err := wrapped.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if workerRun != runID {
		t.Fatalf("Worker run context=%s want=%s", workerRun, runID)
	}
}

func TestProvider429FaultFailsOnceThenDelegates(t *testing.T) {
	accountID := uuid.New()
	runID := uuid.New()
	control := committedFaultControl(accountID, runID, "provider_429")
	base := &recordingProviderAdapter{}
	adapter := controlledAdapter{base: base, control: control}
	request := agentprotocol.ModelTurnRequest{RunID: runID.String()}

	_, firstErrs := collectProvider(adapter.Stream(context.Background(), request, agentprovider.TurnOptions{}))
	if len(firstErrs) != 1 {
		t.Fatalf("first Provider error count=%d", len(firstErrs))
	}
	var providerErr *agentprovider.ProviderError
	if !errors.As(firstErrs[0], &providerErr) || providerErr.Status != http.StatusTooManyRequests || !providerErr.Retryable {
		t.Fatalf("first Provider error metadata: classified=%t status=%d retryable=%t", providerErr != nil, providerErrorStatus(providerErr), providerErr != nil && providerErr.Retryable)
	}
	secondEvents, secondErrs := collectProvider(adapter.Stream(context.Background(), request, agentprovider.TurnOptions{}))
	thirdEvents, thirdErrs := collectProvider(adapter.Stream(context.Background(), request, agentprovider.TurnOptions{}))
	if len(secondErrs) != 0 || len(thirdErrs) != 0 || len(secondEvents) != 1 || len(thirdEvents) != 1 || base.calls.Load() != 2 {
		t.Fatalf("delegation metadata: calls=%d second_events=%d second_errors=%d third_events=%d third_errors=%d", base.calls.Load(), len(secondEvents), len(secondErrs), len(thirdEvents), len(thirdErrs))
	}
}

func providerErrorStatus(failure *agentprovider.ProviderError) int {
	if failure == nil {
		return 0
	}
	return failure.Status
}

type recordingCalendarStore struct {
	service.CalendarStore
	calls atomic.Int32
}

func (store *recordingCalendarStore) ListEvents(context.Context, database.Tx, uuid.UUID, *time.Time, *time.Time, *model.ResourcePosition, int) ([]model.CalendarEvent, error) {
	store.calls.Add(1)
	return []model.CalendarEvent{}, nil
}

type workerHandlerFunc func(context.Context, model.OutboxEvent) error

func (handler workerHandlerFunc) Handle(ctx context.Context, event model.OutboxEvent) error {
	return handler(ctx, event)
}

var _ service.CalendarStore = (*recordingCalendarStore)(nil)
var _ worker.Handler = workerHandlerFunc(nil)

func executionRecord(userID, runID uuid.UUID) agentexecution.Record {
	return agentexecution.Record{Run: model.AgentRun{ID: runID}, Execution: agentexecution.Execution{UserID: userID, RunID: runID}}
}

type recordingProviderAdapter struct {
	calls atomic.Int32
}

func (adapter *recordingProviderAdapter) Stream(context.Context, agentprotocol.ModelTurnRequest, agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		adapter.calls.Add(1)
		yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted}, nil)
	}
}

func collectProvider(sequence iter.Seq2[agentprotocol.ProviderEvent, error]) ([]agentprotocol.ProviderEvent, []error) {
	var events []agentprotocol.ProviderEvent
	var failures []error
	for event, err := range sequence {
		if err != nil {
			failures = append(failures, err)
		} else {
			events = append(events, event)
		}
	}
	return events, failures
}

var _ agentexecution.Store = (*recordingCreateStore)(nil)
var _ agentprovider.Adapter = (*recordingProviderAdapter)(nil)

func TestAgentIntegrationHostLiveLifecycle(t *testing.T) {
	if os.Getenv("DAYORDER_AGENT_INTEGRATION_LIVE") != "1" {
		t.Skip("set DAYORDER_AGENT_INTEGRATION_LIVE=1 through the owned database launcher")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	host, err := Start(ctx, Config{
		Environment: config.Test, AllowedOrigins: []string{"http://127.0.0.1:5173"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixtureName := host.fixture.DatabaseName()
	t.Logf("created owned fixture %s", fixtureName)
	closed := false
	defer func() {
		if closed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if closeErr := host.Close(cleanupCtx); closeErr != nil {
			t.Errorf("cleanup incomplete for %s: %v", fixtureName, closeErr)
			return
		}
		t.Logf("guarded-deleted owned fixture %s", fixtureName)
	}()

	parsedURL, err := url.Parse(host.APIURL())
	if err != nil || parsedURL.Scheme != "http" || !loopbackHost(parsedURL.Host) {
		t.Fatalf("APIURL() = %q", host.APIURL())
	}
	configResponse, err := http.Get(host.APIURL() + "/__test/config")
	if err != nil {
		t.Fatal(err)
	}
	rawConfig, err := io.ReadAll(configResponse.Body)
	configResponse.Body.Close()
	if err != nil || configResponse.StatusCode != http.StatusOK {
		t.Fatalf("config status=%d body_bytes=%d decode_error=%v", configResponse.StatusCode, len(rawConfig), err)
	}
	if bytes.Contains(bytes.ToLower(rawConfig), []byte("postgres")) || bytes.Contains(rawConfig, []byte("providerKey")) {
		t.Fatalf("diagnostic config exposed a secret source: body_bytes=%d", len(rawConfig))
	}
	var fixture fixtureConfig
	if err = json.Unmarshal(rawConfig, &fixture); err != nil || len(fixture.Accounts) != 2 || fixture.Profile != "readonly-fake" {
		t.Fatalf("fixture config decode failed: body_bytes=%d error=%v", len(rawConfig), err)
	}
	if fixture.Window.Start != "2026-09-05T00:00:00Z" || fixture.Window.End != "2026-09-06T00:00:00Z" {
		t.Fatalf("fixture window metadata invalid: start_match=%t end_match=%t", fixture.Window.Start == "2026-09-05T00:00:00Z", fixture.Window.End == "2026-09-06T00:00:00Z")
	}

	clientA := authenticatedFixtureClient(t, host.APIURL(), fixture.Accounts[0])
	clientB := authenticatedFixtureClient(t, host.APIURL(), fixture.Accounts[1])
	completed := createFixtureRun(t, clientA, host.APIURL(), fixture, 0, "background")
	if completed.Budget.MaxDurationMs != 120_000 {
		t.Fatalf("normal run max duration=%d", completed.Budget.MaxDurationMs)
	}
	runID := uuid.MustParse(string(completed.RunID))
	otherResponse := fixtureRequest(t, clientB, http.MethodGet, host.APIURL()+"/api/v1/agent/runs/"+runID.String(), nil, fixture.Accounts[1])
	if otherResponse.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(otherResponse.Body)
		otherResponse.Body.Close()
		t.Fatalf("other account run status=%d body_bytes=%d", otherResponse.StatusCode, len(body))
	}
	otherResponse.Body.Close()
	otherDiagnostic := fixtureRequest(t, clientB, http.MethodGet, host.APIURL()+"/__test/runs/"+runID.String(), nil, fixture.Accounts[1])
	if otherDiagnostic.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(otherDiagnostic.Body)
		otherDiagnostic.Body.Close()
		t.Fatalf("other account diagnostic status=%d body_bytes=%d", otherDiagnostic.StatusCode, len(body))
	}
	otherDiagnostic.Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for !terminalRunStatus(string(completed.Status)) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		response := fixtureRequest(t, clientA, http.MethodGet, host.APIURL()+"/api/v1/agent/runs/"+runID.String(), nil, fixture.Accounts[0])
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("poll status=%d body_bytes=%d", response.StatusCode, len(body))
		}
		if err = json.NewDecoder(response.Body).Decode(&completed); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
	}
	if completed.Status != agentprotocol.ReadonlyRunViewStatusCompleted || completed.Summary == nil ||
		!strings.Contains(*completed.Summary, "Architecture review A") || strings.Contains(*completed.Summary, "Private planning B") {
		t.Fatalf("background result metadata: status=%s summary_present=%t expected_summary=%t forbidden_summary=%t", completed.Status, completed.Summary != nil, completed.Summary != nil && strings.Contains(*completed.Summary, "1 event"), completed.Summary != nil && strings.Contains(*completed.Summary, "Private planning B"))
	}
	diagnosticResponse := fixtureRequest(t, clientA, http.MethodGet, host.APIURL()+"/__test/runs/"+runID.String(), nil, fixture.Accounts[0])
	var state testRunState
	if diagnosticResponse.StatusCode != http.StatusOK || json.NewDecoder(diagnosticResponse.Body).Decode(&state) != nil {
		body, _ := io.ReadAll(diagnosticResponse.Body)
		diagnosticResponse.Body.Close()
		t.Fatalf("diagnostic status=%d body_bytes=%d", diagnosticResponse.StatusCode, len(body))
	}
	diagnosticResponse.Body.Close()
	if state.Status != "completed" || state.OutboxCount != 1 || state.OutboxStatus == nil || *state.OutboxStatus != "processed" ||
		state.Deliveries != 1 || state.ProviderCalls != 4 || state.CalendarCalls != 1 || len(state.SourceRefs) != 1 {
		t.Fatalf("diagnostic metadata: status=%s outbox_count=%d outbox_status_present=%t deliveries=%d provider_calls=%d calendar_calls=%d source_refs=%d", state.Status, state.OutboxCount, state.OutboxStatus != nil, state.Deliveries, state.ProviderCalls, state.CalendarCalls, len(state.SourceRefs))
	}

	armFixtureFault(t, clientA, host.APIURL(), fixture.Accounts[0], "run_timeout")
	activeBackground := createFixtureRun(t, clientA, host.APIURL(), fixture, 0, "background")
	armFixtureFault(t, clientB, host.APIURL(), fixture.Accounts[1], "run_timeout")
	activeForeground := createFixtureRun(t, clientB, host.APIURL(), fixture, 1, "foreground")
	if activeBackground.Budget.MaxDurationMs != 1500 || activeForeground.Budget.MaxDurationMs != 1500 {
		t.Fatalf("run_timeout max durations background=%d foreground=%d", activeBackground.Budget.MaxDurationMs, activeForeground.Budget.MaxDurationMs)
	}
	assertPersistedBudget(t, host, uuid.MustParse(string(activeBackground.RunID)), 1500)
	assertPersistedBudget(t, host, uuid.MustParse(string(activeForeground.RunID)), 1500)

	streamDone := make(chan error, 1)
	go func() {
		tools, toolsErr := integrationTools()
		if toolsErr != nil {
			streamDone <- toolsErr
			return
		}
		text := "Review my calendar"
		requestBody, _ := json.Marshal(agentprotocol.ModelTurnRequest{
			ProtocolVersion: "2.0", RunID: string(activeForeground.RunID), TurnID: "active-close-turn",
			ModelProfile: fixture.Profile,
			Messages:     []agentprotocol.Message{{Role: agentprotocol.MessageRoleUser, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &text}}}},
			Tools:        tools,
		})
		response := fixtureRequest(t, clientB, http.MethodPost,
			host.APIURL()+"/api/v1/agent/runs/"+string(activeForeground.RunID)+"/turns/active-close-turn/stream",
			requestBody, fixture.Accounts[1])
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		streamDone <- nil
	}()
	waitForProviderOperations(t, host, []uuid.UUID{
		uuid.MustParse(string(activeBackground.RunID)), uuid.MustParse(string(activeForeground.RunID)),
	})
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = host.Close(cleanupCtx)
	cleanupCancel()
	if err != nil {
		t.Fatalf("Host.Close(%s): %v", fixtureName, err)
	}
	closed = true
	t.Logf("guarded-deleted owned fixture %s", fixtureName)
	select {
	case streamErr := <-streamDone:
		if streamErr != nil {
			t.Fatal(streamErr)
		}
	case <-time.After(time.Second):
		t.Fatal("active HTTP Provider request did not exit after Host.Close")
	}
	if host.database.API != nil || host.database.Worker != nil || host.database.Migrator != nil {
		t.Fatal("Host.Close left database pools open")
	}
	if _, err = http.Get(host.APIURL() + "/health/live"); err == nil {
		t.Fatal("Host.Close left HTTP listener reachable")
	}
}

func TestAgentIntegrationHostLiveToolTimeoutIsolation(t *testing.T) {
	if os.Getenv("DAYORDER_AGENT_INTEGRATION_LIVE") != "1" {
		t.Skip("set DAYORDER_AGENT_INTEGRATION_LIVE=1 through the owned database launcher")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	host, err := Start(ctx, Config{
		Environment: config.Test, AllowedOrigins: []string{"http://127.0.0.1:5173"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixtureName := host.fixture.DatabaseName()
	t.Logf("created owned fixture %s", fixtureName)
	closed := false
	defer func() {
		if closed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if closeErr := host.Close(cleanupCtx); closeErr != nil {
			t.Errorf("cleanup incomplete for %s: %v", fixtureName, closeErr)
			return
		}
		t.Logf("guarded-deleted owned fixture %s", fixtureName)
	}()

	fixture := fetchFixtureConfig(t, host.APIURL())
	clientA := authenticatedFixtureClient(t, host.APIURL(), fixture.Accounts[0])
	clientB := authenticatedFixtureClient(t, host.APIURL(), fixture.Accounts[1])
	armFixtureFault(t, clientA, host.APIURL(), fixture.Accounts[0], "tool_timeout")
	timedRun := createFixtureRun(t, clientA, host.APIURL(), fixture, 0, "background")
	timedRunID := uuid.MustParse(string(timedRun.RunID))
	waitForOperationState(t, host, timedRunID, "calendar_read", "running", 5*time.Second)

	foreground := createFixtureRun(t, clientB, host.APIURL(), fixture, 1, "foreground")
	calendarBody, _ := json.Marshal(agentprotocol.CalendarReadRequest{
		CallID: "cross-account-isolation",
		Input: agentprotocol.CalendarReadInput{
			Start: agentprotocol.DateTime(fixture.Window.Start), End: agentprotocol.DateTime(fixture.Window.End), Limit: 20,
		},
	})
	started := time.Now()
	calendarResponse := fixtureRequest(t, clientB, http.MethodPost,
		host.APIURL()+"/api/v1/agent/runs/"+string(foreground.RunID)+"/tools/calendar-read",
		calendarBody, fixture.Accounts[1])
	elapsed := time.Since(started)
	defer calendarResponse.Body.Close()
	if calendarResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(calendarResponse.Body)
		t.Fatalf("other account calendar status=%d body_bytes=%d elapsed=%s", calendarResponse.StatusCode, len(body), elapsed)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("other account calendar was delayed by target timeout: %s", elapsed)
	}
	var toolResult agentprotocol.ToolResult
	if err = json.NewDecoder(calendarResponse.Body).Decode(&toolResult); err != nil {
		t.Fatal(err)
	}
	calendarJSON, _ := json.Marshal(toolResult.Data)
	var calendarData agentprotocol.CalendarReadData
	if err = json.Unmarshal(calendarJSON, &calendarData); err != nil {
		t.Fatal(err)
	}
	if len(calendarData.Events) != 1 || calendarData.Events[0].Title != "Private planning B" {
		t.Fatalf("other account calendar metadata: event_count=%d expected_title=%t", len(calendarData.Events), len(calendarData.Events) == 1 && calendarData.Events[0].Title == "Private planning B")
	}

	waitForOperationState(t, host, timedRunID, "calendar_read", "failed", 12*time.Second)
	var errorCode string
	if err = host.database.Migrator.QueryRow(context.Background(), `
SELECT error_code FROM dayorder.agent_run_operations
WHERE run_id = $1 AND kind = 'calendar_read'
`, timedRunID).Scan(&errorCode); err != nil || errorCode != string(agentprotocol.ErrorCodeTimeout) {
		t.Fatalf("target calendar timeout code=%q err=%v", errorCode, err)
	}
	completed := waitForFixtureRun(t, clientA, host.APIURL(), fixture.Accounts[0], timedRunID, 5*time.Second)
	if completed.Status != agentprotocol.ReadonlyRunViewStatusCompleted || completed.Summary == nil ||
		!strings.Contains(*completed.Summary, "未能完成查询") {
		t.Fatalf("target recovery metadata: status=%s summary_present=%t expected_summary=%t", completed.Status, completed.Summary != nil, completed.Summary != nil && strings.Contains(*completed.Summary, "1 event"))
	}
	diagnosticResponse := fixtureRequest(t, clientA, http.MethodGet, host.APIURL()+"/__test/runs/"+timedRunID.String(), nil, fixture.Accounts[0])
	defer diagnosticResponse.Body.Close()
	var state testRunState
	if diagnosticResponse.StatusCode != http.StatusOK || json.NewDecoder(diagnosticResponse.Body).Decode(&state) != nil {
		body, _ := io.ReadAll(diagnosticResponse.Body)
		t.Fatalf("target diagnostic status=%d body_bytes=%d", diagnosticResponse.StatusCode, len(body))
	}
	if state.Status != "completed" || state.OutboxStatus == nil || *state.OutboxStatus != "processed" {
		t.Fatalf("target recovery diagnostic metadata: status=%s outbox_status_present=%t", state.Status, state.OutboxStatus != nil)
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = host.Close(cleanupCtx)
	cleanupCancel()
	if err != nil {
		t.Fatalf("Host.Close(%s): %v", fixtureName, err)
	}
	closed = true
	t.Logf("guarded-deleted owned fixture %s", fixtureName)
}

func authenticatedFixtureClient(t *testing.T, apiURL string, account fixtureAccount) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	body, _ := json.Marshal(map[string]string{"email": account.Email, "password": account.Password})
	response := fixtureRequest(t, client, http.MethodPost, apiURL+"/api/v1/auth/login", body, account)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("login status=%d body_bytes=%d", response.StatusCode, len(raw))
	}
	return client
}

func fetchFixtureConfig(t *testing.T, apiURL string) fixtureConfig {
	t.Helper()
	response, err := http.Get(apiURL + "/__test/config")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("fixture config status=%d body_bytes=%d", response.StatusCode, len(body))
	}
	var fixture fixtureConfig
	if err = json.NewDecoder(response.Body).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func createFixtureRun(t *testing.T, client *http.Client, apiURL string, fixture fixtureConfig, accountIndex int, mode agentprotocol.ExecutionMode) agentprotocol.ReadonlyRunView {
	t.Helper()
	input := agentprotocol.ReadonlyRunStart{
		ExecutionMode: mode, Intent: "Review my calendar", ModelProfile: fixture.Profile, Timezone: "UTC",
		Scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &fixture.Window.Start, To: &fixture.Window.End},
	}
	body, _ := json.Marshal(input)
	response := fixtureRequest(t, client, http.MethodPost, apiURL+"/api/v1/agent/runs", body, fixture.Accounts[accountIndex])
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("create %s run status=%d body_bytes=%d", mode, response.StatusCode, len(raw))
	}
	var view agentprotocol.ReadonlyRunView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func armFixtureFault(t *testing.T, client *http.Client, apiURL string, account fixtureAccount, fault string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"fault": fault})
	response := fixtureRequest(t, client, http.MethodPost, apiURL+"/__test/fault", body, account)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("arm fault %s status=%d body_bytes=%d", fault, response.StatusCode, len(raw))
	}
}

func fixtureRequest(t *testing.T, client *http.Client, method, target string, body []byte, account fixtureAccount) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://127.0.0.1:5173")
		request.Header.Set("Idempotency-Key", uuid.NewString())
	}
	request.Header.Set("X-Device-ID", account.DeviceID.String())
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertPersistedBudget(t *testing.T, host *Host, runID uuid.UUID, want int) {
	t.Helper()
	var got int
	if err := host.database.Migrator.QueryRow(context.Background(), `
SELECT (budget ->> 'maxDurationMs')::integer FROM dayorder.agent_run_executions WHERE run_id = $1
`, runID).Scan(&got); err != nil || got != want {
		t.Fatalf("persisted budget run=%s got=%d want=%d err=%v", runID, got, want, err)
	}
}

func waitForProviderOperations(t *testing.T, host *Host, runIDs []uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := host.database.Migrator.QueryRow(context.Background(), `
SELECT count(*) FROM dayorder.agent_run_operations WHERE kind = 'provider_turn' AND run_id = ANY($1)
`, runIDs).Scan(&count); err == nil && count == len(runIDs) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("active Provider operations were not registered")
}

func waitForFixtureRun(t *testing.T, client *http.Client, apiURL string, account fixtureAccount, runID uuid.UUID, timeout time.Duration) agentprotocol.ReadonlyRunView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var view agentprotocol.ReadonlyRunView
	for time.Now().Before(deadline) {
		response := fixtureRequest(t, client, http.MethodGet, apiURL+"/api/v1/agent/runs/"+runID.String(), nil, account)
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("poll run status=%d body_bytes=%d", response.StatusCode, len(body))
		}
		if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if terminalRunStatus(string(view.Status)) {
			return view
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach terminal state", runID)
	return view
}

func waitForOperationState(t *testing.T, host *Host, runID uuid.UUID, kind, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var state string
		err := host.database.Migrator.QueryRow(context.Background(), `
SELECT state FROM dayorder.agent_run_operations WHERE run_id = $1 AND kind = $2
`, runID, kind).Scan(&state)
		if err == nil && state == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("operation run=%s kind=%s did not reach %s", runID, kind, want)
}
