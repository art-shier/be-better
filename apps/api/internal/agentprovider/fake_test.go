package agentprovider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttool"
)

func TestFakeFollowsResolvedResultsAndUsesActualCalendarTitles(t *testing.T) {
	window := agentprotocol.CalendarReadInput{Start: "2026-09-05T00:00:00Z", End: "2026-09-06T00:00:00Z", Limit: 20}
	fake, err := NewFake(FakeConfig{Window: window})
	if err != nil {
		t.Fatal(err)
	}
	request := fakeTurnRequest(t)

	assertFakeToolTurn(t, fake, request, "fake-call-1", "skill_list", agentprotocol.ToolCallInput{})
	request.Messages = appendToolExchange(request.Messages, "fake-call-1", "skill_list", agentprotocol.ToolCallInput{}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"skills": []agentprotocol.SkillDescriptor{{Name: "calendar-overview"}}}})
	assertFakeToolTurn(t, fake, request, "fake-call-2", "skill_load", agentprotocol.ToolCallInput{"name": "calendar-overview"})

	request.Messages = appendToolExchange(request.Messages, "fake-call-2", "skill_load", agentprotocol.ToolCallInput{"name": "calendar-overview"}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"activation": map[string]any{"activeToolIds": []any{"dayorder.calendar.read"}}}})
	assertFakeToolTurn(t, fake, request, "fake-call-3", "dayorder.calendar.read", agentprotocol.ToolCallInput{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z", "limit": float64(20)})

	request.Messages = appendToolExchange(request.Messages, "fake-call-3", "dayorder.calendar.read", agentprotocol.ToolCallInput{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z", "limit": 20}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{
		"events": []any{
			map[string]any{"id": "1f29b0c9-9d6b-4a29-91f3-20bf4dd9b499", "title": "Planning", "startAt": "2026-09-05T09:00:00Z", "endAt": "2026-09-05T10:00:00Z", "timezone": "UTC", "kind": "meeting", "version": 1},
			map[string]any{"id": "25a413d4-dd52-45a9-95d1-ce92a15076d1", "title": "Review", "startAt": "2026-09-05T11:00:00Z", "endAt": "2026-09-05T12:00:00Z", "timezone": "UTC", "kind": "meeting", "version": 2},
		},
		"window": map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"}, "hasMore": false, "nextCursor": nil,
	}})
	events, failures := collectProviderSequence(fake.Stream(context.Background(), request, TurnOptions{}))
	if len(failures) != 0 || len(events) != 2 || events[0].Type != agentprotocol.ProviderEventTypeTextDelta || events[0].Text == nil || !strings.Contains(*events[0].Text, "2") || !strings.Contains(*events[0].Text, "Planning") || !strings.Contains(*events[0].Text, "Review") {
		t.Fatalf("events = %#v, failures = %#v", events, failures)
	}
	assertUsage15(t, events[1], agentprotocol.ProviderEventStopReasonEndTurn)
}

func TestFakeRejectsOutOfOrderResultInsteadOfAdvancing(t *testing.T) {
	fake, err := NewFake(FakeConfig{Window: agentprotocol.CalendarReadInput{Start: "2026-09-05T00:00:00Z", End: "2026-09-06T00:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	request := fakeTurnRequest(t)
	request.Messages = appendToolExchange(request.Messages, "wrong", "skill_load", agentprotocol.ToolCallInput{"name": "calendar-overview"}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"activation": map[string]any{"activeToolIds": []any{"dayorder.calendar.read"}}}})
	events, failures := collectProviderSequence(fake.Stream(context.Background(), request, TurnOptions{}))
	if len(events) != 0 || len(failures) != 1 {
		t.Fatalf("events = %#v, failures = %#v", events, failures)
	}
}

func TestFakeRecoversOnlyCalendarTimeoutWithSafeText(t *testing.T) {
	window := agentprotocol.CalendarReadInput{Start: "2026-09-05T00:00:00Z", End: "2026-09-06T00:00:00Z"}
	fake, err := NewFake(FakeConfig{Window: window, RecoverToolTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	request := fakeRequestThroughCalendar(t, window, agentprotocol.ToolResult{Ok: false, Error: &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "private timeout detail", Retryable: false}})
	events, failures := collectProviderSequence(fake.Stream(context.Background(), request, TurnOptions{}))
	if len(failures) != 0 || len(events) != 2 || events[0].Text == nil || *events[0].Text != "未能完成查询。" || strings.Contains(*events[0].Text, "private") {
		t.Fatalf("events = %#v, failures = %#v", events, failures)
	}
	assertUsage15(t, events[1], agentprotocol.ProviderEventStopReasonEndTurn)
}

func TestFakeFaultProducesClassifiedError(t *testing.T) {
	fake, err := NewFake(FakeConfig{Window: agentprotocol.CalendarReadInput{Start: "2026-09-05T00:00:00Z", End: "2026-09-06T00:00:00Z"}, Fault: "provider_unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	events, failures := collectProviderSequence(fake.Stream(context.Background(), fakeTurnRequest(t), TurnOptions{}))
	var providerFailure *ProviderError
	if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &providerFailure) || providerFailure.Code != agentprotocol.ErrorCodeProviderUnavailable || !providerFailure.Retryable {
		t.Fatalf("events = %#v, failures = %#v", events, failures)
	}
}

func TestNewFakeCopiesConfiguredWindow(t *testing.T) {
	cursor := agentprotocol.CalendarCursor("cursor-original")
	window := agentprotocol.CalendarReadInput{Start: "2026-09-05T00:00:00Z", End: "2026-09-06T00:00:00Z", Cursor: &cursor}
	fake, err := NewFake(FakeConfig{Window: window})
	if err != nil {
		t.Fatal(err)
	}
	cursor = "cursor-mutated"
	request := fakeTurnRequest(t)
	request.Messages = appendToolExchange(request.Messages, "fake-call-1", "skill_list", agentprotocol.ToolCallInput{}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"skills": []agentprotocol.SkillDescriptor{{Name: "calendar-overview"}}}})
	request.Messages = appendToolExchange(request.Messages, "fake-call-2", "skill_load", agentprotocol.ToolCallInput{"name": "calendar-overview"}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"activation": agentprotocol.SkillActivation{ActiveToolIds: []string{"dayorder.calendar.read"}}}})
	events, failures := collectProviderSequence(fake.Stream(context.Background(), request, TurnOptions{}))
	if len(failures) != 0 || len(events) != 2 || events[0].Call == nil || events[0].Call.Input["cursor"] != "cursor-original" {
		t.Fatalf("events=%#v failures=%#v", events, failures)
	}
}

func assertFakeToolTurn(t *testing.T, fake *Fake, request agentprotocol.ModelTurnRequest, id, name string, input agentprotocol.ToolCallInput) {
	t.Helper()
	events, failures := collectProviderSequence(fake.Stream(context.Background(), request, TurnOptions{}))
	if len(failures) != 0 || len(events) != 2 || events[0].Call == nil || events[0].Call.ID != id || events[0].Call.Name != name || !reflect.DeepEqual(events[0].Call.Input, input) {
		t.Fatalf("events = %#v, failures = %#v", events, failures)
	}
	assertUsage15(t, events[1], agentprotocol.ProviderEventStopReasonToolUse)
}

func assertUsage15(t *testing.T, event agentprotocol.ProviderEvent, reason agentprotocol.ProviderEventStopReason) {
	t.Helper()
	want := &agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	if event.Type != agentprotocol.ProviderEventTypeCompleted || !reflect.DeepEqual(event.StopReason, stopReason(reason)) || !reflect.DeepEqual(event.Usage, want) {
		t.Fatalf("completed = %#v", event)
	}
}

func fakeTurnRequest(t *testing.T) agentprotocol.ModelTurnRequest {
	t.Helper()
	bindings := agentskill.MetaBindings(nil, agentprotocol.CapabilitySnapshot{}, nil, agenttool.Policy{})
	calendar, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return agentprotocol.ModelTurnRequest{ProtocolVersion: "2.0", RunID: "run-fake", TurnID: "turn-fake", ModelProfile: "fake", Messages: []agentprotocol.Message{textMessage(agentprotocol.MessageRoleUser, "calendar")}, Tools: []agentprotocol.ToolSpec{bindings[0].Spec(), bindings[1].Spec(), calendar}}
}

func appendToolExchange(messages []agentprotocol.Message, id, name string, input agentprotocol.ToolCallInput, result agentprotocol.ToolResult) []agentprotocol.Message {
	return append(messages,
		agentprotocol.Message{Role: agentprotocol.MessageRoleAssistant, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeToolCall, ToolCall: &agentprotocol.ToolCall{ID: id, Name: name, Input: input}}}},
		agentprotocol.Message{Role: agentprotocol.MessageRoleTool, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeToolResult, ToolResult: &result}}},
	)
}

func fakeRequestThroughCalendar(t *testing.T, window agentprotocol.CalendarReadInput, calendarResult agentprotocol.ToolResult) agentprotocol.ModelTurnRequest {
	t.Helper()
	request := fakeTurnRequest(t)
	request.Messages = appendToolExchange(request.Messages, "fake-call-1", "skill_list", agentprotocol.ToolCallInput{}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"skills": []agentprotocol.SkillDescriptor{{Name: "calendar-overview"}}}})
	request.Messages = appendToolExchange(request.Messages, "fake-call-2", "skill_load", agentprotocol.ToolCallInput{"name": "calendar-overview"}, agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{"activation": agentprotocol.SkillActivation{ActiveToolIds: []string{"dayorder.calendar.read"}}}})
	rawInput := agentprotocol.ToolCallInput{"start": string(window.Start), "end": string(window.End)}
	if window.Limit != 0 {
		rawInput["limit"] = window.Limit
	}
	request.Messages = appendToolExchange(request.Messages, "fake-call-3", "dayorder.calendar.read", rawInput, calendarResult)
	return request
}
