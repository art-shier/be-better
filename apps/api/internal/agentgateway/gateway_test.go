package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestNewRejectsInvalidGatewayConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing runs", mutate: func(config *Config) { config.Runs = nil }},
		{name: "missing profiles", mutate: func(config *Config) { config.Profiles = nil }},
		{name: "URL-like profile", mutate: func(config *Config) { config.Profiles[0].ID = "https://provider.invalid/model" }},
		{name: "missing adapter", mutate: func(config *Config) { config.Profiles[0].Adapter = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validUnitConfig(t)
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New accepted invalid configuration")
			}
		})
	}
	if _, err := New(validUnitConfig(t)); err != nil {
		t.Fatalf("New rejected valid configuration: %v", err)
	}
}

func TestAllowRetryRequiresNoPublicationAttemptRoomAndEnoughTime(t *testing.T) {
	failure := &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeProviderRateLimited, Retryable: true, RetryAfter: time.Second}
	if !allowRetry(failure, false, 1, 2*time.Second) {
		t.Fatal("eligible first attempt was not retryable")
	}
	for _, test := range []struct {
		name      string
		failure   *agentprovider.ProviderError
		published bool
		attempt   int
		remaining time.Duration
	}{
		{name: "nil error", attempt: 1, remaining: 2 * time.Second},
		{name: "not retryable", failure: &agentprovider.ProviderError{}, attempt: 1, remaining: 2 * time.Second},
		{name: "published", failure: failure, published: true, attempt: 1, remaining: 2 * time.Second},
		{name: "attempt limit", failure: failure, attempt: 2, remaining: 2 * time.Second},
		{name: "exact retry-after boundary", failure: failure, attempt: 1, remaining: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if allowRetry(test.failure, test.published, test.attempt, test.remaining) {
				t.Fatal("ineligible failure was retryable")
			}
		})
	}
}

func TestProviderCancellationIsClassifiedAsInterruptionNotUserCancel(t *testing.T) {
	failure, code, usage := providerFailure(&agentprovider.ProviderError{Code: agentprotocol.ErrorCodeCancelled})
	if failure != nil || code != agentprotocol.ErrorCodeInternalError || usage != (agentprotocol.Usage{}) {
		t.Fatalf("provider cancellation = failure %#v code %s usage %#v", failure, code, usage)
	}
}

func TestProviderKnownUsageOnErrorRemainsIncomplete(t *testing.T) {
	known := agentprotocol.Usage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}
	failure := &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeProtocolIncompatible, KnownUsage: &known}
	if providerUsageComplete(failure) {
		t.Fatal("known partial Usage from a Provider error was marked complete")
	}
	_, _, got := providerFailure(failure)
	if got != known {
		t.Fatalf("known partial Usage = %#v, want %#v", got, known)
	}
}

func TestExplicitGatewayCancellationPreservesTrustedHostCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	turn := &Turn{ctx: ctx, cancelCtx: cancel}
	want := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "user_cancelled", Retryable: false}
	turn.cancelExplicit(&agentexecution.Error{Agent: want})
	if got := turn.failureFor(turn.contextCode()); !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit cancellation = %#v, want %#v", got, want)
	}
}

func TestTerminalCleanupReservesTimeToPersistFailureAfterEndTimeout(t *testing.T) {
	now := time.Now()
	persisted := false
	primary := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider failed", Retryable: false}
	outcome := finalizeTerminal(cleanupTiming{
		endDeadline: now.Add(30 * time.Millisecond), authorizeDeadline: now.Add(60 * time.Millisecond), overallDeadline: now.Add(200 * time.Millisecond),
	}, func() agentprotocol.AgentError { return primary }, terminalCallbacks{
		end: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		fail: func(ctx context.Context, failure agentprotocol.AgentError) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			persisted = true
			if failure.Code != primary.Code || failure.Details["accounting"] != "incomplete" {
				t.Fatalf("persisted failure = %#v", failure)
			}
			return nil
		},
	})
	if !persisted || !outcome.persisted || outcome.success || outcome.failure.Code != primary.Code {
		t.Fatalf("cleanup outcome = %#v persisted=%t", outcome, persisted)
	}
}

func TestTerminalCleanupReservesTimeToPersistFailureAfterAuthorizationTimeout(t *testing.T) {
	now := time.Now()
	persisted := false
	primary := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "model turn timed out", Retryable: false}
	outcome := finalizeTerminal(cleanupTiming{
		endDeadline: now.Add(30 * time.Millisecond), authorizeDeadline: now.Add(60 * time.Millisecond), overallDeadline: now.Add(200 * time.Millisecond),
	}, func() agentprotocol.AgentError { return primary }, terminalCallbacks{
		end: func(context.Context) error { return nil },
		authorize: func(ctx context.Context) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		},
		fail: func(ctx context.Context, failure agentprotocol.AgentError) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			persisted = true
			if failure.Code != primary.Code || failure.Details["authorization"] != "failed" {
				t.Fatalf("persisted failure = %#v", failure)
			}
			return nil
		},
	})
	if !persisted || !outcome.persisted || outcome.success || outcome.failure.Code != primary.Code {
		t.Fatalf("cleanup outcome = %#v persisted=%t", outcome, persisted)
	}
}

func TestTerminalCleanupPreservesControlledFailureWhenCleanupCannotPersist(t *testing.T) {
	for _, primary := range []agentprotocol.AgentError{
		{Code: agentprotocol.ErrorCodeCancelled, Message: "user_cancelled", Retryable: false},
		{Code: agentprotocol.ErrorCodeTimeout, Message: "model turn timed out", Retryable: false},
	} {
		t.Run(string(primary.Code), func(t *testing.T) {
			now := time.Now()
			outcome := finalizeTerminal(cleanupTiming{
				endDeadline: now.Add(50 * time.Millisecond), authorizeDeadline: now.Add(100 * time.Millisecond), overallDeadline: now.Add(150 * time.Millisecond),
			}, func() agentprotocol.AgentError { return primary }, terminalCallbacks{
				end:  func(context.Context) error { return errors.New("secret accounting failure") },
				fail: func(context.Context, agentprotocol.AgentError) error { return errors.New("secret persistence failure") },
			})
			if outcome.success || outcome.persisted || outcome.failure.Code != primary.Code || outcome.failure.Message != primary.Message || outcome.failure.Details["persistence"] != "failed" {
				t.Fatalf("cleanup outcome = %#v", outcome)
			}
			raw, err := json.Marshal(outcome.failure)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "secret") {
				t.Fatalf("cleanup detail leaked raw error: %s", raw)
			}
		})
	}
}

func TestTerminalCleanupPersistsRealRunFailureAfterAuthorizationTimeout(t *testing.T) {
	fixture := agenttest.Open(t)
	services := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
	created, err := services.Runs.Create(ctx, service.MutationContext{
		UserID: fixture.UserA, DeviceID: fixture.DeviceA, MutationID: uuid.New(), RequestID: uuid.New(),
	}, agentprotocol.ReadonlyRunStart{
		ExecutionMode: agentprotocol.ExecutionModeForeground,
		Intent:        "authorization timeout cleanup",
		ModelProfile:  "readonly-default",
		Timezone:      "UTC",
		Scope:         agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
	})
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(created.RunID))
	actor := agentexecution.Actor{UserID: fixture.UserA, Mode: agentprotocol.ExecutionModeForeground}
	handle, err := services.Runs.BeginOperation(ctx, actor, runID, "provider_turn", uuid.NewString(), []byte(`{"turn":"authorization-timeout"}`), 128)
	if err != nil {
		t.Fatal(err)
	}
	usage := agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
	now := time.Now()
	primary := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "model turn timed out", Retryable: false}
	outcome := finalizeTerminal(cleanupTiming{
		endDeadline: now.Add(time.Second), authorizeDeadline: now.Add(150 * time.Millisecond), overallDeadline: now.Add(2 * time.Second),
	}, func() agentprotocol.AgentError { return primary }, terminalCallbacks{
		end: func(cleanupCtx context.Context) error {
			return services.Runs.EndOperation(cleanupCtx, actor, handle, usage, true, "")
		},
		authorize: func(cleanupCtx context.Context) (bool, error) {
			<-cleanupCtx.Done()
			return false, cleanupCtx.Err()
		},
		fail: func(cleanupCtx context.Context, failure agentprotocol.AgentError) error {
			return services.Runs.Fail(cleanupCtx, actor, runID, failure)
		},
	})
	if outcome.success || !outcome.persisted || outcome.failure.Code != primary.Code || outcome.failure.Details["authorization"] != "failed" {
		t.Fatalf("cleanup outcome = %#v", outcome)
	}
	view, err := services.Runs.Get(ctx, fixture.UserA, runID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != agentprotocol.ReadonlyRunViewStatusFailed || view.Error == nil || view.Error.Code != primary.Code || view.Usage != usage || !view.UsageComplete {
		t.Fatalf("persisted Run = %#v", view)
	}
}

func TestTerminalCleanupRefreshesStopAttributionWhileEndIsBlocked(t *testing.T) {
	fixture := agenttest.Open(t)
	services := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for _, test := range []struct {
		name       string
		userID     uuid.UUID
		deviceID   uuid.UUID
		explicit   bool
		wantCode   agentprotocol.ErrorCode
		wantStatus agentprotocol.ReadonlyRunViewStatus
	}{
		{name: "explicit cancel", userID: fixture.UserA, deviceID: fixture.DeviceA, explicit: true, wantCode: agentprotocol.ErrorCodeCancelled, wantStatus: agentprotocol.ReadonlyRunViewStatusStopped},
		{name: "real deadline", userID: fixture.UserB, deviceID: fixture.DeviceB, wantCode: agentprotocol.ErrorCodeTimeout, wantStatus: agentprotocol.ReadonlyRunViewStatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			runID := createTerminalCleanupRun(t, ctx, services.Runs, test.userID, test.deviceID, test.name)
			actor := agentexecution.Actor{UserID: test.userID, Mode: agentprotocol.ExecutionModeForeground}
			handle, err := services.Runs.BeginOperation(ctx, actor, runID, "provider_turn", uuid.NewString(), []byte(`{"turn":"blocked-end"}`), 128)
			if err != nil {
				t.Fatal(err)
			}
			causeCtx, cancelCause := context.WithCancelCause(context.Background())
			deadline := time.Now().Add(10 * time.Second)
			if !test.explicit {
				deadline = time.Now().Add(500 * time.Millisecond)
			}
			turnCtx, cancelDeadline := context.WithDeadlineCause(causeCtx, deadline, context.DeadlineExceeded)
			turn := &Turn{
				gateway: &Gateway{runs: services.Runs, registry: newTurnRegistry()}, actor: actor, runID: runID,
				ctx: turnCtx, cancelCtx: cancelCause, cancelDeadline: cancelDeadline, deadline: deadline,
				done: make(chan struct{}), consumerDone: make(chan struct{}), terminalReady: make(chan terminalRequest, 1), opGate: make(chan struct{}, 1),
				handle: handle, consumed: true,
			}
			turn.opGate <- struct{}{}
			go turn.watchCancellation()

			lock, err := fixture.Migrator.Begin(ctx)
			if err != nil {
				t.Fatal("begin operation lock")
			}
			rollback := true
			defer func() {
				if rollback {
					_ = lock.Rollback(context.Background())
				}
			}()
			if _, err = lock.Exec(ctx, `SELECT state FROM dayorder.agent_run_operations WHERE run_id=$1 AND kind='provider_turn' FOR UPDATE`, runID); err != nil {
				t.Fatal("lock provider operation")
			}
			finished := make(chan struct{})
			go func() {
				turn.finishFailure(agentprotocol.ErrorCodeProviderUnavailable, agentprotocol.Usage{}, false)
				close(finished)
			}()
			waitForOperationOwnership(t, turn)
			if test.explicit {
				turn.cancelExplicit(context.Canceled)
			} else {
				<-turn.ctx.Done()
			}
			<-finished
			var failure *agentexecution.Error
			if !errors.As(turn.finalErr, &failure) || failure.Agent.Code != test.wantCode || failure.Agent.Details["accounting"] != "incomplete" {
				t.Fatalf("terminal failure = %#v", turn.finalErr)
			}
			view, err := services.Runs.Get(ctx, test.userID, runID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Status != test.wantStatus || view.Error == nil || view.Error.Code != test.wantCode {
				t.Fatalf("persisted Run = %#v", view)
			}
			if err = lock.Commit(ctx); err != nil {
				t.Fatal("release operation lock")
			}
			rollback = false
		})
	}
}

func createTerminalCleanupRun(t testing.TB, ctx context.Context, runs *service.AgentReadonlyService, userID, deviceID uuid.UUID, intent string) uuid.UUID {
	t.Helper()
	from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
	created, err := runs.Create(ctx, service.MutationContext{
		UserID: userID, DeviceID: deviceID, MutationID: uuid.New(), RequestID: uuid.New(),
	}, agentprotocol.ReadonlyRunStart{
		ExecutionMode: agentprotocol.ExecutionModeForeground,
		Intent:        intent,
		ModelProfile:  "readonly-default",
		Timezone:      "UTC",
		Scope:         agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
	})
	if err != nil {
		t.Fatal(err)
	}
	return uuid.MustParse(string(created.RunID))
}

func waitForOperationOwnership(t testing.TB, turn *Turn) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case token := <-turn.opGate:
			turn.opGate <- token
		case <-deadline.C:
			t.Fatal("terminal cleanup did not acquire operation ownership")
		default:
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// The real service is required by Config. This deliberately-invalid zero value
// is sufficient for constructor validation that must not call it.
func newTestRuns() *service.AgentReadonlyService { return &service.AgentReadonlyService{} }

func validUnitConfig(t testing.TB) Config {
	t.Helper()
	meta := agentskill.MetaBindings(nil, agentprotocol.CapabilitySnapshot{}, nil, agenttool.Policy{})
	calendar, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Runs: newTestRuns(), Profiles: []Profile{{ID: "readonly-default", Model: "model", Adapter: testAdapter{}}},
		Tools: []agentprotocol.ToolSpec{meta[0].Spec(), meta[1].Spec(), calendar},
	}
}

type testAdapter struct{}

func (testAdapter) Stream(context.Context, agentprotocol.ModelTurnRequest, agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(func(agentprotocol.ProviderEvent, error) bool) {}
}
