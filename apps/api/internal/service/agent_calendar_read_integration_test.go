package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"dayorder.local/api/internal/agentbinding"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestAgentCalendarReadScopeDoesNotBroadenAccountCalendarAccess(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	reader, err := service.NewAgentCalendarReadService(api.Runs, api.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	start := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	created, err := api.Calendar.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), service.CalendarEventInput{
		Title: "Scoped event", StartAt: start, EndAt: start.Add(time.Hour), Timezone: "UTC", Kind: "fixed",
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, start, end, code string }{
		{"inside", "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", ""},
		{"before", "2026-09-04T23:59:59Z", "2026-09-06T00:00:00Z", "permission_denied"},
		{"reversed", "2026-09-06T00:00:00Z", "2026-09-05T00:00:00Z", "validation_failed"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validReadonlyStart(agentprotocol.ExecutionModeForeground)
			from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
			input.Scope.From, input.Scope.To = &from, &to
			run, err := api.Runs.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), input)
			if err != nil {
				t.Fatal(err)
			}
			runID := uuid.MustParse(string(run.RunID))
			actor := agentexecution.Actor{UserID: fixture.UserA, Mode: agentprotocol.ExecutionModeForeground}
			result, readErr := reader.Read(ctx, actor, runID, "scope-"+test.name, agentprotocol.CalendarReadInput{
				Start: agentprotocol.DateTime(test.start), End: agentprotocol.DateTime(test.end), Limit: 20,
			})
			if test.code == "" {
				if readErr != nil || !result.Ok {
					t.Fatalf("Read() result = %#v, error = %v", result, readErr)
				}
				var data agentprotocol.CalendarReadData
				raw, _ := json.Marshal(result.Data)
				if err = json.Unmarshal(raw, &data); err != nil || len(data.Events) != 1 || string(data.Events[0].ID) != created.Event.ID.String() || data.Events[0].Version != int(created.Event.Version) {
					t.Fatalf("Read() data = %s, error = %v", raw, err)
				}
			} else {
				assertAgentReadErrorCode(t, readErr, agentprotocol.ErrorCode(test.code))
				if !reflect.DeepEqual(result, agentprotocol.ToolResult{}) {
					t.Fatalf("failed Read() result = %#v, want zero", result)
				}
				binding, bindErr := agentbinding.NewCalendar(actor, runID, reader)
				if bindErr != nil {
					t.Fatal(bindErr)
				}
				boundResult, invokeErr := binding.Invoke(ctx, map[string]any{"start": test.start, "end": test.end, "limit": 20}, agenttool.Context{RunID: runID.String(), CallID: "bound-" + test.name})
				if invokeErr != nil || boundResult.Error == nil || boundResult.Error.Code != agentprotocol.ErrorCode(test.code) {
					t.Fatalf("bound Invoke() result=%#v error=%v", boundResult, invokeErr)
				}
			}
			cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
		})
	}

	page, err := api.Calendar.List(ctx, fixture.UserA, nil, nil, "", 20)
	if err != nil || len(page.Events) != 1 || page.Events[0].ID != created.Event.ID {
		t.Fatalf("ordinary Calendar.List() page = %#v, error = %v", page, err)
	}
}

func TestAgentCalendarReadTransactionsAcrossDatabaseRoles(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	worker := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	apiReader, err := service.NewAgentCalendarReadService(api.Runs, api.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	workerReader, err := service.NewAgentCalendarReadService(worker.Runs, worker.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000010"),
		uuid.MustParse("00000000-0000-0000-0000-000000000011"),
		uuid.MustParse("00000000-0000-0000-0000-000000000020"),
	}
	events := []service.CalendarEventInput{
		{ID: &ids[0], Title: "ends at start", StartAt: mustTime("2026-09-04T23:00:00Z"), EndAt: mustTime("2026-09-05T00:00:00Z"), Timezone: "UTC", Kind: "fixed"},
		{ID: &ids[1], Title: "starts at end", StartAt: mustTime("2026-09-06T00:00:00Z"), EndAt: mustTime("2026-09-06T01:00:00Z"), Timezone: "UTC", Kind: "fixed"},
		{ID: &ids[3], Title: "same start high ID", StartAt: mustTime("2026-09-05T09:00:00Z"), EndAt: mustTime("2026-09-05T10:00:00Z"), Timezone: "UTC", Kind: "focus"},
		{ID: &ids[2], Title: "same start low ID", StartAt: mustTime("2026-09-05T09:00:00Z"), EndAt: mustTime("2026-09-05T09:30:00Z"), Timezone: "UTC", Kind: "focus"},
		{ID: &ids[4], Title: "crosses query", StartAt: mustTime("2026-09-05T12:00:00Z"), EndAt: mustTime("2026-09-05T14:00:00Z"), Timezone: "UTC", Kind: "personal"},
	}
	for _, event := range events {
		if _, err = api.Calendar.Create(ctx, readonlyMutationFor(fixture.UserA, fixture.DeviceA), event); err != nil {
			t.Fatal(err)
		}
	}
	userBEventID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	if _, err = api.Calendar.Create(ctx, readonlyMutationFor(fixture.UserB, fixture.DeviceB), service.CalendarEventInput{
		ID: &userBEventID, Title: "worker event", StartAt: mustTime("2026-09-05T15:00:00Z"), EndAt: mustTime("2026-09-05T16:00:00Z"), Timezone: "UTC", Kind: "health",
	}); err != nil {
		t.Fatal(err)
	}

	foregroundID, foregroundActor := createScopedCalendarRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA, agentprotocol.ExecutionModeForeground)
	first := readCalendarData(t, ctx, apiReader, foregroundActor, foregroundID, "page-1", calendarInputWithCursor("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 3, nil))
	if !first.HasMore || first.NextCursor == nil || len(first.Events) != 3 {
		t.Fatalf("first page=%#v", first)
	}
	firstCursor := agentprotocol.CalendarCursor(*first.NextCursor)
	second := readCalendarData(t, ctx, apiReader, foregroundActor, foregroundID, "page-2", calendarInputWithCursor("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 3, &firstCursor))
	if second.HasMore || second.NextCursor != nil || len(second.Events) != 2 {
		t.Fatalf("second page=%#v", second)
	}
	gotIDs := make([]string, 0, 5)
	for _, event := range append(first.Events, second.Events...) {
		gotIDs = append(gotIDs, string(event.ID))
	}
	wantIDs := []string{ids[0].String(), ids[2].String(), ids[3].String(), ids[4].String(), ids[1].String()}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("closed-window ordering=%#v, want %#v", gotIDs, wantIDs)
	}

	crossing := readCalendarData(t, ctx, apiReader, foregroundActor, foregroundID, "crossing", calendarInput("2026-09-05T13:00:00Z", "2026-09-05T13:30:00Z", 20))
	if len(crossing.Events) != 1 || crossing.Events[0].ID != agentprotocol.UUID(ids[4].String()) {
		t.Fatalf("crossing page=%#v", crossing)
	}
	empty := readCalendarData(t, ctx, apiReader, foregroundActor, foregroundID, "empty-live", calendarInput("2026-09-05T04:00:00Z", "2026-09-05T05:00:00Z", 20))
	if empty.Events == nil || len(empty.Events) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty page=%#v", empty)
	}

	readCalendarData(t, ctx, apiReader, foregroundActor, foregroundID, "utc-equivalent", calendarInput("2026-09-05T08:00:00+08:00", "2026-09-06T08:00:00+08:00", 20))
	readCalendarData(t, ctx, apiReader, foregroundActor, foregroundID, "utc-equivalent", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_source_refs WHERE user_id=$1 AND run_id=$2`, 5, fixture.UserA, foregroundID)

	tampered := agentprotocol.CalendarCursor(flipCursor(string(*first.NextCursor)))
	_, err = apiReader.Read(ctx, foregroundActor, foregroundID, "tampered", calendarInputWithCursor("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 3, &tampered))
	assertAgentReadErrorCode(t, err, agentprotocol.ErrorCodeValidationFailed)

	backgroundID, _ := createScopedCalendarRun(t, ctx, fixture, api.Runs, fixture.UserB, fixture.DeviceB, agentprotocol.ExecutionModeBackground)
	token := uuid.New()
	if _, err = worker.Runs.Claim(ctx, fixture.UserB, backgroundID, token, false); err != nil {
		t.Fatal(err)
	}
	backgroundActor := agentexecution.Actor{UserID: fixture.UserB, Token: token, Mode: agentprotocol.ExecutionModeBackground}
	_, err = workerReader.Read(ctx, backgroundActor, backgroundID, "cross-user-cursor", calendarInputWithCursor("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 3, &firstCursor))
	assertAgentReadErrorCode(t, err, agentprotocol.ErrorCodeValidationFailed)
	workerData := readCalendarData(t, ctx, workerReader, backgroundActor, backgroundID, "worker-read", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if len(workerData.Events) != 1 || workerData.Events[0].ID != agentprotocol.UUID(userBEventID.String()) {
		t.Fatalf("worker page=%#v", workerData)
	}

	for _, id := range ids {
		stored, getErr := api.Calendar.Get(ctx, fixture.UserA, id)
		if getErr != nil || stored.Event.Version != 1 {
			t.Fatalf("event %s after reads=%#v error=%v", id, stored.Event, getErr)
		}
		assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.sync_changes WHERE user_id=$1 AND entity_type='calendar_event' AND entity_id=$2`, 1, fixture.UserA, id)
	}
	assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.sync_changes WHERE user_id=$1 AND entity_type='calendar_event' AND entity_id=$2`, 1, fixture.UserB, userBEventID)
	cancelOperationRun(t, ctx, fixture, api.Runs, foregroundID, fixture.UserA, fixture.DeviceA)
	cancelOperationRun(t, ctx, fixture, api.Runs, backgroundID, fixture.UserB, fixture.DeviceB)
}

func TestAgentCalendarReadCancellationSettlesOnRealDatabaseRole(t *testing.T) {
	fixture := agenttest.Open(t)
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	reader, err := service.NewAgentCalendarReadService(api.Runs, api.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	runID, actor := createScopedCalendarRun(t, ctx, fixture, api.Runs, fixture.UserA, fixture.DeviceA, agentprotocol.ExecutionModeForeground)

	lock, err := fixture.Migrator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = lock.Rollback(cleanupCtx)
	}()
	if _, err = lock.Exec(ctx, `LOCK TABLE dayorder.calendar_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	type outcome struct {
		result agentprotocol.ToolResult
		err    error
	}
	outcomes := make(chan outcome, 1)
	go func() {
		result, readErr := reader.Read(readCtx, actor, runID, "cancel-real", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
		outcomes <- outcome{result: result, err: readErr}
	}()
	waitForCalendarOperation(t, ctx, api.Transactor, fixture.UserA, runID, "cancel-real")
	cancelRead()
	unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = lock.Rollback(unlockCtx)
	unlockCancel()
	if err != nil {
		t.Fatal(err)
	}
	var got outcome
	select {
	case got = <-outcomes:
	case <-ctx.Done():
		t.Fatal("cancelled calendar read did not return")
	}
	if got.result.Ok || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled real Read() result=%#v error=%v", got.result, got.err)
	}
	assertCalendarOperationState(t, fixture, runID, "cancel-real", "failed", "cancelled")
	assertDatabaseCount(t, fixture, `SELECT count(*) FROM dayorder.agent_source_refs WHERE run_id=$1`, 0, runID)
	cancelOperationRun(t, ctx, fixture, api.Runs, runID, fixture.UserA, fixture.DeviceA)
}

func createScopedCalendarRun(t testing.TB, ctx context.Context, fixture *agenttest.Database, runs *service.AgentReadonlyService, userID, deviceID uuid.UUID, mode agentprotocol.ExecutionMode) (uuid.UUID, agentexecution.Actor) {
	t.Helper()
	input := validReadonlyStart(mode)
	from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
	input.Scope.From, input.Scope.To = &from, &to
	created, err := runs.Create(ctx, readonlyMutationFor(userID, deviceID), input)
	if err != nil {
		t.Fatal(err)
	}
	return uuid.MustParse(string(created.RunID)), agentexecution.Actor{UserID: userID, Mode: mode}
}

func readCalendarData(t testing.TB, ctx context.Context, reader *service.AgentCalendarReadService, actor agentexecution.Actor, runID uuid.UUID, callID string, input agentprotocol.CalendarReadInput) agentprotocol.CalendarReadData {
	t.Helper()
	result, err := reader.Read(ctx, actor, runID, callID, input)
	if err != nil || !result.Ok {
		t.Fatalf("Read(%s) result=%#v error=%v", callID, result, err)
	}
	raw, _ := json.Marshal(result.Data)
	var data agentprotocol.CalendarReadData
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func calendarInputWithCursor(start, end string, limit int, cursor *agentprotocol.CalendarCursor) agentprotocol.CalendarReadInput {
	return agentprotocol.CalendarReadInput{Start: agentprotocol.DateTime(start), End: agentprotocol.DateTime(end), Limit: limit, Cursor: cursor}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func flipCursor(cursor string) string {
	if cursor[0] == 'A' {
		return "B" + cursor[1:]
	}
	return "A" + cursor[1:]
}

func waitForCalendarOperation(t testing.TB, ctx context.Context, transactor *database.Transactor, userID, runID uuid.UUID, operationID string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		var waiting bool
		err := transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
			return tx.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2 AND operation_id=$3),
  EXISTS (
    SELECT 1 FROM pg_catalog.pg_locks
    WHERE relation='dayorder.calendar_events'::regclass
      AND mode='AccessShareLock' AND NOT granted
  )`, userID, runID, operationID).Scan(&count, &waiting)
		})
		if err != nil {
			t.Fatal(err)
		}
		if count == 1 && waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("calendar operation did not start")
		case <-ticker.C:
		}
	}
}

func assertCalendarOperationState(t testing.TB, fixture *agenttest.Database, runID uuid.UUID, operationID, state, errorCode string) {
	t.Helper()
	var gotState, gotError string
	if err := fixture.Migrator.QueryRow(context.Background(), `SELECT state, error_code FROM dayorder.agent_run_operations WHERE run_id=$1 AND operation_id=$2`, runID, operationID).Scan(&gotState, &gotError); err != nil {
		t.Fatal(err)
	}
	if gotState != state || gotError != errorCode {
		t.Fatalf("operation state/error=%q/%q, want %q/%q", gotState, gotError, state, errorCode)
	}
}

func assertAgentReadErrorCode(t testing.TB, err error, code agentprotocol.ErrorCode) {
	t.Helper()
	var protocolError *agentexecution.Error
	if !errors.As(err, &protocolError) || protocolError.Agent.Code != code || protocolError.Agent.Retryable {
		t.Fatalf("Read() error = %v, want %s", err, code)
	}
}
