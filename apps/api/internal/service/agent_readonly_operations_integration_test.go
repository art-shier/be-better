package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestReadonlyOperationDuplicateProviderTurnConflicts(t *testing.T) {
	fixture := agenttest.Open(t)
	runs := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI).Runs
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(created.RunID))
	actor := agentexecution.Actor{UserID: fixture.UserA, Mode: agentprotocol.ExecutionModeForeground}
	if _, err = runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-1", []byte(`{"text":"first"}`), 2048); err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte(`{"text":"a"}`), []byte(`{"text":"b"}`)} {
		_, err = runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-1", payload, 2048)
		if !errors.Is(err, model.ErrConflict) {
			t.Fatalf("duplicate error = %v", err)
		}
	}
	assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, 1, fixture.UserA, runID)
}

func TestReadonlyOperationAccountingTransactions(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Run("new runs have complete zero accounting", func(t *testing.T) {
		runID, _ := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{}, 0, true)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("calendar payload is canonicalized and same content starts a new attempt", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		first, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar-1", []byte("{\"b\":2,\"a\":1.0}"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, first, agentprotocol.Usage{}, true, ""); err != nil {
			t.Fatal(err)
		}
		second, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar-1", []byte("{\n\t\"a\":1,\"b\":2}"), 0)
		if err != nil {
			t.Fatalf("canonical replay BeginOperation() error = %v", err)
		}
		if second.Attempts != 2 || !second.StartedAt.Equal(first.StartedAt) {
			t.Fatalf("second attempt = %#v, first started at %v", second, first.StartedAt)
		}
		if err = api.Runs.EndOperation(ctx, actor, second, agentprotocol.Usage{}, true, ""); err != nil {
			t.Fatal(err)
		}
		if _, err = api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar-1", []byte("{\"a\":2,\"b\":2}"), 0); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("different-payload BeginOperation() error = %v, want conflict", err)
		}
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("token reservation is atomic and budget bounded", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		_, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "over-budget", []byte("{}"), 16001)
		assertReadonlyProtocolError(t, err, "run token budget exceeded")
		operation, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-budget", []byte("{}"), 8001)
		if err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{}, false, "provider_unavailable"); err != nil {
			t.Fatal(err)
		}
		_, err = api.Runs.RetryOperation(ctx, actor, operation, 8001)
		assertReadonlyProtocolError(t, err, "run token budget exceeded")
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{}, 8001, false)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("unknown provider attempt retains reservation across one fixed retry", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		first, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-retry", []byte("{}"), 100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = api.Runs.RetryOperation(ctx, actor, first, 100); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("running RetryOperation() error = %v, want conflict", err)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{}, 100, false)
		if err = api.Runs.EndOperation(ctx, actor, first, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, false, "provider_unavailable"); err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, first, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, false, "provider_unavailable"); err != nil {
			t.Fatalf("duplicate incomplete EndOperation() error = %v", err)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, 100, false)
		if _, err = api.Runs.RetryOperation(ctx, actor, first, 99); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("changed-reserve RetryOperation() error = %v, want conflict", err)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, 100, false)

		second, err := api.Runs.RetryOperation(ctx, actor, first, 100)
		if err != nil {
			t.Fatal(err)
		}
		if second.Attempts != 2 || second.ReservedTokens != 200 {
			t.Fatalf("retry operation = %#v", second)
		}
		if err = api.Runs.EndOperation(ctx, actor, first, agentprotocol.Usage{OutputTokens: 99, TotalTokens: 99}, true, ""); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("stale EndOperation() error = %v, want conflict", err)
		}
		usage := agentprotocol.Usage{OutputTokens: 20, TotalTokens: 20}
		if err = api.Runs.EndOperation(ctx, actor, second, usage, true, ""); err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, second, usage, true, ""); err != nil {
			t.Fatalf("duplicate EndOperation() error = %v", err)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}, 100, false)
		if _, err = api.Runs.RetryOperation(ctx, actor, second, 100); err == nil {
			t.Fatal("third provider attempt was accepted")
		} else {
			assertReadonlyProtocolError(t, err, "readonly operation retry limit exceeded")
		}
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("retry rejects another running provider without ledger mutation", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		first, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-a", []byte("{}"), 100)
		if err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, first, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, false, "provider_unavailable"); err != nil {
			t.Fatal(err)
		}
		second, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-b", []byte("{}"), 100)
		if err != nil {
			t.Fatal(err)
		}
		beforeRetry, err := api.Runs.Get(ctx, fixture.UserA, runID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = api.Runs.RetryOperation(ctx, actor, first, 100); !errors.Is(err, model.ErrConflict) {
			t.Errorf("RetryOperation() beside running provider error = %v, want conflict", err)
		}
		afterRetry, err := api.Runs.Get(ctx, fixture.UserA, runID)
		if err != nil {
			t.Fatal(err)
		}
		if afterRetry.Version != beforeRetry.Version {
			t.Fatalf("rejected RetryOperation() version = %d, want unchanged %d", afterRetry.Version, beforeRetry.Version)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, 200, false)
		assertOperationLedger(t, fixture, runID, first.ID, "unknown", 1, 100, false)
		assertOperationLedger(t, fixture, runID, second.ID, "running", 1, 100, false)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("zero reservation retry preserves earlier usage uncertainty", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserB, fixture.DeviceB)
		first, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "zero-reserve", []byte("{}"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, first, agentprotocol.Usage{InputTokens: 10, TotalTokens: 10}, false, "provider_unavailable"); err != nil {
			t.Fatal(err)
		}
		second, err := api.Runs.RetryOperation(ctx, actor, first, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, second, agentprotocol.Usage{OutputTokens: 20, TotalTokens: 20}, true, ""); err != nil {
			t.Fatal(err)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}, 0, false)
		assertOperationLedger(t, fixture, runID, first.ID, "completed", 2, 0, false)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserB, fixture.DeviceB)
	})
}

func TestReadonlyOperationLimitsAndAuthorityTransactions(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	worker := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Run("nine provider turns succeed and the tenth is rejected", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		for index := 1; index <= 9; index++ {
			operation, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", fmt.Sprintf("turn-%d", index), []byte(fmt.Sprintf("{\"turn\":%d}", index)), 1)
			if err != nil {
				t.Fatalf("provider turn %d error = %v", index, err)
			}
			if index == 1 {
				if _, err = api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "overlap", []byte("{}"), 1); !errors.Is(err, model.ErrConflict) {
					t.Fatalf("overlapping provider turn error = %v, want conflict", err)
				}
			}
			if err = api.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{InputTokens: 1, TotalTokens: 1}, true, ""); err != nil {
				t.Fatal(err)
			}
		}
		_, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn-10", []byte("{\"turn\":10}"), 1)
		assertReadonlyProtocolError(t, err, "readonly provider turn limit exceeded")
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{InputTokens: 9, TotalTokens: 9}, 0, true)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("eight calendar IDs and thirty-two attempts succeed at their boundary", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		var repeated agentexecution.Operation
		for index := 0; index < 8; index++ {
			operation, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", fmt.Sprintf("calendar-%d", index), []byte(fmt.Sprintf("{\"calendar\":%d}", index)), 0)
			if err != nil {
				t.Fatal(err)
			}
			if err = api.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{}, true, ""); err != nil {
				t.Fatal(err)
			}
			if index == 0 {
				repeated = operation
			}
		}
		if _, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar-8", []byte("{}"), 0); err == nil {
			t.Fatal("ninth distinct calendar read was accepted")
		} else {
			assertReadonlyProtocolError(t, err, "readonly calendar read limit exceeded")
		}
		for totalAttempts := 9; totalAttempts <= 32; totalAttempts++ {
			var err error
			repeated, err = api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar-0", []byte("{\"calendar\":0}"), 0)
			if err != nil {
				t.Fatalf("calendar total attempt %d error = %v", totalAttempts, err)
			}
			if err = api.Runs.EndOperation(ctx, actor, repeated, agentprotocol.Usage{}, true, ""); err != nil {
				t.Fatal(err)
			}
		}
		_, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar-0", []byte("{\"calendar\":0}"), 0)
		assertReadonlyProtocolError(t, err, "readonly calendar attempt limit exceeded")
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, 8, fixture.UserA, runID)
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{}, 0, true)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("calendar total attempt cap also rejects a new ID", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		for attempt := 1; attempt <= 32; attempt++ {
			operation, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "repeated-calendar", []byte("{}"), 0)
			if err != nil {
				t.Fatalf("calendar attempt %d error = %v", attempt, err)
			}
			if err = api.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{}, true, ""); err != nil {
				t.Fatal(err)
			}
		}
		_, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "new-calendar", []byte("{}"), 0)
		assertReadonlyProtocolError(t, err, "readonly calendar attempt limit exceeded")
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("calendar model accounting inputs are rejected without mutation", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA)
		if _, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "reserved-calendar", []byte("{}"), 1); err == nil {
			t.Fatal("calendar token reservation was accepted")
		} else {
			assertReadonlyProtocolError(t, err, "calendar reads cannot reserve model tokens")
		}
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, 0, fixture.UserA, runID)
		operation, err := api.Runs.BeginOperation(ctx, actor, runID, "calendar_read", "calendar", []byte("{}"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{InputTokens: 1, TotalTokens: 1}, true, ""); err == nil {
			t.Fatal("calendar model usage was accepted")
		} else {
			assertReadonlyProtocolError(t, err, "calendar reads cannot report model token usage")
		}
		if err = api.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{}, false, "tool_failed"); err != nil {
			t.Fatal(err)
		}
		assertExecutionAccounting(t, fixture, runID, agentprotocol.Usage{}, 0, true)
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
	})

	t.Run("concurrent provider begins grant one execution authority", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserB, fixture.DeviceB)
		start := make(chan struct{})
		results := make(chan operationResult, 2)
		for index := 0; index < 2; index++ {
			go func(index int) {
				<-start
				operation, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", fmt.Sprintf("concurrent-%d", index), []byte(fmt.Sprintf("{\"request\":%d}", index)), 10)
				results <- operationResult{operation: operation, err: err}
			}(index)
		}
		close(start)
		successes, conflicts := 0, 0
		first, second := <-results, <-results
		for _, result := range []operationResult{first, second} {
			if result.err == nil {
				successes++
				if err := api.Runs.EndOperation(ctx, actor, result.operation, agentprotocol.Usage{}, true, ""); err != nil {
					t.Fatal(err)
				}
			} else if errors.Is(result.err, model.ErrConflict) {
				conflicts++
			} else {
				t.Fatalf("concurrent BeginOperation() error = %v", result.err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("concurrent results successes=%d conflicts=%d", successes, conflicts)
		}
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserB, fixture.DeviceB)
	})

	t.Run("background operations require the exact claimed token", func(t *testing.T) {
		created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserB, fixture.DeviceB), validReadonlyStart(agentprotocol.ExecutionModeBackground))
		if err != nil {
			t.Fatal(err)
		}
		runID := uuid.MustParse(string(created.RunID))
		unclaimed := agentexecution.Actor{UserID: fixture.UserB, Mode: agentprotocol.ExecutionModeBackground}
		if _, err = worker.Runs.BeginOperation(ctx, unclaimed, runID, "provider_turn", "turn", []byte("{}"), 1); !errors.Is(err, service.ErrValidation) {
			t.Fatalf("unclaimed BeginOperation() error = %v, want validation", err)
		}
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, 0, fixture.UserB, runID)
		token := uuid.New()
		if _, err = worker.Runs.Claim(ctx, fixture.UserB, runID, token, false); err != nil {
			t.Fatal(err)
		}
		actor := agentexecution.Actor{UserID: fixture.UserB, Token: token, Mode: agentprotocol.ExecutionModeBackground}
		if _, err = worker.Runs.Authorize(ctx, actor, runID); err != nil {
			t.Fatal(err)
		}
		operation, err := worker.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "turn", []byte("{}"), 1)
		if err != nil {
			t.Fatal(err)
		}
		stale := actor
		stale.Token = uuid.New()
		if _, err = worker.Runs.Authorize(ctx, stale, runID); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("stale-token Authorize() error = %v, want conflict", err)
		}
		if err = worker.Runs.EndOperation(ctx, stale, operation, agentprotocol.Usage{}, true, ""); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("stale-token EndOperation() error = %v, want conflict", err)
		}
		if err = worker.Runs.EndOperation(ctx, actor, operation, agentprotocol.Usage{}, true, ""); err != nil {
			t.Fatal(err)
		}
		if err = worker.Runs.Fail(ctx, actor, runID, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "cancelled"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cancelled run rejects new work but permits authenticated settlement", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserB, fixture.DeviceB)
		operation, err := api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "cancelled-turn", []byte("{}"), 50)
		if err != nil {
			t.Fatal(err)
		}
		cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserB, fixture.DeviceB)
		if _, err = api.Runs.RetryOperation(ctx, actor, operation, 50); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("RetryOperation() after cancel error = %v, want conflict", err)
		}
		if _, err = api.Runs.BeginOperation(ctx, actor, runID, "provider_turn", "late-turn", []byte("{}"), 1); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("BeginOperation() after cancel error = %v, want conflict", err)
		}
		usage := agentprotocol.Usage{InputTokens: 4, OutputTokens: 6, TotalTokens: 10}
		if err = api.Runs.EndOperation(ctx, actor, operation, usage, true, "cancelled"); err != nil {
			t.Fatalf("EndOperation() after cancel error = %v", err)
		}
		view, err := api.Runs.Get(ctx, fixture.UserB, runID)
		if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusStopped || view.Usage != usage {
			t.Fatalf("cancelled settled view = %#v, error=%v", view, err)
		}
	})

	t.Run("authorization converges an expired run", func(t *testing.T) {
		runID, actor := createForegroundOperationRun(t, ctx, fixture, api.Runs, fixture.UserB, fixture.DeviceB)
		expireRun(t, fixture, runID)
		if _, err := api.Runs.Authorize(ctx, actor, runID); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("Authorize() expired error = %v, want conflict", err)
		}
		view, err := api.Runs.Get(ctx, fixture.UserB, runID)
		if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusFailed || view.Error == nil || view.Error.Code != agentprotocol.ErrorCodeTimeout {
			t.Fatalf("expired authorized view = %#v, error=%v", view, err)
		}
	})
}

type operationResult struct {
	operation agentexecution.Operation
	err       error
}

func createForegroundOperationRun(t testing.TB, ctx context.Context, fixture *agenttest.Database, runs *service.AgentReadonlyService, userID, deviceID uuid.UUID) (uuid.UUID, agentexecution.Actor) {
	t.Helper()
	created, err := runs.Create(ctx, readonlyMutationFor(userID, deviceID), validReadonlyStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	return uuid.MustParse(string(created.RunID)), agentexecution.Actor{UserID: userID, Mode: agentprotocol.ExecutionModeForeground}
}

func cancelOperationRun(t testing.TB, ctx context.Context, fixture *agenttest.Database, runs *service.AgentReadonlyService, runID, userID, deviceID uuid.UUID) {
	t.Helper()
	view, err := runs.Get(ctx, userID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status == agentprotocol.ReadonlyRunViewStatusCompleted || view.Status == agentprotocol.ReadonlyRunViewStatusFailed || view.Status == agentprotocol.ReadonlyRunViewStatusStopped {
		return
	}
	if _, err = runs.Cancel(ctx, readonlyMutationFor(userID, deviceID), runID, int64(view.Version)); err != nil {
		t.Fatal(err)
	}
}

func assertReadonlyProtocolError(t testing.TB, err error, message string) {
	t.Helper()
	var protocolError *agentexecution.Error
	if !errors.As(err, &protocolError) || protocolError.Agent.Code != agentprotocol.ErrorCodeValidationFailed || protocolError.Agent.Message != message || protocolError.Agent.Retryable {
		t.Fatalf("operation error = %v, want validation_failed %q", err, message)
	}
}

func assertExecutionAccounting(t testing.TB, fixture *agenttest.Database, runID uuid.UUID, usage agentprotocol.Usage, reserved int, complete bool) {
	t.Helper()
	var input, output, total, gotReserved int
	var gotComplete bool
	err := fixture.Migrator.QueryRow(context.Background(), "SELECT (known_usage->>'inputTokens')::integer, (known_usage->>'outputTokens')::integer, (known_usage->>'totalTokens')::integer, reserved_tokens, usage_complete FROM dayorder.agent_run_executions WHERE run_id=$1", runID).Scan(&input, &output, &total, &gotReserved, &gotComplete)
	if err != nil {
		t.Fatal("read readonly execution accounting")
	}
	if got := (agentprotocol.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total}); got != usage || gotReserved != reserved || gotComplete != complete {
		t.Fatalf("execution accounting = usage %#v reserved %d complete %t, want usage %#v reserved %d complete %t", got, gotReserved, gotComplete, usage, reserved, complete)
	}
}

func assertOperationLedger(t testing.TB, fixture *agenttest.Database, runID uuid.UUID, operationID, state string, attempts, reserved int, complete bool) {
	t.Helper()
	var gotState string
	var gotAttempts, gotReserved int
	var gotComplete bool
	err := fixture.Migrator.QueryRow(context.Background(), "SELECT state, attempts, reserved_tokens, usage_complete FROM dayorder.agent_run_operations WHERE run_id=$1 AND operation_id=$2", runID, operationID).Scan(&gotState, &gotAttempts, &gotReserved, &gotComplete)
	if err != nil {
		t.Fatal("read readonly operation ledger")
	}
	if gotState != state || gotAttempts != attempts || gotReserved != reserved || gotComplete != complete {
		t.Fatalf("operation ledger = state %q attempts %d reserved %d complete %t, want state %q attempts %d reserved %d complete %t", gotState, gotAttempts, gotReserved, gotComplete, state, attempts, reserved, complete)
	}
}
