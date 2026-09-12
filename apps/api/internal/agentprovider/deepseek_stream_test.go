package agentprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

type oneByteReader struct{ source io.Reader }

func (reader oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return reader.source.Read(buffer)
}

func TestParseDeepSeekStreamReassemblesToolCallAndCombinedUsageTail(t *testing.T) {
	events, errors := collectProviderSequence(ParseDeepSeekStream(context.Background(), oneByteReader{source: strings.NewReader(readDeepSeekFixture(t, "tool-turn.sse"))}))
	if len(errors) != 0 {
		t.Fatalf("errors = %v", errors)
	}
	want := []agentprotocol.ProviderEvent{
		{Type: agentprotocol.ProviderEventTypeToolCall, Call: &agentprotocol.ToolCall{
			ID: "c1", Name: "dayorder.calendar.read",
			Input: agentprotocol.ToolCallInput{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"},
		}},
		{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: stopReason(agentprotocol.ProviderEventStopReasonToolUse), Usage: &agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestParseDeepSeekStreamAcceptsCompatibilityUsageOnlyTail(t *testing.T) {
	events, errors := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\ndata: [DONE]\n\n")))
	if len(errors) != 0 {
		t.Fatalf("errors = %v", errors)
	}
	want := []agentprotocol.ProviderEvent{
		{Type: agentprotocol.ProviderEventTypeTextDelta, Text: stringPointer("hello")},
		{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: stopReason(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestParseDeepSeekStreamKeepsLatestMatchingUsageSnapshotWithoutSumming(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\ndata: [DONE]\n\n"
	events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(stream)))
	want := []agentprotocol.ProviderEvent{{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: stopReason(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}}}
	if len(failures) != 0 || !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%#v failures=%#v", events, failures)
	}
}

func TestParseDeepSeekStreamRejectsMalformedOrIncompleteTurnsWithoutCompleted(t *testing.T) {
	tests := map[string]string{
		"missing usage":      "missing-usage.sse",
		"missing done":       "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n",
		"second finish":      "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		"multiple choices":   "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null},{\"index\":1,\"delta\":{},\"finish_reason\":null}]}\n\n",
		"multiple calls":     "multiple-calls.sse",
		"truncated json":     "data: {\"choices\":[\n\n",
		"unknown tool alias": "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"unknown_tool\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n",
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			stream := fixture
			if strings.HasSuffix(fixture, ".sse") {
				stream = readDeepSeekFixture(t, fixture)
			}
			events, errors := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(stream)))
			if len(errors) == 0 {
				t.Fatalf("errors = nil, events = %#v", events)
			}
			for _, event := range events {
				if event.Type == agentprotocol.ProviderEventTypeCompleted {
					t.Fatalf("unexpected successful completed: %#v", event)
				}
			}
		})
	}
}

func TestParseDeepSeekStreamCarriesKnownUsageWhenDoneIsMissing(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n"
	events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(stream)))
	if len(events) != 0 || len(failures) != 1 {
		t.Fatalf("events = %#v, failures = %#v", events, failures)
	}
	var providerFailure *ProviderError
	if !errors.As(failures[0], &providerFailure) || !reflect.DeepEqual(providerFailure.KnownUsage, &agentprotocol.Usage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}) {
		t.Fatalf("failure = %#v", failures[0])
	}
	providerFailure.KnownUsage.InputTokens = 99
	events, failures = collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(stream)))
	if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &providerFailure) || providerFailure.KnownUsage.InputTokens != 5 {
		t.Fatalf("KnownUsage snapshot was not isolated: events=%#v failures=%#v", events, failures)
	}
}

func TestParseDeepSeekStreamRejectsIncompleteOrNullUsageWithoutKnownUsage(t *testing.T) {
	tests := map[string]string{
		"null usage":        "null",
		"empty usage":       `{}`,
		"partial usage":     `{"prompt_tokens":1}`,
		"null token member": `{"prompt_tokens":null,"completion_tokens":1,"total_tokens":1}`,
	}
	for name, usage := range tests {
		for _, done := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/failure", true: "/completion"}[done], func(t *testing.T) {
				stream := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":" + usage + "}\n\n"
				if done {
					stream += "data: [DONE]\n\n"
				}
				events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(stream)))
				if len(failures) != 1 {
					t.Fatalf("events=%#v failures=%#v", events, failures)
				}
				for _, event := range events {
					if event.Type == agentprotocol.ProviderEventTypeCompleted {
						t.Fatalf("invalid Usage completed: %#v", event)
					}
				}
				var failure *ProviderError
				if !errors.As(failures[0], &failure) || failure.KnownUsage != nil {
					t.Fatalf("invalid Usage became KnownUsage: %#v", failures[0])
				}
			})
		}
	}
}

func TestParseDeepSeekStreamClassifiesSourceIOFailuresAndPreservesOnlyValidUsage(t *testing.T) {
	validUsage := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n"
	tests := []struct {
		name       string
		prefix     string
		knownUsage *agentprotocol.Usage
	}{
		{name: "before publication"},
		{name: "after valid usage", prefix: validUsage, knownUsage: &agentprotocol.Usage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &sourceErrorReader{data: []byte(test.prefix), failure: io.ErrUnexpectedEOF}
			events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), source))
			var failure *ProviderError
			if len(events) != 0 || len(failures) != 1 || !errors.As(failures[0], &failure) || failure.Code != agentprotocol.ErrorCodeProviderUnavailable || !failure.Retryable || !reflect.DeepEqual(failure.KnownUsage, test.knownUsage) {
				t.Fatalf("events=%#v failures=%#v", events, failures)
			}
		})
	}
}

func TestParseDeepSeekStreamAppliesDepthLimitToFragmentedToolArguments(t *testing.T) {
	accepted, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(fragmentedArgumentStream(nestedArgument(16)))))
	if len(failures) != 0 || len(accepted) != 2 || accepted[0].Type != agentprotocol.ProviderEventTypeToolCall || accepted[1].Type != agentprotocol.ProviderEventTypeCompleted {
		t.Fatalf("depth 16 events=%#v failures=%#v", accepted, failures)
	}
	rejected, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(fragmentedArgumentStream(nestedArgument(17)))))
	if len(rejected) != 0 || len(failures) != 1 {
		t.Fatalf("depth 17 events=%#v failures=%#v", rejected, failures)
	}
	var failure *ProviderError
	if !errors.As(failures[0], &failure) || failure.Code != agentprotocol.ErrorCodeProtocolIncompatible || !reflect.DeepEqual(failure.KnownUsage, &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}) {
		t.Fatalf("depth 17 failure=%#v", failures[0])
	}
}

func TestParseDeepSeekStreamRejectsFinishReasonThatDoesNotMatchToolPresence(t *testing.T) {
	tests := map[string]string{
		"call with stop":           "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"skill_list\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n",
		"tool finish without call": "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n",
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(stream)))
			if len(failures) != 1 {
				t.Fatalf("events=%#v failures=%#v", events, failures)
			}
			for _, event := range events {
				if event.Type == agentprotocol.ProviderEventTypeCompleted {
					t.Fatalf("mismatched terminal completed: %#v", event)
				}
			}
		})
	}
}

func TestParseDeepSeekStreamEnforcesDataDepthUTF8AndReasoningBounds(t *testing.T) {
	terminal := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"
	largeFrame := "data: {\"padding\":\"" + strings.Repeat("a", maxSSEDataBytes) + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\n" + terminal
	smallPadding := strings.Repeat("a", 65500)
	var cumulative strings.Builder
	for range 17 {
		cumulative.WriteString("data: {\"padding\":\"")
		cumulative.WriteString(smallPadding)
		cumulative.WriteString("\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\n")
	}
	cumulative.WriteString(terminal)
	deep := "data: {\"padding\":" + strings.Repeat("[", maxJSONDepth+1) + "0" + strings.Repeat("]", maxJSONDepth+1) + ",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\n" + terminal
	reasoning := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private thought\"},\"finish_reason\":null}]}\n\n" + terminal
	invalidUTF8 := append([]byte("data: {\"padding\":\""), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte("\",\"choices\":[]}\n\n")...)
	tests := map[string][]byte{
		"single frame":    []byte(largeFrame),
		"cumulative turn": []byte(cumulative.String()),
		"json depth":      []byte(deep),
		"reasoning":       []byte(reasoning),
		"invalid utf8":    invalidUTF8,
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(string(stream))))
			if len(failures) != 1 {
				t.Fatalf("events=%#v failures=%#v", events, failures)
			}
			for _, event := range events {
				if event.Type == agentprotocol.ProviderEventTypeCompleted {
					t.Fatalf("bounded failure completed: %#v", event)
				}
			}
		})
	}
}

func TestParseDeepSeekStreamParsesTextFixture(t *testing.T) {
	events, failures := collectProviderSequence(ParseDeepSeekStream(context.Background(), strings.NewReader(readDeepSeekFixture(t, "text-turn.sse"))))
	if len(failures) != 0 || len(events) != 2 || events[0].Text == nil || *events[0].Text != "Calendar overview ready." {
		t.Fatalf("events=%#v failures=%#v", events, failures)
	}
	wantCompleted := agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: stopReason(agentprotocol.ProviderEventStopReasonEndTurn), Usage: &agentprotocol.Usage{InputTokens: 8, OutputTokens: 7, TotalTokens: 15}}
	if !reflect.DeepEqual(events[1], wantCompleted) {
		t.Fatalf("completed=%#v, want %#v", events[1], wantCompleted)
	}
}

func readDeepSeekFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "deepseek", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func collectProviderSequence(sequence func(func(agentprotocol.ProviderEvent, error) bool)) ([]agentprotocol.ProviderEvent, []error) {
	var events []agentprotocol.ProviderEvent
	var errors []error
	sequence(func(event agentprotocol.ProviderEvent, err error) bool {
		if err != nil {
			errors = append(errors, err)
		} else {
			events = append(events, event)
		}
		return true
	})
	return events, errors
}

func stopReason(value agentprotocol.ProviderEventStopReason) *agentprotocol.ProviderEventStopReason {
	return &value
}
func stringPointer(value string) *string { return &value }

type sourceErrorReader struct {
	data    []byte
	failure error
}

func (reader *sourceErrorReader) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, reader.failure
	}
	count := copy(buffer, reader.data)
	reader.data = reader.data[count:]
	return count, nil
}

func nestedArgument(depth int) string {
	return `{"value":` + strings.Repeat("[", depth-1) + "0" + strings.Repeat("]", depth-1) + "}"
}

func fragmentedArgumentStream(arguments string) string {
	split := len(arguments) / 2
	first, _ := json.Marshal(arguments[:split])
	second, _ := json.Marshal(arguments[split:])
	return "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"depth-call\",\"type\":\"function\",\"function\":{\"name\":\"skill_list\",\"arguments\":" + string(first) + "}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":" + string(second) + "}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n"
}
