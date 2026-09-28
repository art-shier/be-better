package agentprovider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strconv"
	"strings"

	"dayorder.local/api/internal/agentprotocol"
)

type Fake struct {
	window             agentprotocol.CalendarReadInput
	recoverToolTimeout bool
	fault              agentprotocol.ErrorCode
}

func NewFake(config FakeConfig) (*Fake, error) {
	if err := agentprotocol.Validate(agentprotocol.DefinitionCalendarReadInput, config.Window); err != nil {
		return nil, errors.New("invalid Fake window")
	}
	fault, ok := fakeFaultCode(config.Fault)
	if !ok {
		return nil, errors.New("invalid Fake fault")
	}
	window := config.Window
	if config.Window.Cursor != nil {
		cursor := *config.Window.Cursor
		window.Cursor = &cursor
	}
	return &Fake{window: window, recoverToolTimeout: config.RecoverToolTimeout, fault: fault}, nil
}

func (fake *Fake) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest, _ TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		if ctx.Err() != nil {
			yield(agentprotocol.ProviderEvent{}, &ProviderError{Code: agentprotocol.ErrorCodeCancelled, Retryable: false})
			return
		}
		if fake.fault != "" {
			yield(agentprotocol.ProviderEvent{}, &ProviderError{Code: fake.fault, Retryable: fake.fault == agentprotocol.ErrorCodeProviderUnavailable || fake.fault == agentprotocol.ErrorCodeProviderRateLimited || fake.fault == agentprotocol.ErrorCodeTimeout})
			return
		}
		if err := ValidateHistory(request); err != nil {
			yield(agentprotocol.ProviderEvent{}, err)
			return
		}
		exchanges := fakeExchanges(request.Messages)
		if len(exchanges) > 3 {
			yield(agentprotocol.ProviderEvent{}, protocolError())
			return
		}
		for index, exchange := range exchanges {
			if err := fake.validateExchange(index, exchange); err != nil {
				yield(agentprotocol.ProviderEvent{}, err)
				return
			}
		}

		if len(exchanges) == 3 {
			last := exchanges[2].result
			if !last.Ok && last.Error != nil && last.Error.Code == agentprotocol.ErrorCodeTimeout && fake.recoverToolTimeout {
				yieldFakeText(yield, "未能完成查询。")
				return
			}
			calendar, err := fakeCalendarData(last)
			if err != nil {
				yield(agentprotocol.ProviderEvent{}, err)
				return
			}
			titles := make([]string, len(calendar.Events))
			for index, event := range calendar.Events {
				titles[index] = event.Title
			}
			text := strconv.Itoa(len(titles)) + " calendar events."
			if len(titles) > 0 {
				text = strconv.Itoa(len(titles)) + " calendar events: " + strings.Join(titles, "; ") + "."
			}
			yieldFakeText(yield, text)
			return
		}

		calls := []agentprotocol.ToolCall{
			{ID: "fake-call-1", Name: "skill_list", Input: agentprotocol.ToolCallInput{}},
			{ID: "fake-call-2", Name: "skill_load", Input: agentprotocol.ToolCallInput{"name": "calendar-overview"}},
			{ID: "fake-call-3", Name: "dayorder.calendar.read", Input: fakeWindowInput(fake.window)},
		}
		call := calls[len(exchanges)]
		if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: &call}, nil) {
			return
		}
		yield(fakeCompleted(agentprotocol.ProviderEventStopReasonToolUse), nil)
	}
}

type fakeExchange struct {
	call   agentprotocol.ToolCall
	result agentprotocol.ToolResult
}

func fakeExchanges(messages []agentprotocol.Message) []fakeExchange {
	var exchanges []fakeExchange
	var pending *agentprotocol.ToolCall
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type == agentprotocol.ContentBlockTypeToolCall && block.ToolCall != nil {
				call := *block.ToolCall
				pending = &call
			}
			if block.Type == agentprotocol.ContentBlockTypeToolResult && block.ToolResult != nil && pending != nil {
				exchanges = append(exchanges, fakeExchange{call: *pending, result: *block.ToolResult})
				pending = nil
			}
		}
	}
	return exchanges
}

func (fake *Fake) validateExchange(index int, exchange fakeExchange) error {
	expectedNames := []string{"skill_list", "skill_load", "dayorder.calendar.read"}
	expectedIDs := []string{"fake-call-1", "fake-call-2", "fake-call-3"}
	if index >= len(expectedNames) || exchange.call.Name != expectedNames[index] || exchange.call.ID != expectedIDs[index] {
		return protocolError()
	}
	switch index {
	case 0:
		if len(exchange.call.Input) != 0 || !exchange.result.Ok || !fakeHasCalendarSkill(exchange.result) {
			return protocolError()
		}
	case 1:
		if !reflect.DeepEqual(exchange.call.Input, agentprotocol.ToolCallInput{"name": "calendar-overview"}) || !exchange.result.Ok || !fakeHasCalendarTool(exchange.result) {
			return protocolError()
		}
	case 2:
		if !equalJSON(exchange.call.Input, fakeWindowInput(fake.window)) {
			return protocolError()
		}
		if !exchange.result.Ok && !(fake.recoverToolTimeout && exchange.result.Error != nil && exchange.result.Error.Code == agentprotocol.ErrorCodeTimeout) {
			return protocolError()
		}
	}
	return nil
}

func fakeHasCalendarSkill(result agentprotocol.ToolResult) bool {
	raw, err := json.Marshal(result.Data["skills"])
	if err != nil {
		return false
	}
	var skills []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &skills) != nil {
		return false
	}
	for _, skill := range skills {
		if skill.Name == "calendar-overview" {
			return true
		}
	}
	return false
}

func fakeHasCalendarTool(result agentprotocol.ToolResult) bool {
	raw, err := json.Marshal(result.Data["activation"])
	if err != nil {
		return false
	}
	var activation struct {
		ActiveToolIDs []string `json:"activeToolIds"`
	}
	if json.Unmarshal(raw, &activation) != nil {
		return false
	}
	for _, id := range activation.ActiveToolIDs {
		if id == "dayorder.calendar.read" {
			return true
		}
	}
	return false
}

func fakeCalendarData(result agentprotocol.ToolResult) (agentprotocol.CalendarReadData, error) {
	if !result.Ok {
		return agentprotocol.CalendarReadData{}, protocolError()
	}
	raw, err := json.Marshal(result.Data)
	if err != nil {
		return agentprotocol.CalendarReadData{}, protocolError()
	}
	var data agentprotocol.CalendarReadData
	if err := json.Unmarshal(raw, &data); err != nil || agentprotocol.Validate(agentprotocol.DefinitionCalendarReadData, data) != nil {
		return agentprotocol.CalendarReadData{}, protocolError()
	}
	return data, nil
}

func fakeWindowInput(window agentprotocol.CalendarReadInput) agentprotocol.ToolCallInput {
	raw, _ := json.Marshal(window)
	var input agentprotocol.ToolCallInput
	_ = json.Unmarshal(raw, &input)
	return input
}

func equalJSON(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	var leftValue, rightValue any
	if json.Unmarshal(leftRaw, &leftValue) != nil || json.Unmarshal(rightRaw, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func yieldFakeText(yield func(agentprotocol.ProviderEvent, error) bool, text string) {
	if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
		return
	}
	yield(fakeCompleted(agentprotocol.ProviderEventStopReasonEndTurn), nil)
}

func fakeCompleted(reason agentprotocol.ProviderEventStopReason) agentprotocol.ProviderEvent {
	return agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: &reason, Usage: &agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}
}

func fakeFaultCode(value string) (agentprotocol.ErrorCode, bool) {
	if value == "" {
		return "", true
	}
	code := agentprotocol.ErrorCode(value)
	switch code {
	case agentprotocol.ErrorCodeProviderUnavailable, agentprotocol.ErrorCodeProviderRateLimited, agentprotocol.ErrorCodeTimeout, agentprotocol.ErrorCodeProtocolIncompatible, agentprotocol.ErrorCodeCancelled:
		return code, true
	default:
		return "", false
	}
}

var _ Adapter = (*Fake)(nil)
