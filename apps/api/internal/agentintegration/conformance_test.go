package agentintegration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dayorder.local/api/internal/agentbinding"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentgateway"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agentruntime"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/httpapi"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestRuntimeTraceRecorderSnapshotsWithoutAliasing(t *testing.T) {
	runID := uuid.New()
	text := "synthetic calendar result"
	trace := agentruntime.Trace{
		State:      agentprotocol.RuntimeState{RunID: runID.String()},
		Inputs:     []agentprotocol.RuntimeInput{{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: &text}},
		ModelTurns: 4,
	}
	recorder := newRuntimeTraceRecorder()
	recorder.Record(runID, &trace)

	trace.Inputs[0].Text = nil
	diagnostic := recorder.Diagnostic(runID, true)
	if diagnostic.Status != traceStatusReady || diagnostic.Value == nil ||
		len(diagnostic.Value.Inputs) != 1 || diagnostic.Value.Inputs[0].Text == nil ||
		*diagnostic.Value.Inputs[0].Text != "synthetic calendar result" || diagnostic.Value.ModelTurns != 4 {
		t.Fatalf("snapshot metadata invalid: status=%s error_code=%s value_present=%t", diagnostic.Status, diagnostic.ErrorCode, diagnostic.Value != nil)
	}

	recorder.Close()
	if diagnostic = recorder.Diagnostic(runID, true); diagnostic.Status != traceStatusError || diagnostic.ErrorCode != traceErrorUnavailable {
		t.Fatalf("closed recorder diagnostic: status=%s error_code=%s", diagnostic.Status, diagnostic.ErrorCode)
	}
}

func TestRuntimeTraceRecorderReportsPendingMissingAndSnapshotErrors(t *testing.T) {
	recorder := newRuntimeTraceRecorder()
	runID := uuid.New()
	if got := recorder.Diagnostic(runID, false); got.Status != traceStatusPending {
		t.Fatalf("nonterminal missing trace status=%s error_code=%s", got.Status, got.ErrorCode)
	}
	if got := recorder.Diagnostic(runID, true); got.Status != traceStatusMissing {
		t.Fatalf("terminal missing trace status=%s error_code=%s", got.Status, got.ErrorCode)
	}

	bad := agentruntime.Trace{Inputs: []agentprotocol.RuntimeInput{{
		Type: agentprotocol.RuntimeInputTypeToolResult,
		ToolResult: &agentprotocol.RuntimeInputToolResult{Result: agentprotocol.ToolResult{
			Data: agentprotocol.ToolResultData{"not-json": func() {}},
		}},
	}}}
	recorder.Record(runID, &bad)
	if got := recorder.Diagnostic(runID, true); got.Status != traceStatusError || got.ErrorCode != traceErrorSnapshotFailed {
		t.Fatalf("unserializable trace diagnostic: status=%s error_code=%s", got.Status, got.ErrorCode)
	}
}

func TestRuntimeTraceRecorderEnforcesPerTraceCountAndTotalBounds(t *testing.T) {
	t.Run("per trace", func(t *testing.T) {
		recorder := newRuntimeTraceRecorder()
		runID := uuid.New()
		large := strings.Repeat("x", maxRuntimeTraceBytes+1)
		recorder.Record(runID, &agentruntime.Trace{Inputs: []agentprotocol.RuntimeInput{{
			Type: agentprotocol.RuntimeInputTypeUserMessage, Text: &large,
		}}})
		if got := recorder.Diagnostic(runID, true); got.Status != traceStatusError || got.ErrorCode != traceErrorSizeExceeded {
			t.Fatalf("oversize trace diagnostic: status=%s error_code=%s", got.Status, got.ErrorCode)
		}
	})

	t.Run("entry count", func(t *testing.T) {
		recorder := newRuntimeTraceRecorder()
		for range maxRuntimeTraces {
			recorder.Record(uuid.New(), &agentruntime.Trace{})
		}
		overflowID := uuid.New()
		recorder.Record(overflowID, &agentruntime.Trace{})
		if got := recorder.Diagnostic(overflowID, true); got.Status != traceStatusError || got.ErrorCode != traceErrorCapacityExceeded {
			t.Fatalf("count overflow diagnostic: status=%s error_code=%s", got.Status, got.ErrorCode)
		}
	})

	t.Run("total bytes", func(t *testing.T) {
		recorder := newRuntimeTraceRecorder()
		payload := strings.Repeat("x", maxRuntimeTraceBytes-1024)
		for range 4 {
			recorder.Record(uuid.New(), &agentruntime.Trace{Inputs: []agentprotocol.RuntimeInput{{
				Type: agentprotocol.RuntimeInputTypeUserMessage, Text: &payload,
			}}})
		}
		overflowID := uuid.New()
		recorder.Record(overflowID, &agentruntime.Trace{Inputs: []agentprotocol.RuntimeInput{{
			Type: agentprotocol.RuntimeInputTypeUserMessage, Text: &payload,
		}}})
		if got := recorder.Diagnostic(overflowID, true); got.Status != traceStatusError || got.ErrorCode != traceErrorCapacityExceeded {
			t.Fatalf("total byte overflow diagnostic: status=%s error_code=%s", got.Status, got.ErrorCode)
		}
	})
}

type tracedProcessorStub struct {
	trace *agentruntime.Trace
	err   error
}

func (stub tracedProcessorStub) ProcessWithTrace(_ context.Context, _ model.OutboxEvent) (*agentruntime.Trace, error) {
	return stub.trace, stub.err
}

func TestTraceRecordingProcessorPreservesProcessingError(t *testing.T) {
	wantErr := errors.New("controlled processing failure")
	runID := uuid.New()
	recorder := newRuntimeTraceRecorder()
	processor := traceRecordingProcessor{
		base:   tracedProcessorStub{trace: &agentruntime.Trace{ModelTurns: 4}, err: wantErr},
		traces: recorder,
	}

	err := processor.Process(t.Context(), model.OutboxEvent{AggregateID: runID})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Process() error = %v, want original %v", err, wantErr)
	}
	if got := recorder.Diagnostic(runID, true); got.Status != traceStatusReady || got.Value == nil || got.Value.ModelTurns != 4 {
		t.Fatalf("processing failure trace metadata: status=%s error_code=%s value_present=%t", got.Status, got.ErrorCode, got.Value != nil)
	}
}

func TestCalendarPageDifferenceReportsOnlyTheFirstFieldPath(t *testing.T) {
	cursorA, cursorB := "cursor-a", "cursor-b"
	base := agentprotocol.CalendarReadData{
		Events: []agentprotocol.CalendarReadDataEventsElem{{
			EndAt: "2026-09-07T01:00:00Z", ID: "00000000-0000-0000-0000-000000000001", Kind: "fixed",
			StartAt: "2026-09-07T00:00:00Z", Timezone: "UTC", Title: "fixture", Version: 1,
		}},
		HasMore: true, NextCursor: &cursorA,
		Window: agentprotocol.CalendarReadDataWindow{Start: "2026-09-07T00:00:00Z", End: "2026-09-08T00:00:00Z"},
	}
	tests := []struct {
		name, want string
		mutate     func(agentprotocol.CalendarReadData) agentprotocol.CalendarReadData
	}{
		{"window start", "window.start", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Window.Start = "2026-09-07T00:00:01Z"
			return got
		}},
		{"window end", "window.end", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Window.End = "2026-09-08T00:00:01Z"
			return got
		}},
		{"has more", "hasMore", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.HasMore = false
			return got
		}},
		{"missing cursor", "nextCursor.presence", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.NextCursor = nil
			return got
		}},
		{"cursor", "nextCursor", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.NextCursor = &cursorB
			return got
		}},
		{"event count", "events.length", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData { got.Events = nil; return got }},
		{"event end", "events[0].endAt", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].EndAt = "2026-09-07T01:00:01Z"
			return got
		}},
		{"event id", "events[0].id", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].ID = "00000000-0000-0000-0000-000000000002"
			return got
		}},
		{"event kind", "events[0].kind", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].Kind = "other"
			return got
		}},
		{"event start", "events[0].startAt", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].StartAt = "2026-09-07T00:00:01Z"
			return got
		}},
		{"event timezone", "events[0].timezone", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].Timezone = "Asia/Shanghai"
			return got
		}},
		{"event title", "events[0].title", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].Title = "changed"
			return got
		}},
		{"event version", "events[0].version", func(got agentprotocol.CalendarReadData) agentprotocol.CalendarReadData {
			got.Events[0].Version = 2
			return got
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := base
			got.Events = append([]agentprotocol.CalendarReadDataEventsElem(nil), base.Events...)
			if difference := firstCalendarPageDifference(base, test.mutate(got)); difference != test.want {
				t.Fatalf("difference path=%q, want %q", difference, test.want)
			}
		})
	}
	if difference := firstCalendarPageDifference(base, base); difference != "" {
		t.Fatalf("equal page difference path=%q", difference)
	}
}

func TestSourceRefDifferenceReportsOnlyTheFirstFieldPath(t *testing.T) {
	base := []sourceRefProjection{{
		EntityType: "calendar_event", EntityID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EntityVersion: 1, LabelSnapshot: "fixture",
	}}
	tests := []struct {
		name, want string
		mutate     func([]sourceRefProjection) []sourceRefProjection
	}{
		{"count", "sourceRefs.length", func([]sourceRefProjection) []sourceRefProjection { return nil }},
		{"entity type", "sourceRefs[0].entityType", func(got []sourceRefProjection) []sourceRefProjection { got[0].EntityType = "other"; return got }},
		{"entity id", "sourceRefs[0].entityId", func(got []sourceRefProjection) []sourceRefProjection {
			got[0].EntityID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
			return got
		}},
		{"entity version", "sourceRefs[0].entityVersion", func(got []sourceRefProjection) []sourceRefProjection { got[0].EntityVersion = 2; return got }},
		{"label snapshot", "sourceRefs[0].labelSnapshot", func(got []sourceRefProjection) []sourceRefProjection { got[0].LabelSnapshot = "changed"; return got }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := append([]sourceRefProjection(nil), base...)
			if difference := firstSourceRefDifference(base, test.mutate(got)); difference != test.want {
				t.Fatalf("difference path=%q, want %q", difference, test.want)
			}
		})
	}
	if difference := firstSourceRefDifference(base, base); difference != "" {
		t.Fatalf("equal SourceRefs difference path=%q", difference)
	}
}

type sourceRefProjection struct {
	EntityType    string
	EntityID      uuid.UUID
	EntityVersion int64
	LabelSnapshot string
}

func firstCalendarPageDifference(want, got agentprotocol.CalendarReadData) string {
	if want.Window.Start != got.Window.Start {
		return "window.start"
	}
	if want.Window.End != got.Window.End {
		return "window.end"
	}
	if want.HasMore != got.HasMore {
		return "hasMore"
	}
	if (want.NextCursor == nil) != (got.NextCursor == nil) {
		return "nextCursor.presence"
	}
	if want.NextCursor != nil && *want.NextCursor != *got.NextCursor {
		return "nextCursor"
	}
	return firstCalendarEventsDifference(want.Events, got.Events)
}

func firstCalendarEventsDifference(want, got []agentprotocol.CalendarReadDataEventsElem) string {
	if len(want) != len(got) {
		return "events.length"
	}
	for index := range want {
		prefix := "events[" + strconv.Itoa(index) + "]."
		switch {
		case want[index].EndAt != got[index].EndAt:
			return prefix + "endAt"
		case want[index].ID != got[index].ID:
			return prefix + "id"
		case want[index].Kind != got[index].Kind:
			return prefix + "kind"
		case want[index].StartAt != got[index].StartAt:
			return prefix + "startAt"
		case want[index].Timezone != got[index].Timezone:
			return prefix + "timezone"
		case want[index].Title != got[index].Title:
			return prefix + "title"
		case want[index].Version != got[index].Version:
			return prefix + "version"
		}
	}
	return ""
}

func firstSourceRefDifference(want, got []sourceRefProjection) string {
	if len(want) != len(got) {
		return "sourceRefs.length"
	}
	for index := range want {
		prefix := "sourceRefs[" + strconv.Itoa(index) + "]."
		switch {
		case want[index].EntityType != got[index].EntityType:
			return prefix + "entityType"
		case want[index].EntityID != got[index].EntityID:
			return prefix + "entityId"
		case want[index].EntityVersion != got[index].EntityVersion:
			return prefix + "entityVersion"
		case want[index].LabelSnapshot != got[index].LabelSnapshot:
			return prefix + "labelSnapshot"
		}
	}
	return ""
}

func TestAgentIntegrationServiceBinding(t *testing.T) {
	if os.Getenv("DAYORDER_AGENT_SERVICE_CONFORMANCE_REQUIRED") != "1" {
		t.Skip("Service Binding conformance runs only through the owned integration orchestrator")
	}
	fixture := agenttest.Open(t)
	ctx := controlledServiceConformanceContext(t, 90*time.Second)
	stopServiceConformanceIfRequested(t, ctx)
	configuration, err := seedDatabase(ctx, fixture, "readonly-default")
	if err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}
	api := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	workerServices := agenttest.NewServices(t, fixture, config.DatabaseRoleWorker)
	reader, err := service.NewAgentCalendarReadService(workerServices.Runs, workerServices.Calendar)
	if err != nil {
		t.Fatal(err)
	}

	from, to := configuration.PagingWindow.Start, configuration.PagingWindow.End
	created, err := api.Runs.Create(ctx, service.MutationContext{
		UserID: fixture.UserB, DeviceID: fixture.DeviceB, MutationID: uuid.New(), RequestID: uuid.New(),
	}, agentprotocol.ReadonlyRunStart{
		Intent: "read the synthetic paging window", ExecutionMode: agentprotocol.ExecutionModeBackground,
		Scope:    agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
		Timezone: "UTC", ModelProfile: configuration.Profile,
	})
	if err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}
	runID := uuid.MustParse(string(created.RunID))
	token := uuid.New()
	if _, err = workerServices.Runs.Claim(ctx, fixture.UserB, runID, token, false); err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}
	actor := agentexecution.Actor{UserID: fixture.UserB, Token: token, Mode: agentprotocol.ExecutionModeBackground}
	binding, err := agentbinding.NewCalendar(actor, runID, reader)
	if err != nil {
		t.Fatal(err)
	}

	outside, invokeErr := binding.Invoke(ctx, map[string]any{
		"start": configuration.Window.Start, "end": configuration.Window.End, "limit": 25,
	}, agenttool.Context{RunID: runID.String(), CallID: "scope-outside"})
	stopServiceConformanceIfRequested(t, ctx)
	assertBoundCalendarFailure(t, outside, invokeErr, agentprotocol.ErrorCodePermissionDenied)

	first := invokeBoundCalendarPage(t, ctx, binding, runID, "paging-first", map[string]any{
		"start": from, "end": to, "limit": 25,
	})
	if len(first.Events) != 25 || !first.HasMore || first.NextCursor == nil {
		t.Fatalf("first Service Binding page count/hasMore/cursor = %d/%t/%t", len(first.Events), first.HasMore, first.NextCursor != nil)
	}
	second := invokeBoundCalendarPage(t, ctx, binding, runID, "paging-second", map[string]any{
		"start": from, "end": to, "limit": 25, "cursor": *first.NextCursor,
	})
	if len(second.Events) != 25 || second.HasMore || second.NextCursor != nil {
		t.Fatalf("second Service Binding page count/hasMore/cursor = %d/%t/%t", len(second.Events), second.HasMore, second.NextCursor != nil)
	}

	servicePages := []agentprotocol.CalendarReadData{first, second}
	serviceEvents := append(append([]agentprotocol.CalendarReadDataEventsElem(nil), first.Events...), second.Events...)
	identifiers := make(map[agentprotocol.UUID]struct{}, len(serviceEvents))
	for index, event := range serviceEvents {
		identifiers[event.ID] = struct{}{}
		if event.Version != 1 || len([]rune(event.Title)) != 240 || strings.Trim(event.Title, "<") != "" {
			t.Fatalf("Service Binding event %d version/title shape is invalid", index)
		}
		if index > 0 && string(serviceEvents[index-1].StartAt) >= string(event.StartAt) {
			t.Fatalf("Service Binding event order is not strictly increasing at %d", index)
		}
	}
	if len(identifiers) != 50 || string(serviceEvents[0].StartAt) != "2026-09-07T00:00:00Z" || string(serviceEvents[49].StartAt) != "2026-09-07T16:20:00Z" {
		t.Fatalf("Service Binding combined identity/time boundary = %d/%s/%s", len(identifiers), serviceEvents[0].StartAt, serviceEvents[49].StartAt)
	}

	tamperedCursor := string(*first.NextCursor)
	if tamperedCursor[0] == 'A' {
		tamperedCursor = "B" + tamperedCursor[1:]
	} else {
		tamperedCursor = "A" + tamperedCursor[1:]
	}
	tampered, invokeErr := binding.Invoke(ctx, map[string]any{
		"start": from, "end": to, "limit": 25, "cursor": tamperedCursor,
	}, agenttool.Context{RunID: runID.String(), CallID: "paging-tampered"})
	stopServiceConformanceIfRequested(t, ctx)
	assertBoundCalendarFailure(t, tampered, invokeErr, agentprotocol.ErrorCodeValidationFailed)

	oversized, invokeErr := binding.Invoke(ctx, map[string]any{
		"start": from, "end": to, "limit": 50,
	}, agenttool.Context{RunID: runID.String(), CallID: "paging-oversized"})
	stopServiceConformanceIfRequested(t, ctx)
	assertBoundCalendarFailure(t, oversized, invokeErr, agentprotocol.ErrorCodeValidationFailed)
	if oversized.Error == nil || !strings.Contains(oversized.Error.Message, "exceeds resultMaxBytes") {
		t.Fatalf("Service Binding oversized result message did not identify resultMaxBytes")
	}

	serviceSourceRefs := readServiceConformanceSourceRefs(t, ctx, fixture, fixture.UserB, runID)
	if len(serviceSourceRefs) != 50 {
		t.Fatalf("Service Binding source ref count=%d, want 50", len(serviceSourceRefs))
	}
	current, err := api.Runs.Get(ctx, fixture.UserB, runID)
	if err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}
	if _, err = api.Runs.Cancel(ctx, service.MutationContext{
		UserID: fixture.UserB, DeviceID: fixture.DeviceB, MutationID: uuid.New(), RequestID: uuid.New(),
	}, runID, int64(current.Version)); err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}

	handler := newServiceConformanceRouter(t, fixture, api, configuration)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	account := configuration.Accounts[1]
	integrationHTTPJSON(t, ctx, client, http.MethodPost, server.URL+"/api/v1/auth/login", server.URL, map[string]string{
		"email": account.Email, "password": account.Password,
	}, nil, http.StatusOK, nil)
	start := agentprotocol.ReadonlyRunStart{
		Intent: "same-database HTTP paging conformance", ExecutionMode: agentprotocol.ExecutionModeForeground,
		Scope: agentprotocol.AgentScope{
			Domains: []string{"calendar"}, From: &from, To: &to,
		},
		Timezone: "UTC", ModelProfile: configuration.Profile,
	}
	requestHeaders := func() map[string]string {
		return map[string]string{"Idempotency-Key": uuid.NewString(), "X-Device-ID": account.DeviceID.String()}
	}
	var foregroundCreated agentprotocol.ReadonlyRunView
	integrationHTTPJSON(t, ctx, client, http.MethodPost, server.URL+"/api/v1/agent/runs", server.URL, start, requestHeaders(), http.StatusCreated, &foregroundCreated)
	if foregroundCreated.Status != agentprotocol.ReadonlyRunViewStatusReady || foregroundCreated.ExecutionMode != agentprotocol.ExecutionModeForeground {
		t.Fatalf("same-database HTTP Run status/mode=%s/%s", foregroundCreated.Status, foregroundCreated.ExecutionMode)
	}
	httpRunID := string(foregroundCreated.RunID)
	httpFirst := invokeHTTPCalendarPage(t, ctx, client, server.URL, server.URL, account.DeviceID, httpRunID, "http-page-1", agentprotocol.CalendarReadInput{
		Start: agentprotocol.DateTime(from), End: agentprotocol.DateTime(to), Limit: 25,
	})
	if difference := firstCalendarPageDifference(servicePages[0], httpFirst); difference != "" {
		t.Fatalf("same-database page 1 difference at %s", difference)
	}
	if httpFirst.NextCursor == nil {
		t.Fatal("same-database HTTP page 1 cursor missing")
	}
	httpCursor := agentprotocol.CalendarCursor(*httpFirst.NextCursor)
	httpSecond := invokeHTTPCalendarPage(t, ctx, client, server.URL, server.URL, account.DeviceID, httpRunID, "http-page-2", agentprotocol.CalendarReadInput{
		Start: agentprotocol.DateTime(from), End: agentprotocol.DateTime(to), Limit: 25, Cursor: &httpCursor,
	})
	if difference := firstCalendarPageDifference(servicePages[1], httpSecond); difference != "" {
		t.Fatalf("same-database page 2 difference at %s", difference)
	}
	httpEvents := append(append([]agentprotocol.CalendarReadDataEventsElem(nil), httpFirst.Events...), httpSecond.Events...)
	if difference := firstCalendarEventsDifference(serviceEvents, httpEvents); difference != "" {
		t.Fatalf("same-database merged calendar difference at %s", difference)
	}
	httpSourceRefs := readServiceConformanceSourceRefs(t, ctx, fixture, fixture.UserB, uuid.MustParse(httpRunID))
	if difference := firstSourceRefDifference(serviceSourceRefs, httpSourceRefs); difference != "" {
		t.Fatalf("same-database SourceRef difference at %s", difference)
	}

	var latest agentprotocol.ReadonlyRunView
	integrationHTTPJSON(t, ctx, client, http.MethodGet, server.URL+"/api/v1/agent/runs/"+httpRunID, server.URL, nil, map[string]string{
		"X-Device-ID": account.DeviceID.String(),
	}, http.StatusOK, &latest)
	var completed agentprotocol.ReadonlyRunView
	integrationHTTPJSON(t, ctx, client, http.MethodPost, server.URL+"/api/v1/agent/runs/"+httpRunID+"/finish", server.URL, agentprotocol.ReadonlyRunFinish{
		Phase: agentprotocol.ReadonlyRunFinishPhaseCompleted, Summary: "Read 50 calendar events across two pages", Steps: []agentprotocol.ReadonlyRunFinishStepsElem{},
	}, map[string]string{
		"Idempotency-Key": uuid.NewString(),
		"If-Match":        `"` + strconv.Itoa(latest.Version) + `"`,
		"X-Device-ID":     account.DeviceID.String(),
	}, http.StatusOK, &completed)
	if completed.Status != agentprotocol.ReadonlyRunViewStatusCompleted || completed.ResultOrigin != agentprotocol.ReadonlyRunViewResultOriginClientReported {
		t.Fatalf("same-database HTTP terminal status/origin=%s/%s", completed.Status, completed.ResultOrigin)
	}
}

func newServiceConformanceRouter(t testing.TB, fixture *agenttest.Database, services agenttest.Services, configuration fixtureConfig) http.Handler {
	t.Helper()
	reader, err := service.NewAgentCalendarReadService(services.Runs, services.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	accountsRepository, err := postgresstore.NewAccountRepository(fixture.API)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := service.NewAccountService(accountsRepository)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.NewSessionService(accountsRepository, accountsRepository, integrationHMACKey)
	if err != nil {
		t.Fatal(err)
	}
	devices, err := service.NewDeviceService(postgresstore.NewDeviceRepository(), services.Transactor, services.Audit)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: agentprotocol.CalendarReadInput{
		Start: agentprotocol.DateTime(configuration.PagingWindow.Start), End: agentprotocol.DateTime(configuration.PagingWindow.End), Limit: 25,
	}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := integrationTools()
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := agentgateway.New(agentgateway.Config{
		Runs: services.Runs, Profiles: []agentgateway.Profile{{ID: configuration.Profile, Model: "fixture-calendar-overview", Adapter: adapter}}, Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewAgentIntegrationRouter(httpapi.RouterOptions{
		Accounts: accounts, Sessions: sessions, Calendar: services.Calendar, Devices: devices,
	}, httpapi.AgentIntegrationOptions{
		Environment: config.Test, Runs: services.Runs, Calendar: reader, Gateway: gateway,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func invokeHTTPCalendarPage(t testing.TB, ctx context.Context, client *http.Client, baseURL, origin string, deviceID uuid.UUID, runID, callID string, input agentprotocol.CalendarReadInput) agentprotocol.CalendarReadData {
	t.Helper()
	var result agentprotocol.ToolResult
	integrationHTTPJSON(t, ctx, client, http.MethodPost, baseURL+"/api/v1/agent/runs/"+runID+"/tools/calendar-read", origin, agentprotocol.CalendarReadRequest{
		CallID: callID, Input: input,
	}, map[string]string{
		"Idempotency-Key": uuid.NewString(), "X-Device-ID": deviceID.String(),
	}, http.StatusOK, &result)
	if !result.Ok {
		code := agentprotocol.ErrorCode("")
		if result.Error != nil {
			code = result.Error.Code
		}
		t.Fatalf("same-database HTTP calendar result ok=%t error_code=%s", result.Ok, code)
	}
	raw, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data agentprotocol.CalendarReadData
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func readServiceConformanceSourceRefs(t testing.TB, ctx context.Context, fixture *agenttest.Database, userID, runID uuid.UUID) []sourceRefProjection {
	t.Helper()
	rows, err := fixture.Migrator.Query(ctx, `
SELECT entity_type, entity_id, entity_version, label_snapshot
FROM dayorder.agent_source_refs
WHERE user_id = $1 AND run_id = $2
ORDER BY entity_type, entity_id, entity_version, label_snapshot
`, userID, runID)
	if err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}
	defer rows.Close()
	refs := make([]sourceRefProjection, 0, 50)
	for rows.Next() {
		var ref sourceRefProjection
		if err = rows.Scan(&ref.EntityType, &ref.EntityID, &ref.EntityVersion, &ref.LabelSnapshot); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	if err = rows.Err(); err != nil {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatal(err)
	}
	return refs
}

func invokeBoundCalendarPage(t testing.TB, ctx context.Context, binding agenttool.Binding, runID uuid.UUID, callID string, input map[string]any) agentprotocol.CalendarReadData {
	t.Helper()
	result, err := binding.Invoke(ctx, input, agenttool.Context{RunID: runID.String(), CallID: callID})
	if err != nil || !result.Ok {
		stopServiceConformanceIfRequested(t, ctx)
		t.Fatalf("Service Binding call %s failed (ok=%t, error=%v)", callID, result.Ok, err)
	}
	raw, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data agentprotocol.CalendarReadData
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

type serviceConformanceStopKey struct{}

type serviceConformanceStop struct {
	requested atomic.Bool
}

func controlledServiceConformanceContext(t testing.TB, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	if os.Getenv("DAYORDER_AGENT_SERVICE_CONFORMANCE_CONTROLLED") != "1" {
		return ctx
	}
	stop := &serviceConformanceStop{}
	ctx = context.WithValue(ctx, serviceConformanceStopKey{}, stop)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if scanner.Text() != "stop" {
				continue
			}
			stop.requested.Store(true)
			cancel()
			return
		}
		stop.requested.Store(true)
		cancel()
	}()
	return ctx
}

func stopServiceConformanceIfRequested(t testing.TB, ctx context.Context) {
	t.Helper()
	stop, _ := ctx.Value(serviceConformanceStopKey{}).(*serviceConformanceStop)
	if stop != nil && stop.requested.Load() {
		t.Skip("Service Binding conformance stopped cooperatively by the orchestrator")
	}
}

func assertBoundCalendarFailure(t testing.TB, result agentprotocol.ToolResult, err error, code agentprotocol.ErrorCode) {
	t.Helper()
	if err != nil || result.Ok || result.Error == nil || result.Error.Code != code {
		gotCode := agentprotocol.ErrorCode("")
		if result.Error != nil {
			gotCode = result.Error.Code
		}
		t.Fatalf("Service Binding failure ok/code/error = %t/%s/%v, want false/%s/nil", result.Ok, gotCode, err, code)
	}
}

func TestAgentIntegrationExternalReadonlyBinding(t *testing.T) {
	if os.Getenv("DAYORDER_AGENT_EXTERNAL_CONFORMANCE_REQUIRED") != "1" {
		t.Skip("external conformance runs only through the owned integration orchestrator")
	}
	apiURL := requireLoopbackIntegrationURL(t, "DAYORDER_AGENT_HARNESS_URL")
	origin := requireLoopbackIntegrationURL(t, "DAYORDER_AGENT_HARNESS_ORIGIN")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	var configuration fixtureConfig
	integrationHTTPJSON(t, t.Context(), client, http.MethodGet, apiURL+"/__test/config", origin, nil, nil, http.StatusOK, &configuration)
	if len(configuration.Accounts) != 2 {
		t.Fatalf("external fixture accounts = %d, want 2", len(configuration.Accounts))
	}
	account := configuration.Accounts[1]
	integrationHTTPJSON(t, t.Context(), client, http.MethodPost, apiURL+"/api/v1/auth/login", origin, map[string]string{
		"email": account.Email, "password": account.Password,
	}, nil, http.StatusOK, nil)

	start := agentprotocol.ReadonlyRunStart{
		Intent: "external HTTP readonly binding", ExecutionMode: agentprotocol.ExecutionModeForeground,
		Scope: agentprotocol.AgentScope{
			Domains: []string{"calendar"}, From: &configuration.Window.Start, To: &configuration.Window.End,
		},
		Timezone: "UTC", ModelProfile: configuration.Profile,
	}
	headers := map[string]string{
		"Idempotency-Key": uuid.NewString(),
		"X-Device-ID":     account.DeviceID.String(),
	}
	var created agentprotocol.ReadonlyRunView
	integrationHTTPJSON(t, t.Context(), client, http.MethodPost, apiURL+"/api/v1/agent/runs", origin, start, headers, http.StatusCreated, &created)
	if created.Status != agentprotocol.ReadonlyRunViewStatusReady || created.ExecutionMode != agentprotocol.ExecutionModeForeground {
		t.Fatalf("external Run status/mode = %s/%s", created.Status, created.ExecutionMode)
	}
	runID := string(created.RunID)
	var result agentprotocol.ToolResult
	integrationHTTPJSON(t, t.Context(), client, http.MethodPost, apiURL+"/api/v1/agent/runs/"+runID+"/tools/calendar-read", origin, agentprotocol.CalendarReadRequest{
		CallID: "external-calendar", Input: agentprotocol.CalendarReadInput{
			Start: agentprotocol.DateTime(configuration.Window.Start), End: agentprotocol.DateTime(configuration.Window.End), Limit: 25,
		},
	}, map[string]string{
		"Idempotency-Key": uuid.NewString(),
		"X-Device-ID":     account.DeviceID.String(),
	}, http.StatusOK, &result)
	if !result.Ok {
		t.Fatal("external calendar binding returned a failed ToolResult")
	}
	raw, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data agentprotocol.CalendarReadData
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Events) != 1 || data.Events[0].Version != 1 || string(data.Events[0].ID) == "" {
		t.Fatalf("external calendar event count/version/identity = %d/%d/%t", len(data.Events), firstCalendarVersion(data), firstCalendarIDPresent(data))
	}

	var diagnostic testRunState
	integrationHTTPJSON(t, t.Context(), client, http.MethodGet, apiURL+"/__test/runs/"+runID, origin, nil, nil, http.StatusOK, &diagnostic)
	if diagnostic.OutboxCount != 0 || len(diagnostic.SourceRefs) != 1 || diagnostic.SourceRefs[0].EntityID != string(data.Events[0].ID) || diagnostic.SourceRefs[0].EntityVersion != 1 {
		t.Fatalf("external diagnostics outbox/source refs = %d/%d", diagnostic.OutboxCount, len(diagnostic.SourceRefs))
	}

	var latest agentprotocol.ReadonlyRunView
	integrationHTTPJSON(t, t.Context(), client, http.MethodGet, apiURL+"/api/v1/agent/runs/"+runID, origin, nil, map[string]string{
		"X-Device-ID": account.DeviceID.String(),
	}, http.StatusOK, &latest)
	var stopped agentprotocol.ReadonlyRunView
	integrationHTTPJSON(t, t.Context(), client, http.MethodPost, apiURL+"/api/v1/agent/runs/"+runID+"/cancel", origin, struct{}{}, map[string]string{
		"Idempotency-Key": uuid.NewString(),
		"If-Match":        `"` + strconv.Itoa(latest.Version) + `"`,
		"X-Device-ID":     account.DeviceID.String(),
	}, http.StatusOK, &stopped)
	if stopped.Status != agentprotocol.ReadonlyRunViewStatusStopped {
		t.Fatalf("external cancelled Run status = %s", stopped.Status)
	}
}

func requireLoopbackIntegrationURL(t testing.TB, environmentKey string) string {
	t.Helper()
	value := strings.TrimSuffix(strings.TrimSpace(os.Getenv(environmentKey)), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatalf("%s must be an explicit loopback HTTP origin", environmentKey)
	}
	return value
}

func TestIntegrationHTTPJSONCancelsResponseReadWithSuppliedContext(t *testing.T) {
	responseStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, `{"started":`)
		response.(http.Flusher).Flush()
		close(responseStarted)
		select {
		case <-request.Context().Done():
			close(requestCanceled)
		case <-releaseHandler:
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		server.CloseClientConnections()
		close(releaseHandler)
	})
	result := make(chan error, 1)
	go func() {
		result <- doIntegrationHTTPJSON(ctx, server.Client(), http.MethodGet, server.URL, server.URL, nil, nil, http.StatusOK, &struct{}{})
	}()

	select {
	case <-responseStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP response did not start")
	}
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("HTTP response read did not stop after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HTTP response read error is context canceled=%t", errors.Is(err, context.Canceled))
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe request context cancellation")
	}
}

func integrationHTTPJSON(t testing.TB, ctx context.Context, client *http.Client, method, target, origin string, input any, headers map[string]string, wantStatus int, output any) {
	t.Helper()
	if err := doIntegrationHTTPJSON(ctx, client, method, target, origin, input, headers, wantStatus, output); err != nil {
		t.Fatalf("integration HTTP %s failed: %s", method, err)
	}
}

func doIntegrationHTTPJSON(ctx context.Context, client *http.Client, method, target, origin string, input any, headers map[string]string, wantStatus int, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return errors.New("request JSON encoding failed")
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return errors.New("request construction failed")
	}
	request.Header.Set("Origin", origin)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.New("request transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		return fmt.Errorf("unexpected status=%d want=%d", response.StatusCode, wantStatus)
	}
	limited := io.LimitReader(response.Body, 1_048_577)
	raw, err := io.ReadAll(limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.New("response body read failed")
	}
	if len(raw) > 1_048_576 {
		return errors.New("JSON response exceeded 1 MiB")
	}
	if output != nil {
		if err = json.Unmarshal(raw, output); err != nil {
			return errors.New("response was not valid JSON")
		}
	}
	return nil
}

func firstCalendarVersion(data agentprotocol.CalendarReadData) int {
	if len(data.Events) == 0 {
		return 0
	}
	return data.Events[0].Version
}

func firstCalendarIDPresent(data agentprotocol.CalendarReadData) bool {
	return len(data.Events) > 0 && data.Events[0].ID != ""
}
