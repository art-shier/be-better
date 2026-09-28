package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestAgentCalendarObservabilityEmitsSettledToolFailureWithoutPrivateText(t *testing.T) {
	observer := &recordingAgentObserver{}
	var logs bytes.Buffer
	reader, _, calendar, actor, runID := newAgentCalendarUnitFixtureForModeWithConfig(t, agentprotocol.ExecutionModeForeground, func(config *service.AgentReadonlyConfig) {
		config.Observer = observer
		config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	reader.SetObserver(observer)
	calendar.list = func(context.Context) ([]model.CalendarEvent, error) {
		return nil, errors.New("calendar title canary Cookie=private api_key=secret")
	}

	_, err := reader.Read(context.Background(), actor, runID, "call-observed", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	assertAgentReadErrorCode(t, err, agentprotocol.ErrorCodeToolFailed)
	observations := observer.snapshot()
	if len(observations) != 1 {
		t.Fatalf("tool observations = %#v, want one settled attempt", observations)
	}
	got := observations[0]
	if got.Kind != "tool" || got.Mode != "foreground" || got.ToolID != "dayorder.calendar.read" ||
		got.ModelProfile != "readonly-default" || got.Outcome != "failed" || got.ErrorCode != "tool_failed" ||
		got.Attempts != 1 || !got.UsageComplete {
		t.Fatalf("tool observation = %#v", got)
	}
	logged := logs.String()
	for _, required := range []string{"agent tool completed", "runId=" + runID.String(), "callId=call-observed", "tool=dayorder.calendar.read", "errorCode=tool_failed"} {
		if !strings.Contains(logged, required) {
			t.Errorf("controlled log missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"calendar title canary", "Cookie=private", "api_key=secret"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("controlled log exposed %q: %s", forbidden, logged)
		}
	}
}

func TestAgentCalendarObservabilityMeasuresStoredCancellationToActualDependencyExit(t *testing.T) {
	observer := &recordingAgentObserver{}
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixtureForModeWithConfig(t, agentprotocol.ExecutionModeBackground, func(config *service.AgentReadonlyConfig) {
		config.Observer = observer
	})
	reader.SetObserver(observer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calendar.list = func(ctx context.Context) ([]model.CalendarEvent, error) {
		requestedAt := time.Now()
		code := string(agentprotocol.ErrorCodeCancelled)
		runs.record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusStopped)
		runs.record.Run.ErrorCode = &code
		runs.record.Run.FinishedAt = &requestedAt
		cancel()
		time.Sleep(25 * time.Millisecond)
		return nil, ctx.Err()
	}

	_, err := reader.Read(ctx, actor, runID, "cancel-observed", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error=%v, want context.Canceled", err)
	}
	observations := observer.snapshot()
	if len(observations) != 1 || observations[0].Kind != "tool" || observations[0].Outcome != "cancelled" ||
		observations[0].ErrorCode != "cancelled" || observations[0].CancelLatency < 20*time.Millisecond || observations[0].CancelLatency > time.Second {
		t.Fatalf("Calendar cancellation observation = %#v", observations)
	}
}

func TestAgentCalendarObservabilityReportsSettlementFailureAfterDependencyExit(t *testing.T) {
	observer := &recordingAgentObserver{}
	var logs bytes.Buffer
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixtureForModeWithConfig(t, agentprotocol.ExecutionModeForeground, func(config *service.AgentReadonlyConfig) {
		config.Observer = observer
		config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	reader.SetObserver(observer)
	dependencyExitedAt := time.Time{}
	calendar.list = func(context.Context) ([]model.CalendarEvent, error) {
		dependencyExitedAt = time.Now()
		return nil, context.Canceled
	}
	runs.settlementErr = errors.New("settlement canary Cookie=private api_key=secret")

	_, err := reader.Read(context.Background(), actor, runID, "call-settlement-uncertain", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error=%v, want original context.Canceled identity", err)
	}
	var cleanup *agentexecution.Error
	if !errors.As(err, &cleanup) || cleanup.Agent.Code != agentprotocol.ErrorCodeToolFailed {
		t.Fatalf("Read() error=%v, want controlled settlement failure", err)
	}
	if strings.Contains(err.Error(), "settlement canary") || strings.Contains(err.Error(), "Cookie=private") || strings.Contains(err.Error(), "api_key=secret") {
		t.Fatalf("Read() leaked settlement detail: %v", err)
	}
	if dependencyExitedAt.IsZero() || runs.settlementEnteredAt.IsZero() || runs.settlementEnteredAt.Before(dependencyExitedAt) {
		t.Fatalf("dependency exit=%s settlement entry=%s, want settlement after dependency exit", dependencyExitedAt, runs.settlementEnteredAt)
	}
	if runs.settlementDeadline.IsZero() || !runs.settlementDeadline.After(runs.settlementEnteredAt) ||
		runs.settlementDeadline.Sub(runs.settlementEnteredAt) > 5*time.Second {
		t.Fatalf("settlement entry=%s deadline=%s, want positive remaining budget at most five seconds", runs.settlementEnteredAt, runs.settlementDeadline)
	}

	observations := observer.snapshot()
	if len(observations) != 1 {
		t.Fatalf("settlement-failure observations = %#v, want exactly one actual Tool attempt", observations)
	}
	got := observations[0]
	if got.Kind != "tool" || got.Mode != "foreground" || got.ToolID != "dayorder.calendar.read" ||
		got.ModelProfile != "readonly-default" || got.Outcome != "cancelled" || got.ErrorCode != "cancelled" ||
		got.UsageComplete || got.CancelLatency != 0 || got.Attempts != 1 {
		t.Fatalf("settlement-failure observation = %#v", got)
	}
	logged := logs.String()
	if strings.Count(logged, "agent tool completed") != 1 {
		t.Fatalf("controlled Tool completion log count != 1: %s", logged)
	}
	for _, required := range []string{
		"runId=" + runID.String(), "callId=call-settlement-uncertain", "outcome=cancelled",
		"errorCode=cancelled", "settlementComplete=false",
	} {
		if !strings.Contains(logged, required) {
			t.Errorf("controlled log missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"outcome=completed", "settlement canary", "Cookie=private", "api_key=secret"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("controlled log exposed or overstated %q: %s", forbidden, logged)
		}
	}
}

func TestAgentCalendarReadValidatesArgumentsBeforeCalendarAccess(t *testing.T) {
	reader, _, calendar, actor, runID := newAgentCalendarUnitFixture(t)
	longCursor := agentprotocol.CalendarCursor(strings.Repeat("x", 4097))
	tests := []struct {
		name  string
		input agentprotocol.CalendarReadInput
		code  agentprotocol.ErrorCode
	}{
		{"reversed", calendarInput("2026-09-06T00:00:00Z", "2026-09-05T00:00:00Z", 20), agentprotocol.ErrorCodeValidationFailed},
		{"over 31 days", calendarInput("2026-09-05T00:00:00Z", "2026-10-07T00:00:00Z", 20), agentprotocol.ErrorCodeValidationFailed},
		{"limit", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 51), agentprotocol.ErrorCodeValidationFailed},
		{"cursor bytes", agentprotocol.CalendarReadInput{Start: "2026-09-05T00:00:00Z", End: "2026-09-06T00:00:00Z", Cursor: &longCursor, Limit: 20}, agentprotocol.ErrorCodeValidationFailed},
		{"outside run", calendarInput("2026-09-04T23:59:59Z", "2026-09-06T00:00:00Z", 20), agentprotocol.ErrorCodePermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := reader.Read(context.Background(), actor, runID, "invalid-"+test.name, test.input)
			assertAgentReadErrorCode(t, err, test.code)
		})
	}
	if calendar.listCalls != 0 {
		t.Fatalf("invalid requests reached CalendarService %d times", calendar.listCalls)
	}
}

func TestAgentCalendarReadUnwrapsProtocolErrorsWithoutLeakingWrappers(t *testing.T) {
	protocol := agentprotocol.AgentError{Code: agentprotocol.ErrorCodePermissionDenied, Message: "calendar read denied", Retryable: false}
	tests := []struct {
		name string
		err  error
	}{
		{"pointer", &agentexecution.Error{Agent: protocol}},
		{"value", agentexecution.Error{Agent: protocol}},
		{"wrapped pointer", fmt.Errorf("database detail: %w", &agentexecution.Error{Agent: protocol})},
		{"wrapped value", fmt.Errorf("database detail: %w", agentexecution.Error{Agent: protocol})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, runs, _, actor, runID := newAgentCalendarUnitFixture(t)
			runs.getErr = test.err
			_, err := reader.Read(context.Background(), actor, runID, "protocol-error", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
			assertAgentReadErrorCode(t, err, protocol.Code)
			if err.Error() != protocol.Message {
				t.Fatalf("Read() exposed wrapper error %q, want %q", err, protocol.Message)
			}
		})
	}
}

func TestAgentCalendarReadProjectsUTCDataAndDeduplicatesEquivalentCallRefs(t *testing.T) {
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixture(t)
	eventID := uuid.New()
	location, source := "secret room", "private calendar"
	goalID := uuid.New()
	calendar.events = []model.CalendarEvent{{
		ID: eventID, Title: "Planning", StartAt: time.Date(2026, 9, 5, 9, 0, 0, 123000000, time.FixedZone("CST", 8*60*60)),
		EndAt: time.Date(2026, 9, 5, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60)), Timezone: "Asia/Shanghai",
		Kind: "focus", Version: 7, Location: &location, SourceCalendar: &source, GoalID: &goalID,
	}}

	first, err := reader.Read(context.Background(), actor, runID, "same-call", calendarInput("2026-09-05T08:00:00+08:00", "2026-09-06T08:00:00+08:00", 0))
	if err != nil || !first.Ok {
		t.Fatalf("first Read() result=%#v error=%v", first, err)
	}
	second, err := reader.Read(context.Background(), actor, runID, "same-call", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if err != nil || !second.Ok || !reflect.DeepEqual(first, second) {
		t.Fatalf("equivalent Read() first=%#v second=%#v error=%v", first, second, err)
	}

	var data agentprotocol.CalendarReadData
	raw, _ := json.Marshal(first.Data)
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Events) != 1 || data.Events[0].ID != agentprotocol.UUID(eventID.String()) || data.Events[0].Title != "Planning" ||
		data.Events[0].StartAt != "2026-09-05T01:00:00.123Z" || data.Events[0].EndAt != "2026-09-05T02:00:00Z" || data.Events[0].Version != 7 {
		t.Fatalf("projected data=%#v", data)
	}
	if data.Window.Start != "2026-09-05T00:00:00Z" || data.Window.End != "2026-09-06T00:00:00Z" || data.NextCursor != nil || data.HasMore {
		t.Fatalf("projected page metadata=%#v", data)
	}
	if strings.Contains(string(raw), "secret room") || strings.Contains(string(raw), "private calendar") || strings.Contains(string(raw), goalID.String()) {
		t.Fatalf("projection exposed non-minimal fields: %s", raw)
	}
	if len(runs.refs) != 1 || runs.refs[0] != (model.AgentSourceRefDraft{EntityType: "calendar_event", EntityID: eventID, EntityVersion: 7, LabelSnapshot: "Planning"}) {
		t.Fatalf("recorded refs=%#v", runs.refs)
	}
	if len(runs.operations) != 1 || runs.operations[0].Attempts != 2 || runs.operations[0].State != "completed" || runs.operations[0].Usage != (agentprotocol.Usage{}) {
		t.Fatalf("operation=%#v", runs.operations)
	}
	if runs.record.Execution.KnownUsage != (agentprotocol.Usage{}) || runs.record.Execution.ReservedTokens != 0 || !runs.record.Execution.UsageComplete {
		t.Fatalf("calendar read changed model accounting: %#v", runs.record.Execution)
	}
}

func TestAgentCalendarReadReturnsExplicitEmptyPage(t *testing.T) {
	reader, _, _, actor, runID := newAgentCalendarUnitFixture(t)
	result, err := reader.Read(context.Background(), actor, runID, "empty", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if err != nil {
		t.Fatal(err)
	}
	var data agentprotocol.CalendarReadData
	raw, _ := json.Marshal(result.Data)
	if err = json.Unmarshal(raw, &data); err != nil || data.Events == nil || len(data.Events) != 0 || data.NextCursor != nil || data.HasMore {
		t.Fatalf("empty data=%s error=%v", raw, err)
	}
}

func TestAgentCalendarReadRejectsOversizedResultWithoutRefs(t *testing.T) {
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixture(t)
	calendar.events = []model.CalendarEvent{{
		ID: uuid.New(), Title: strings.Repeat("界", 70_000), StartAt: time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC),
		EndAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC), Timezone: "UTC", Kind: "fixed", Version: 1,
	}}
	_, err := reader.Read(context.Background(), actor, runID, "oversized", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	assertAgentReadErrorCode(t, err, agentprotocol.ErrorCodeValidationFailed)
	if len(runs.refs) != 0 || len(runs.operations) != 1 || runs.operations[0].State != "failed" || runs.operations[0].ErrorCode != "validation_failed" {
		t.Fatalf("oversized publication refs=%#v operations=%#v", runs.refs, runs.operations)
	}
}

func TestAgentCalendarReadCancellationSettlesWithoutPublishing(t *testing.T) {
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	calendar.list = func(ctx context.Context) ([]model.CalendarEvent, error) {
		cancel()
		return nil, ctx.Err()
	}
	_, err := reader.Read(ctx, actor, runID, "cancelled", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error=%v, want context.Canceled", err)
	}
	if len(runs.refs) != 0 || len(runs.operations) != 1 || runs.operations[0].State != "failed" || runs.operations[0].ErrorCode != "cancelled" {
		t.Fatalf("cancelled publication refs=%#v operations=%#v", runs.refs, runs.operations)
	}
}

func TestAgentCalendarReadSettlesCancellationAfterRefsAndDuringEnd(t *testing.T) {
	tests := []struct {
		name       string
		cancelWhen func(*agentCalendarRunStore, context.CancelFunc)
		wantState  string
		wantCode   string
	}{
		{"after refs", func(store *agentCalendarRunStore, cancel context.CancelFunc) {
			store.afterRefs = func(*agentCalendarRunStore) { cancel() }
		}, "failed", "cancelled"},
		{"during end", func(store *agentCalendarRunStore, cancel context.CancelFunc) {
			store.onOperations = func(call int) {
				if call == 2 {
					cancel()
				}
			}
		}, "completed", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, runs, calendar, actor, runID := newAgentCalendarUnitFixture(t)
			calendar.events = []model.CalendarEvent{{ID: uuid.New(), Title: "cancel race", StartAt: time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC), Timezone: "UTC", Kind: "fixed", Version: 1}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.cancelWhen(runs, cancel)
			result, err := reader.Read(ctx, actor, runID, "cancel-race", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
			if !errors.Is(err, context.Canceled) || result.Ok {
				t.Fatalf("Read() result=%#v error=%v, want cancellation without success", result, err)
			}
			if len(runs.operations) != 1 || runs.operations[0].State != test.wantState || runs.operations[0].ErrorCode != test.wantCode {
				t.Fatalf("operation=%#v, want state/code %q/%q", runs.operations, test.wantState, test.wantCode)
			}
		})
	}
}

func TestAgentCalendarReadPreservesContextWhenSettlementFails(t *testing.T) {
	tests := []struct {
		name        string
		mode        agentprotocol.ExecutionMode
		newContext  func() (context.Context, func())
		prepare     func(*agentCalendarRunStore)
		wantContext error
		wantCleanup agentprotocol.ErrorCode
	}{
		{"cancelled and token rotated", agentprotocol.ExecutionModeBackground, func() (context.Context, func()) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel
		}, func(store *agentCalendarRunStore) {
			store.record.Execution.Token = uuid.New()
		}, context.Canceled, agentprotocol.ErrorCodeVersionConflict},
		{"deadline and settlement error", agentprotocol.ExecutionModeForeground, func() (context.Context, func()) {
			ctx := newManualDeadlineContext()
			return ctx, ctx.expire
		}, func(store *agentCalendarRunStore) {
			store.settlementErr = errors.New("raw database settlement detail")
		}, context.DeadlineExceeded, agentprotocol.ErrorCodeToolFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, runs, calendar, actor, runID := newAgentCalendarUnitFixtureForMode(t, test.mode)
			ctx, stop := test.newContext()
			defer stop()
			calendar.events = []model.CalendarEvent{{ID: uuid.New(), Title: "cleanup conflict", StartAt: time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC), Timezone: "UTC", Kind: "fixed", Version: 1}}
			runs.afterRefs = func(*agentCalendarRunStore) {
				test.prepare(runs)
				stop()
			}
			_, err := reader.Read(ctx, actor, runID, "cleanup-error", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
			if !errors.Is(err, test.wantContext) {
				t.Fatalf("Read() error=%v, want context identity %v", err, test.wantContext)
			}
			var cleanup *agentexecution.Error
			if !errors.As(err, &cleanup) || cleanup.Agent.Code != test.wantCleanup {
				t.Fatalf("Read() error=%v, want cleanup code %s", err, test.wantCleanup)
			}
			if strings.Contains(err.Error(), "raw database") {
				t.Fatalf("Read() leaked settlement detail: %v", err)
			}
			if len(runs.refs) != 1 || len(runs.operations) != 1 || runs.operations[0].State != "running" {
				t.Fatalf("failed settlement refs=%#v operation=%#v", runs.refs, runs.operations)
			}
		})
	}
}

func TestAgentCalendarReadFailureSettlesWhenCancelledDuringEnd(t *testing.T) {
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calendar.list = func(context.Context) ([]model.CalendarEvent, error) {
		return nil, service.ErrValidation
	}
	runs.onOperations = func(call int) {
		if call == 2 {
			cancel()
		}
	}
	_, err := reader.Read(ctx, actor, runID, "failed-cancel-race", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error=%v, want cancellation identity", err)
	}
	var readFailure *agentexecution.Error
	if !errors.As(err, &readFailure) || readFailure.Agent.Code != agentprotocol.ErrorCodeValidationFailed {
		t.Fatalf("Read() error=%v, want validation read failure", err)
	}
	if len(runs.operations) != 1 || runs.operations[0].State != "failed" || runs.operations[0].ErrorCode != "validation_failed" {
		t.Fatalf("operation=%#v, want failed validation settlement", runs.operations)
	}
}

func TestAgentCalendarReadRechecksRunBeforeRefs(t *testing.T) {
	reader, runs, calendar, actor, runID := newAgentCalendarUnitFixture(t)
	calendar.events = []model.CalendarEvent{{ID: uuid.New(), Title: "Too late", StartAt: time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC), Timezone: "UTC", Kind: "fixed", Version: 1}}
	calendar.afterList = func() { runs.record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusStopped) }
	_, err := reader.Read(context.Background(), actor, runID, "late", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
	assertAgentReadErrorCode(t, err, agentprotocol.ErrorCodeVersionConflict)
	if len(runs.refs) != 0 || len(runs.operations) != 1 || runs.operations[0].State != "failed" || runs.operations[0].ErrorCode != "version_conflict" {
		t.Fatalf("late publication refs=%#v operations=%#v", runs.refs, runs.operations)
	}
}

func TestAgentCalendarReadDoesNotPublishSuccessAfterRefsLoseAuthority(t *testing.T) {
	tests := []struct {
		name      string
		mode      agentprotocol.ExecutionMode
		mutate    func(*agentCalendarRunStore)
		operation string
	}{
		{"cancelled", agentprotocol.ExecutionModeForeground, func(store *agentCalendarRunStore) {
			store.record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusStopped)
		}, "completed"},
		{"token rotated", agentprotocol.ExecutionModeBackground, func(store *agentCalendarRunStore) {
			store.record.Execution.Token = uuid.New()
		}, "running"},
		{"deadline elapsed", agentprotocol.ExecutionModeForeground, func(store *agentCalendarRunStore) {
			store.record.Execution.Deadline = time.Date(2026, 9, 7, 11, 59, 59, 0, time.UTC)
		}, "completed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, runs, calendar, actor, runID := newAgentCalendarUnitFixtureForMode(t, test.mode)
			calendar.events = []model.CalendarEvent{{ID: uuid.New(), Title: "Settled too late", StartAt: time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC), Timezone: "UTC", Kind: "fixed", Version: 1}}
			runs.afterRefs = test.mutate
			result, err := reader.Read(context.Background(), actor, runID, "late-after-refs", calendarInput("2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z", 20))
			assertAgentReadErrorCode(t, err, agentprotocol.ErrorCodeVersionConflict)
			if result.Ok || len(runs.refs) != 1 || len(runs.operations) != 1 || runs.operations[0].State != test.operation {
				t.Fatalf("late settled result=%#v refs=%#v operations=%#v", result, runs.refs, runs.operations)
			}
		})
	}
}

func calendarInput(start, end string, limit int) agentprotocol.CalendarReadInput {
	return agentprotocol.CalendarReadInput{Start: agentprotocol.DateTime(start), End: agentprotocol.DateTime(end), Limit: limit}
}

func newAgentCalendarUnitFixture(t testing.TB) (*service.AgentCalendarReadService, *agentCalendarRunStore, *agentCalendarStore, agentexecution.Actor, uuid.UUID) {
	return newAgentCalendarUnitFixtureForMode(t, agentprotocol.ExecutionModeForeground)
}

func newAgentCalendarUnitFixtureForMode(t testing.TB, mode agentprotocol.ExecutionMode) (*service.AgentCalendarReadService, *agentCalendarRunStore, *agentCalendarStore, agentexecution.Actor, uuid.UUID) {
	return newAgentCalendarUnitFixtureForModeWithConfig(t, mode, nil)
}

func newAgentCalendarUnitFixtureForModeWithConfig(t testing.TB, mode agentprotocol.ExecutionMode, configure func(*service.AgentReadonlyConfig)) (*service.AgentCalendarReadService, *agentCalendarRunStore, *agentCalendarStore, agentexecution.Actor, uuid.UUID) {
	t.Helper()
	runsStore := &agentCalendarRunStore{}
	transactor := agentCalendarTransactor{}
	idempotency, _ := service.NewIdempotencyService(&memoryMutationStore{})
	syncWriter, auditWriter, outboxWriter := &memorySyncWriter{}, &memoryAuditWriter{}, &memoryOutboxWriter{}
	commands, err := service.NewCommandService(transactor, idempotency, syncWriter, auditWriter, outboxWriter)
	if err != nil {
		t.Fatal(err)
	}
	config := defaultReadonlyUnitConfig()
	if configure != nil {
		configure(&config)
	}
	config.Store, config.Transactor, config.Commands = runsStore, transactor, commands
	config.SyncWriter, config.AuditWriter = syncWriter, auditWriter
	runs, err := service.NewAgentReadonlyService(config)
	if err != nil {
		t.Fatal(err)
	}
	calendarStore := &agentCalendarStore{}
	cursors, _ := service.NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	calendar, err := service.NewCalendarService(calendarStore, transactor, commands, cursors)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := service.NewAgentCalendarReadService(runs, calendar)
	if err != nil {
		t.Fatal(err)
	}
	input := validReadonlyStart(mode)
	from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
	input.Scope.From, input.Scope.To = &from, &to
	view, err := runs.Create(context.Background(), readonlyMutation(), input)
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(view.RunID))
	actor := agentexecution.Actor{UserID: runsStore.record.Execution.UserID, Mode: mode}
	if mode == agentprotocol.ExecutionModeBackground {
		actor.Token = uuid.New()
		if _, err = runs.Claim(context.Background(), actor.UserID, runID, actor.Token, false); err != nil {
			t.Fatal(err)
		}
	}
	return reader, runsStore, calendarStore, actor, runID
}

type agentCalendarTransactor struct{}

func (agentCalendarTransactor) WithUser(ctx context.Context, _ uuid.UUID, operation func(context.Context, database.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(ctx, readonlyTestTx{})
}

type agentCalendarRunStore struct {
	record              agentexecution.Record
	operations          []agentexecution.Operation
	refs                []model.AgentSourceRefDraft
	afterRefs           func(*agentCalendarRunStore)
	getErr              error
	onOperations        func(int)
	operationCalls      int
	settlementErr       error
	settlementEnteredAt time.Time
	settlementDeadline  time.Time
}

func (*agentCalendarRunStore) LockAccount(context.Context, database.Tx, uuid.UUID) error { return nil }
func (store *agentCalendarRunStore) Create(_ context.Context, _ database.Tx, record agentexecution.Record) error {
	store.record = record
	return nil
}
func (store *agentCalendarRunStore) Get(_ context.Context, _ database.Tx, userID, runID uuid.UUID, _ bool) (agentexecution.Record, error) {
	if store.getErr != nil {
		return agentexecution.Record{}, store.getErr
	}
	if store.record.Execution.UserID != userID || store.record.Run.ID != runID {
		return agentexecution.Record{}, model.ErrNotFound
	}
	return store.record, nil
}
func (store *agentCalendarRunStore) Active(context.Context, database.Tx, uuid.UUID) ([]agentexecution.Record, error) {
	if store.record.Run.ID == uuid.Nil {
		return nil, nil
	}
	return []agentexecution.Record{store.record}, nil
}
func (*agentCalendarRunStore) CountCreatedSince(context.Context, database.Tx, uuid.UUID, time.Time) (int, error) {
	return 0, nil
}
func (store *agentCalendarRunStore) Save(_ context.Context, _ database.Tx, record agentexecution.Record, expectedVersion int64, expectedToken uuid.UUID) error {
	if store.record.Run.Version != expectedVersion || store.record.Execution.Token != expectedToken {
		return model.ErrConflict
	}
	record.Run.Version = expectedVersion + 1
	store.record = record
	return nil
}

func (store *agentCalendarRunStore) Operations(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]agentexecution.Operation, error) {
	store.operationCalls++
	if store.onOperations != nil {
		store.onOperations(store.operationCalls)
	}
	return append([]agentexecution.Operation(nil), store.operations...), nil
}
func (store *agentCalendarRunStore) PutOperation(ctx context.Context, _ database.Tx, operation agentexecution.Operation, expectedState string) error {
	if expectedState == "running" {
		store.settlementEnteredAt = time.Now()
		store.settlementDeadline, _ = ctx.Deadline()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if expectedState == "running" && store.settlementErr != nil {
		return store.settlementErr
	}
	for index := range store.operations {
		if store.operations[index].Kind == operation.Kind && store.operations[index].ID == operation.ID {
			if store.operations[index].State != expectedState {
				return model.ErrConflict
			}
			store.operations[index] = operation
			return nil
		}
	}
	if expectedState != "" {
		return model.ErrConflict
	}
	store.operations = append(store.operations, operation)
	return nil
}
func (store *agentCalendarRunStore) AddRefs(_ context.Context, _ database.Tx, _, _ uuid.UUID, refs []model.AgentSourceRefDraft) error {
	for _, ref := range refs {
		found := false
		for _, existing := range store.refs {
			if existing.EntityType == ref.EntityType && existing.EntityID == ref.EntityID && existing.EntityVersion == ref.EntityVersion {
				found = true
				break
			}
		}
		if !found {
			store.refs = append(store.refs, ref)
		}
	}
	if store.afterRefs != nil {
		store.afterRefs(store)
	}
	return nil
}
func (*agentCalendarRunStore) AddSteps(context.Context, database.Tx, uuid.UUID, uuid.UUID, []model.AgentStepDraft) error {
	return nil
}

type agentCalendarStore struct {
	events    []model.CalendarEvent
	listCalls int
	list      func(context.Context) ([]model.CalendarEvent, error)
	afterList func()
}

type manualDeadlineContext struct {
	context.Context
	done    chan struct{}
	once    sync.Once
	expired atomic.Bool
}

func newManualDeadlineContext() *manualDeadlineContext {
	return &manualDeadlineContext{Context: context.Background(), done: make(chan struct{})}
}

func (ctx *manualDeadlineContext) Done() <-chan struct{} { return ctx.done }
func (ctx *manualDeadlineContext) Err() error {
	if ctx.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}
func (ctx *manualDeadlineContext) expire() {
	ctx.once.Do(func() {
		ctx.expired.Store(true)
		close(ctx.done)
	})
}

func (*agentCalendarStore) CreateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent) (model.CalendarEvent, error) {
	return model.CalendarEvent{}, errors.New("unexpected CreateEvent")
}
func (*agentCalendarStore) GetEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.CalendarEvent, error) {
	return model.CalendarEvent{}, errors.New("unexpected GetEvent")
}
func (store *agentCalendarStore) ListEvents(ctx context.Context, _ database.Tx, _ uuid.UUID, _, _ *time.Time, after *model.ResourcePosition, limit int) ([]model.CalendarEvent, error) {
	store.listCalls++
	if store.list != nil {
		return store.list(ctx)
	}
	events := append([]model.CalendarEvent(nil), store.events...)
	sort.Slice(events, func(i, j int) bool {
		if events[i].StartAt.Equal(events[j].StartAt) {
			return strings.Compare(events[i].ID.String(), events[j].ID.String()) < 0
		}
		return events[i].StartAt.Before(events[j].StartAt)
	})
	if after != nil {
		filtered := events[:0]
		for _, event := range events {
			if event.StartAt.After(after.UpdatedAt) || (event.StartAt.Equal(after.UpdatedAt) && strings.Compare(event.ID.String(), after.ID.String()) > 0) {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	if len(events) > limit {
		events = events[:limit]
	}
	if store.afterList != nil {
		store.afterList()
	}
	return events, nil
}
func (*agentCalendarStore) UpdateEvent(context.Context, database.Tx, uuid.UUID, model.CalendarEvent, int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	return model.CalendarEvent{}, nil, errors.New("unexpected UpdateEvent")
}
func (*agentCalendarStore) DeleteEvent(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.CalendarEvent, []model.CalendarReminder, error) {
	return model.CalendarEvent{}, nil, errors.New("unexpected DeleteEvent")
}
func (*agentCalendarStore) CreateReminder(context.Context, database.Tx, uuid.UUID, model.CalendarReminder) (model.CalendarReminder, error) {
	return model.CalendarReminder{}, errors.New("unexpected CreateReminder")
}
func (*agentCalendarStore) ListReminders(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.CalendarReminder, error) {
	return nil, errors.New("unexpected ListReminders")
}
func (*agentCalendarStore) DeleteReminder(context.Context, database.Tx, uuid.UUID, uuid.UUID, uuid.UUID) (model.CalendarReminder, error) {
	return model.CalendarReminder{}, errors.New("unexpected DeleteReminder")
}
