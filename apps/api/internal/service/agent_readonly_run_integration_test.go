package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestReadonlyLifecycleTransactions(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	worker := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Run("create replay and trusted completion", func(t *testing.T) {
		mutation := readonlyMutationFor(fixture.UserA, fixture.DeviceA)
		created, err := api.Runs.Create(ctx, mutation, validReadonlyStart(agentprotocol.ExecutionModeBackground))
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := api.Runs.Create(ctx, mutation, validReadonlyStart(agentprotocol.ExecutionModeBackground))
		if err != nil {
			t.Fatal(err)
		}
		if replayed.RunID != created.RunID || replayed.Version != created.Version {
			t.Fatalf("replayed run = %#v, created = %#v", replayed, created)
		}
		runID := uuid.MustParse(string(created.RunID))
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_runs WHERE user_id=$1 AND id=$2`, 1, fixture.UserA, runID)
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.outbox_events WHERE user_id=$1 AND aggregate_id=$2 AND event_type='agent.readonly.run.requested'`, 1, fixture.UserA, runID)
		assertRunTransaction(t, fixture, runID, 1, "agent.readonly.create")

		token := uuid.New()
		claimed, err := worker.Runs.Claim(ctx, fixture.UserA, runID, token, false)
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Execution.Token != token || claimed.Run.Status != "analyzing" || claimed.Run.Version != 2 {
			t.Fatalf("claimed record = %#v", claimed)
		}
		assertRunTransaction(t, fixture, runID, 2, "agent.readonly.claim")
		summary := "calendar is clear"
		state := terminalRuntimeState(claimed, agentprotocol.RuntimePhaseCompleted, &summary, nil)
		actor := agentexecution.Actor{UserID: fixture.UserA, Token: token, Mode: agentprotocol.ExecutionModeBackground}
		if err = worker.Runs.Complete(ctx, actor, runID, state, []model.AgentStepDraft{{Title: "Read calendar", Detail: "Checked the requested window"}}); err != nil {
			t.Fatal(err)
		}
		if err = worker.Runs.Complete(ctx, actor, runID, state, nil); err != nil {
			t.Fatalf("duplicate Complete() error = %v", err)
		}
		if err = worker.Runs.Complete(ctx, agentexecution.Actor{UserID: fixture.UserA, Token: uuid.New(), Mode: agentprotocol.ExecutionModeBackground}, runID, state, nil); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("old-token Complete() error = %v, want conflict", err)
		}
		completed, err := api.Runs.Get(ctx, fixture.UserA, runID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status != agentprotocol.ReadonlyRunViewStatusCompleted || completed.Version != 3 || completed.ResultOrigin != agentprotocol.ReadonlyRunViewResultOriginServerRuntime || completed.Summary == nil || *completed.Summary != summary {
			t.Fatalf("completed view = %#v", completed)
		}
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_steps WHERE user_id=$1 AND run_id=$2`, 1, fixture.UserA, runID)
		assertRunTransaction(t, fixture, runID, 3, "agent.readonly.complete")
	})

	t.Run("account lock permits one concurrent active run", func(t *testing.T) {
		starts := make(chan struct{})
		results := make(chan createResult, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for range 2 {
			go func() {
				ready.Done()
				<-starts
				view, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
				results <- createResult{view: view, err: err}
			}()
		}
		ready.Wait()
		close(starts)
		first, second := <-results, <-results
		successes, conflicts := 0, 0
		var winner agentprotocol.ReadonlyRunView
		for _, result := range []createResult{first, second} {
			switch {
			case result.err == nil:
				successes++
				winner = result.view
			case errors.Is(result.err, model.ErrConflict):
				conflicts++
			default:
				t.Fatalf("concurrent Create() error = %v", result.err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("concurrent results: successes=%d conflicts=%d", successes, conflicts)
		}
		cancelMutation := readonlyMutationFor(fixture.UserA, fixture.DeviceA)
		stopped, err := api.Runs.Cancel(ctx, cancelMutation, uuid.MustParse(string(winner.RunID)), int64(winner.Version))
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := api.Runs.Cancel(ctx, cancelMutation, uuid.MustParse(string(winner.RunID)), int64(winner.Version))
		if err != nil || replayed.RunID != stopped.RunID || replayed.Version != stopped.Version {
			t.Fatalf("replayed Cancel() = %#v, error=%v", replayed, err)
		}
	})

	t.Run("foreground and background terminal paths stay isolated", func(t *testing.T) {
		background, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeBackground))
		if err != nil {
			t.Fatal(err)
		}
		backgroundID := uuid.MustParse(string(background.RunID))
		finish := completedReadonlyFinish("must not be accepted")
		if _, err = api.Runs.Finish(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), backgroundID, int64(background.Version), finish); !errors.Is(err, service.ErrValidation) {
			t.Fatalf("background Finish() error = %v, want ErrValidation", err)
		}
		token := uuid.New()
		if _, err = worker.Runs.Claim(ctx, fixture.UserA, backgroundID, token, false); err != nil {
			t.Fatal(err)
		}
		if err = worker.Runs.Fail(ctx, agentexecution.Actor{UserID: fixture.UserA, Token: token, Mode: agentprotocol.ExecutionModeBackground}, backgroundID, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider unavailable", Retryable: true}); err != nil {
			t.Fatal(err)
		}
		assertRunTransaction(t, fixture, backgroundID, 3, "agent.readonly.fail")

		foreground, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
		if err != nil {
			t.Fatal(err)
		}
		finishMutation := readonlyMutationFor(fixture.UserA, fixture.DeviceA)
		finishInput := completedReadonlyFinish("client summary")
		finishInput.Steps = []agentprotocol.ReadonlyRunFinishStepsElem{{Title: "Summarize", Detail: "Prepared the client-reported result"}}
		finished, err := api.Runs.Finish(ctx, finishMutation, uuid.MustParse(string(foreground.RunID)), int64(foreground.Version), finishInput)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := api.Runs.Finish(ctx, finishMutation, uuid.MustParse(string(foreground.RunID)), int64(foreground.Version), finishInput)
		if err != nil || replayed.RunID != finished.RunID || replayed.Version != finished.Version {
			t.Fatalf("replayed Finish() = %#v, error=%v", replayed, err)
		}
		if finished.Status != agentprotocol.ReadonlyRunViewStatusCompleted || finished.ResultOrigin != agentprotocol.ReadonlyRunViewResultOriginClientReported {
			t.Fatalf("foreground finish = %#v", finished)
		}
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_steps WHERE user_id=$1 AND run_id=$2`, 1, fixture.UserA, uuid.MustParse(string(foreground.RunID)))
		assertRunTransaction(t, fixture, uuid.MustParse(string(foreground.RunID)), 2, "agent.readonly.finish")
	})

	t.Run("cancelled terminal state has precedence over a new finish", func(t *testing.T) {
		created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
		if err != nil {
			t.Fatal(err)
		}
		runID := uuid.MustParse(string(created.RunID))
		stopped, err := api.Runs.Cancel(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), runID, int64(created.Version))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = api.Runs.Finish(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), runID, int64(stopped.Version), completedReadonlyFinish("late result")); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("Finish() after cancel error = %v, want conflict", err)
		}
		got, err := api.Runs.Get(ctx, fixture.UserA, runID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != agentprotocol.ReadonlyRunViewStatusStopped || got.Error == nil || got.Error.Code != agentprotocol.ErrorCodeCancelled {
			t.Fatalf("run after late finish = %#v", got)
		}
		assertRunTransaction(t, fixture, runID, 2, "agent.readonly.cancel")
	})

	t.Run("get and claim converge deadlines transactionally", func(t *testing.T) {
		foreground, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
		if err != nil {
			t.Fatal(err)
		}
		foregroundID := uuid.MustParse(string(foreground.RunID))
		expireRun(t, fixture, foregroundID)
		timedOut, err := api.Runs.Get(ctx, fixture.UserA, foregroundID)
		if err != nil {
			t.Fatal(err)
		}
		if timedOut.Status != agentprotocol.ReadonlyRunViewStatusFailed || timedOut.Error == nil || timedOut.Error.Code != agentprotocol.ErrorCodeTimeout || timedOut.Version != 2 {
			t.Fatalf("Get() timeout = %#v", timedOut)
		}
		assertRunTransaction(t, fixture, foregroundID, 2, "agent.readonly.timeout")

		background, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeBackground))
		if err != nil {
			t.Fatal(err)
		}
		backgroundID := uuid.MustParse(string(background.RunID))
		expireRun(t, fixture, backgroundID)
		token := uuid.New()
		claimed, err := worker.Runs.Claim(ctx, fixture.UserA, backgroundID, token, false)
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Run.Status != "failed" || claimed.Run.ErrorCode == nil || *claimed.Run.ErrorCode != "timeout" || claimed.Execution.Token != token || claimed.Run.Version != 2 {
			t.Fatalf("Claim() timeout = %#v", claimed)
		}
		assertRunTransaction(t, fixture, backgroundID, 2, "agent.readonly.timeout")

		deadlineFinish, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
		if err != nil {
			t.Fatal(err)
		}
		deadlineFinishID := uuid.MustParse(string(deadlineFinish.RunID))
		expireRun(t, fixture, deadlineFinishID)
		if _, err = api.Runs.Finish(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), deadlineFinishID, int64(deadlineFinish.Version), completedReadonlyFinish("too late")); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("Finish() beyond deadline error = %v, want conflict", err)
		}
		deadlineResult, err := api.Runs.Get(ctx, fixture.UserA, deadlineFinishID)
		if err != nil || deadlineResult.Error == nil || deadlineResult.Error.Code != agentprotocol.ErrorCodeTimeout {
			t.Fatalf("deadline-priority result = %#v, error=%v", deadlineResult, err)
		}
	})

	t.Run("reliable redelivery records interruption without rerun", func(t *testing.T) {
		created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeBackground))
		if err != nil {
			t.Fatal(err)
		}
		runID := uuid.MustParse(string(created.RunID))
		oldToken, newToken := uuid.New(), uuid.New()
		claimed, err := worker.Runs.Claim(ctx, fixture.UserA, runID, oldToken, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = worker.Runs.Claim(ctx, fixture.UserA, runID, newToken, false); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("competing Claim() error = %v, want conflict", err)
		}
		interrupted, err := worker.Runs.Claim(ctx, fixture.UserA, runID, newToken, true)
		if err != nil {
			t.Fatal(err)
		}
		if interrupted.Run.Status != "failed" || interrupted.Execution.Token != newToken || interrupted.Run.ErrorCode == nil || *interrupted.Run.ErrorCode != "internal_error" || interrupted.Run.ErrorMessage == nil || *interrupted.Run.ErrorMessage != "execution_interrupted" {
			t.Fatalf("redelivered Claim() = %#v", interrupted)
		}
		if interrupted.Run.StartedAt == nil || claimed.Run.StartedAt == nil || !interrupted.Run.StartedAt.Equal(*claimed.Run.StartedAt) {
			t.Fatalf("redelivery changed start time: before=%v after=%v", claimed.Run.StartedAt, interrupted.Run.StartedAt)
		}
		assertRunTransaction(t, fixture, runID, 3, "agent.readonly.fail")
		if _, err = worker.Runs.Claim(ctx, fixture.UserA, runID, uuid.New(), false); err != nil {
			t.Fatalf("terminal Claim() error = %v", err)
		}
	})

	t.Run("trusted foreground failure uses the nil token sentinel", func(t *testing.T) {
		created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
		if err != nil {
			t.Fatal(err)
		}
		runID := uuid.MustParse(string(created.RunID))
		actor := agentexecution.Actor{UserID: fixture.UserA, Mode: agentprotocol.ExecutionModeForeground}
		state := agentprotocol.RuntimeState{
			ProtocolVersion: readonlyTestProtocolVersion, RunID: runID.String(), ExecutionMode: agentprotocol.ExecutionModeForeground,
			Phase: agentprotocol.RuntimePhaseCompleted, Messages: []agentprotocol.Message{},
			CapabilitySnapshot: created.CapabilitySnapshot, Budget: created.Budget,
		}
		if err = api.Runs.Complete(ctx, actor, runID, state, nil); !errors.Is(err, service.ErrValidation) {
			t.Fatalf("foreground Complete() error = %v, want ErrValidation", err)
		}
		if err = api.Runs.Fail(ctx, actor, runID, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "foreground provider failed", Retryable: true}); err != nil {
			t.Fatal(err)
		}
		if err = api.Runs.Fail(ctx, actor, runID, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "late failure"}); err != nil {
			t.Fatalf("duplicate terminal Fail() error = %v", err)
		}
		failed, err := api.Runs.Get(ctx, fixture.UserA, runID)
		if err != nil || failed.Status != agentprotocol.ReadonlyRunViewStatusFailed || failed.Error == nil || failed.Error.Code != agentprotocol.ErrorCodeProviderUnavailable {
			t.Fatalf("foreground Fail() result = %#v, error=%v", failed, err)
		}
		assertRunTransaction(t, fixture, runID, 2, "agent.readonly.fail")
	})

	t.Run("legacy run without execution is rejected", func(t *testing.T) {
		runID := uuid.New()
		if _, err := fixture.Migrator.Exec(ctx, `INSERT INTO dayorder.agent_runs (id,user_id,intent,status,action_mode,scope) VALUES ($1,$2,'legacy','ready','read','{}')`, runID, fixture.UserA); err != nil {
			t.Fatal("insert legacy run")
		}
		if _, err := api.Runs.Get(ctx, fixture.UserA, runID); !errors.Is(err, model.ErrNotFound) {
			t.Fatalf("Get(legacy) error = %v, want not found", err)
		}
	})

	t.Run("creation rate is capped at ten per minute", func(t *testing.T) {
		for index := 0; index < 9; index++ {
			created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserB, fixture.DeviceB), validReadonlyStart(agentprotocol.ExecutionModeForeground))
			if err != nil {
				t.Fatalf("Create() %d error = %v", index+1, err)
			}
			if _, err = api.Runs.Cancel(ctx, readonlyMutationFor(fixture.UserB, fixture.DeviceB), uuid.MustParse(string(created.RunID)), int64(created.Version)); err != nil {
				t.Fatalf("Cancel() %d error = %v", index+1, err)
			}
		}
		tenth, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserB, fixture.DeviceB), validReadonlyStart(agentprotocol.ExecutionModeForeground))
		if err != nil {
			t.Fatal(err)
		}
		tenthID := uuid.MustParse(string(tenth.RunID))
		expireRun(t, fixture, tenthID)
		if _, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserB, fixture.DeviceB), validReadonlyStart(agentprotocol.ExecutionModeForeground)); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("eleventh Create() error = %v, want conflict", err)
		}
		var status string
		if err := fixture.Migrator.QueryRow(ctx, `SELECT status FROM dayorder.agent_runs WHERE user_id=$1 AND id=$2`, fixture.UserB, tenthID).Scan(&status); err != nil {
			t.Fatal("read tenth run after rate conflict")
		}
		if status != "failed" {
			t.Fatalf("tenth run status immediately after rate conflict = %q, want failed", status)
		}
		converged, err := api.Runs.Get(ctx, fixture.UserB, tenthID)
		if err != nil || converged.Status != agentprotocol.ReadonlyRunViewStatusFailed || converged.Error == nil || converged.Error.Code != agentprotocol.ErrorCodeTimeout {
			t.Fatalf("expired tenth run = %#v, error=%v", converged, err)
		}
	})
}

func TestReadonlyCompletionPreservesAuthoritativeIncompleteAccounting(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	worker := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeBackground))
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(created.RunID))
	token := uuid.New()
	claimed, err := worker.Runs.Claim(ctx, fixture.UserA, runID, token, false)
	if err != nil {
		t.Fatal(err)
	}

	summary := "accounting remains server-owned"
	state := terminalRuntimeState(claimed, agentprotocol.RuntimePhaseCompleted, &summary, nil)
	state.Usage = agentprotocol.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	actor := agentexecution.Actor{UserID: fixture.UserA, Token: token, Mode: agentprotocol.ExecutionModeBackground}
	if err = worker.Runs.Complete(ctx, actor, runID, state, nil); !errors.Is(err, service.ErrValidation) {
		t.Fatalf("Complete(runtime usage ahead of ledger) error = %v, want ErrValidation", err)
	}
	aheadView, err := api.Runs.Get(ctx, fixture.UserA, runID)
	if err != nil || aheadView.Status != agentprotocol.ReadonlyRunViewStatusAnalyzing || aheadView.Usage != (agentprotocol.Usage{}) {
		t.Fatalf("run after ahead-of-ledger Complete = %#v, error=%v", aheadView, err)
	}

	repository := postgresstore.NewAgentExecutionRepository()
	authoritative := agentprotocol.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30}
	if err = worker.Transactor.WithUser(ctx, fixture.UserA, func(ctx context.Context, tx database.Tx) error {
		record, getErr := repository.Get(ctx, tx, fixture.UserA, runID, true)
		if getErr != nil {
			return getErr
		}
		expectedVersion := record.Run.Version
		record.Execution.KnownUsage = authoritative
		record.Execution.ReservedTokens = 400
		record.Execution.UsageComplete = false
		return repository.Save(ctx, tx, record, expectedVersion, token)
	}); err != nil {
		t.Fatalf("seed authoritative accounting: %v", err)
	}
	if err = worker.Runs.Complete(ctx, actor, runID, state, nil); err != nil {
		t.Fatal(err)
	}

	var stored agentexecution.Record
	if err = worker.Transactor.WithUser(ctx, fixture.UserA, func(ctx context.Context, tx database.Tx) error {
		var getErr error
		stored, getErr = repository.Get(ctx, tx, fixture.UserA, runID, false)
		return getErr
	}); err != nil {
		t.Fatalf("load completed accounting: %v", err)
	}
	if stored.Execution.KnownUsage != authoritative || stored.Execution.ReservedTokens != 400 || stored.Execution.UsageComplete {
		t.Fatalf("completed accounting = usage %#v, reserved %d, complete %t", stored.Execution.KnownUsage, stored.Execution.ReservedTokens, stored.Execution.UsageComplete)
	}
}

func TestReadonlyDeadlineIsCheckedAfterLockWait(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	created, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), validReadonlyStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(created.RunID))
	lock, err := fixture.Migrator.Begin(ctx)
	if err != nil {
		t.Fatal("begin deadline lock transaction")
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err = lock.Exec(ctx, `UPDATE dayorder.agent_run_executions SET deadline=clock_timestamp()+interval '250 milliseconds' WHERE run_id=$1`, runID); err != nil {
		t.Fatal("set near deadline")
	}
	var status string
	if err = lock.QueryRow(ctx, `SELECT status FROM dayorder.agent_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&status); err != nil {
		t.Fatal("lock readonly run")
	}

	result := make(chan error, 1)
	go func() {
		_, finishErr := api.Runs.Finish(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), runID, int64(created.Version), completedReadonlyFinish("must time out after waiting"))
		result <- finishErr
	}()
	time.Sleep(500 * time.Millisecond)
	if err = lock.Commit(ctx); err != nil {
		t.Fatal("release deadline lock")
	}
	if err = <-result; !errors.Is(err, model.ErrConflict) {
		t.Fatalf("Finish() after lock wait error = %v, want conflict", err)
	}
	view, err := api.Runs.Get(ctx, fixture.UserA, runID)
	if err != nil || view.Status != agentprotocol.ReadonlyRunViewStatusFailed || view.Error == nil || view.Error.Code != agentprotocol.ErrorCodeTimeout {
		t.Fatalf("run after deadline lock wait = %#v, error=%v", view, err)
	}
}

type createResult struct {
	view agentprotocol.ReadonlyRunView
	err  error
}

func readonlyMutationFor(userID, deviceID uuid.UUID) service.MutationContext {
	return service.MutationContext{UserID: userID, DeviceID: deviceID, MutationID: uuid.New(), RequestID: uuid.New()}
}

func completedReadonlyFinish(summary string) agentprotocol.ReadonlyRunFinish {
	return agentprotocol.ReadonlyRunFinish{Phase: agentprotocol.ReadonlyRunFinishPhaseCompleted, Summary: summary, Steps: []agentprotocol.ReadonlyRunFinishStepsElem{}}
}

func terminalRuntimeState(record agentexecution.Record, phase agentprotocol.RuntimePhase, summary *string, runError *agentprotocol.AgentError) agentprotocol.RuntimeState {
	state := agentprotocol.RuntimeState{
		ProtocolVersion: readonlyTestProtocolVersion, RunID: record.Run.ID.String(), ExecutionMode: record.Execution.Mode,
		Phase: phase, Messages: []agentprotocol.Message{}, CapabilitySnapshot: record.Execution.Capabilities,
		Budget: record.Execution.Budget, Usage: agentprotocol.Usage{},
		AssistantDraft: summary, Error: runError,
	}
	if phase == agentprotocol.RuntimePhaseCompleted && summary != nil {
		state.Messages = append(state.Messages, agentprotocol.Message{
			Role:    agentprotocol.MessageRoleAssistant,
			Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: summary}},
		})
		state.AssistantDraft = nil
	}
	return state
}

const readonlyTestProtocolVersion = "2.0"

func expireRun(t testing.TB, fixture *agenttest.Database, runID uuid.UUID) {
	t.Helper()
	if _, err := fixture.Migrator.Exec(context.Background(), `UPDATE dayorder.agent_run_executions SET deadline=now()-interval '1 second' WHERE run_id=$1`, runID); err != nil {
		t.Fatal("expire readonly run")
	}
}

func assertDatabaseCount(t testing.TB, fixture *agenttest.Database, query string, want int, arguments ...any) {
	t.Helper()
	var got int
	if err := fixture.Migrator.QueryRow(context.Background(), query, arguments...).Scan(&got); err != nil {
		t.Fatal("query readonly integration count")
	}
	if got != want {
		t.Fatalf("database count = %d, want %d", got, want)
	}
}

func assertRunTransaction(t testing.TB, fixture *agenttest.Database, runID uuid.UUID, version int, action string) {
	t.Helper()
	assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.sync_changes WHERE entity_type='agent_run' AND entity_id=$1 AND entity_version=$2`, 1, runID, version)
	assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.audit_events a JOIN dayorder.audit_event_entities e ON e.user_id=a.user_id AND e.audit_event_id=a.id WHERE e.entity_type='agent_run' AND e.entity_id=$1 AND a.action=$2`, 1, runID, action)
}
