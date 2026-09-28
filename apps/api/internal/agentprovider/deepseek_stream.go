package agentprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net"
	"strings"
	"unicode/utf8"

	"dayorder.local/api/internal/agentprotocol"
)

type deepSeekChunk struct {
	Choices []deepSeekChoice `json:"choices"`
	Usage   *deepSeekUsage   `json:"usage"`
}

type deepSeekChoice struct {
	Index        int           `json:"index"`
	Delta        deepSeekDelta `json:"delta"`
	FinishReason *string       `json:"finish_reason"`
}

type deepSeekDelta struct {
	Content          *string                 `json:"content"`
	ReasoningContent *string                 `json:"reasoning_content"`
	ToolCalls        []deepSeekToolCallDelta `json:"tool_calls"`
}

type deepSeekToolCallDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id"`
	Type     string                    `json:"type"`
	Function deepSeekFunctionCallDelta `json:"function"`
}

type deepSeekFunctionCallDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type deepSeekUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	TotalTokens      *int `json:"total_tokens"`
}

type pendingDeepSeekCall struct {
	id        string
	name      string
	arguments strings.Builder
	seen      bool
}

// ParseDeepSeekStream parses a cancellation-protected DeepSeek SSE response.
// Request-specific ToolSpec validation is deliberately performed by DeepSeek.Stream.
func ParseDeepSeekStream(ctx context.Context, source io.Reader) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		reader := bufio.NewReaderSize(source, 32<<10)
		var data bytes.Buffer
		var cumulative int
		var call pendingDeepSeekCall
		var finishReason *agentprotocol.ProviderEventStopReason
		var usage *agentprotocol.Usage

		fail := func(err error) {
			if err == nil {
				err = protocolError()
			}
			var providerFailure *ProviderError
			if usage != nil && errors.As(err, &providerFailure) && providerFailure.KnownUsage == nil {
				known := *usage
				providerFailure.KnownUsage = &known
			}
			yield(agentprotocol.ProviderEvent{}, err)
		}

		for {
			if ctx.Err() != nil {
				fail(&ProviderError{Code: agentprotocol.ErrorCodeCancelled, Retryable: false})
				return
			}
			line, err := readSSELine(reader)
			if err != nil && !errors.Is(err, io.EOF) {
				fail(classifyStreamReadFailure(err))
				return
			}
			if len(line) > 0 && !utf8.Valid(line) {
				fail(protocolError())
				return
			}
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				if data.Len() > 0 {
					payload := bytes.TrimSuffix(data.Bytes(), []byte{'\n'})
					data.Reset()
					if bytes.Equal(payload, []byte("[DONE]")) {
						if finishReason == nil || usage == nil {
							fail(protocolError())
							return
						}
						isToolUse := *finishReason == agentprotocol.ProviderEventStopReasonToolUse
						if call.seen != isToolUse {
							fail(protocolError())
							return
						}
						if call.seen {
							parsedCall, parseErr := completedToolCall(call)
							if parseErr != nil {
								fail(parseErr)
								return
							}
							if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeToolCall, Call: parsedCall}, nil) {
								return
							}
						}
						if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: finishReason, Usage: usage}, nil) {
							return
						}
						return
					} else {
						chunk, parseErr := decodeDeepSeekChunk(payload)
						if parseErr != nil {
							fail(parseErr)
							return
						}
						for _, event := range chunk.events {
							if !yield(event, nil) {
								return
							}
						}
						if chunk.call != nil {
							if call.seen && chunk.call.Index != 0 {
								fail(protocolError())
								return
							}
							call.seen = true
							if chunk.call.ID != "" {
								if call.id != "" && call.id != chunk.call.ID {
									fail(protocolError())
									return
								}
								call.id = chunk.call.ID
							}
							if chunk.call.Function.Name != "" {
								if call.name != "" && call.name != chunk.call.Function.Name {
									fail(protocolError())
									return
								}
								call.name = chunk.call.Function.Name
							}
							call.arguments.WriteString(chunk.call.Function.Arguments)
						}
						if chunk.finishReason != nil {
							if finishReason != nil {
								fail(protocolError())
								return
							}
							finishReason = chunk.finishReason
						}
						if chunk.usage != nil {
							if usage != nil && *usage != *chunk.usage {
								fail(protocolError())
								return
							}
							latest := *chunk.usage
							usage = &latest
						}
					}
				}
			} else if bytes.HasPrefix(line, []byte("data:")) {
				value := line[len("data:"):]
				if len(value) > 0 && value[0] == ' ' {
					value = value[1:]
				}
				if data.Len()+len(value) > maxSSEDataBytes {
					fail(protocolError())
					return
				}
				cumulative += len(value)
				if cumulative > maxTurnSSEDataBytes {
					fail(protocolError())
					return
				}
				data.Write(value)
				data.WriteByte('\n')
			}

			if errors.Is(err, io.EOF) {
				fail(protocolError())
				return
			}
		}
	}
}

type decodedDeepSeekChunk struct {
	events       []agentprotocol.ProviderEvent
	call         *deepSeekToolCallDelta
	finishReason *agentprotocol.ProviderEventStopReason
	usage        *agentprotocol.Usage
}

func decodeDeepSeekChunk(payload []byte) (decodedDeepSeekChunk, error) {
	if !utf8.Valid(payload) || jsonDepth(payload) > maxJSONDepth {
		return decodedDeepSeekChunk{}, protocolError()
	}
	var chunk deepSeekChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return decodedDeepSeekChunk{}, protocolError()
	}
	if len(chunk.Choices) > 1 {
		return decodedDeepSeekChunk{}, protocolError()
	}
	result := decodedDeepSeekChunk{}
	if len(chunk.Choices) == 0 {
		if chunk.Usage == nil {
			return decodedDeepSeekChunk{}, protocolError()
		}
	} else {
		choice := chunk.Choices[0]
		if choice.Index != 0 || (choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "") || len(choice.Delta.ToolCalls) > 1 {
			return decodedDeepSeekChunk{}, protocolError()
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			text := *choice.Delta.Content
			result.events = append(result.events, agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text})
		}
		if len(choice.Delta.ToolCalls) == 1 {
			call := choice.Delta.ToolCalls[0]
			if call.Index != 0 || (call.Type != "" && call.Type != "function") {
				return decodedDeepSeekChunk{}, protocolError()
			}
			result.call = &call
		}
		if choice.FinishReason != nil {
			mapped, err := mapDeepSeekFinishReason(*choice.FinishReason)
			if err != nil {
				return decodedDeepSeekChunk{}, err
			}
			result.finishReason = &mapped
		}
	}
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens == nil || chunk.Usage.CompletionTokens == nil || chunk.Usage.TotalTokens == nil ||
			*chunk.Usage.PromptTokens < 0 || *chunk.Usage.CompletionTokens < 0 || *chunk.Usage.TotalTokens < 0 {
			return decodedDeepSeekChunk{}, protocolError()
		}
		result.usage = &agentprotocol.Usage{InputTokens: *chunk.Usage.PromptTokens, OutputTokens: *chunk.Usage.CompletionTokens, TotalTokens: *chunk.Usage.TotalTokens}
	}
	return result, nil
}

func completedToolCall(call pendingDeepSeekCall) (*agentprotocol.ToolCall, error) {
	name, ok := internalToolName(call.name)
	if !ok || call.id == "" || call.name == "" {
		return nil, protocolError()
	}
	arguments := call.arguments.String()
	if jsonDepth([]byte(arguments)) > maxJSONDepth {
		return nil, protocolError()
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var input map[string]any
	if err := decoder.Decode(&input); err != nil || input == nil {
		return nil, protocolError()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, protocolError()
	}
	return &agentprotocol.ToolCall{ID: call.id, Name: name, Input: input}, nil
}

func mapDeepSeekFinishReason(reason string) (agentprotocol.ProviderEventStopReason, error) {
	switch reason {
	case "stop":
		return agentprotocol.ProviderEventStopReasonEndTurn, nil
	case "tool_calls":
		return agentprotocol.ProviderEventStopReasonToolUse, nil
	case "length":
		return agentprotocol.ProviderEventStopReasonMaxTokens, nil
	default:
		return "", protocolError()
	}
}

func internalToolName(name string) (string, bool) {
	for _, alias := range deepSeekToolAliases {
		if alias.vendor == name {
			return alias.internal, true
		}
	}
	return "", false
}

func readSSELine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > maxSSEDataBytes+16 {
			return nil, protocolError()
		}
		if err == nil {
			return bytes.TrimSuffix(line, []byte{'\n'}), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func classifyStreamReadFailure(err error) error {
	var providerFailure *ProviderError
	if errors.As(err, &providerFailure) {
		return providerFailure
	}
	if errors.Is(err, context.Canceled) {
		return &ProviderError{Code: agentprotocol.ErrorCodeCancelled, Retryable: false}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ProviderError{Code: agentprotocol.ErrorCodeTimeout, Retryable: true}
	}
	var networkFailure net.Error
	if errors.As(err, &networkFailure) && networkFailure.Timeout() {
		return &ProviderError{Code: agentprotocol.ErrorCodeTimeout, Retryable: true}
	}
	return &ProviderError{Code: agentprotocol.ErrorCodeProviderUnavailable, Retryable: true}
}

func jsonDepth(payload []byte) int {
	depth, maximum := 0, 0
	inString, escaped := false, false
	for _, value := range payload {
		if inString {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maximum {
				maximum = depth
			}
		case '}', ']':
			depth--
		}
	}
	return maximum
}
