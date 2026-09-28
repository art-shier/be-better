package agentprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/canonicaljson"
)

const maxUTF8BytesVocabularyURL = "https://dayorder.local/schemas/agent/vocab/max-utf8-bytes"

type deepSeekMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCalls  []deepSeekToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

type deepSeekToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function deepSeekToolFunction `json:"function"`
}

type deepSeekToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Arguments   string         `json:"arguments,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type deepSeekTool struct {
	Type     string               `json:"type"`
	Function deepSeekToolFunction `json:"function"`
}

// EncodeDeepSeekRequest validates and converts one model turn without credentials.
func EncodeDeepSeekRequest(request agentprotocol.ModelTurnRequest, options TurnOptions) ([]byte, error) {
	if strings.TrimSpace(options.Model) == "" || options.MaxOutputTokens < 1 || options.MaxOutputTokens > maxTurnOutputTokens {
		return nil, protocolError()
	}
	messages, err := validatedHistory(request)
	if err != nil {
		return nil, err
	}
	tools := make([]deepSeekTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		alias, _ := deepSeekToolName(spec.ID)
		parameters, err := projectDeepSeekSchema(spec.InputSchema)
		if err != nil {
			return nil, protocolError()
		}
		tools = append(tools, deepSeekTool{Type: "function", Function: deepSeekToolFunction{Name: alias, Description: spec.Description, Parameters: parameters}})
	}
	payload := map[string]any{
		"model": options.Model, "messages": messages, "tools": tools,
		"stream": true, "stream_options": map[string]any{"include_usage": true},
		"thinking": map[string]any{"type": "disabled"}, "max_tokens": options.MaxOutputTokens,
	}
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > maxRequestBodyBytes {
		return nil, protocolError()
	}
	return raw, nil
}

// ValidateHistory applies the shared server-side history and Tool argument policy.
func ValidateHistory(request agentprotocol.ModelTurnRequest) error {
	_, err := validatedHistory(request)
	return err
}

func validatedHistory(request agentprotocol.ModelTurnRequest) ([]deepSeekMessage, error) {
	if err := agentprotocol.Validate(agentprotocol.DefinitionModelTurnRequest, request); err != nil || len(request.Messages) > maxRequestMessages {
		return nil, protocolError()
	}
	specs := make(map[string]agentprotocol.ToolSpec, len(request.Tools))
	aliases := make(map[string]string, len(request.Tools))
	for _, spec := range request.Tools {
		alias, ok := deepSeekToolName(spec.ID)
		if !ok {
			return nil, protocolError()
		}
		if _, duplicate := specs[spec.ID]; duplicate {
			return nil, protocolError()
		}
		if existing, collision := aliases[alias]; collision && existing != spec.ID {
			return nil, protocolError()
		}
		specs[spec.ID] = spec
		aliases[alias] = spec.ID
	}
	messages, err := encodeDeepSeekHistory(request.Messages, specs)
	if err != nil {
		return nil, err
	}
	return messages, nil
}

func encodeDeepSeekHistory(messages []agentprotocol.Message, specs map[string]agentprotocol.ToolSpec) ([]deepSeekMessage, error) {
	converted := make([]deepSeekMessage, 0, len(messages))
	seenCalls := make(map[string]struct{})
	pendingCallID := ""

	for _, message := range messages {
		if pendingCallID != "" && message.Role != agentprotocol.MessageRoleTool {
			return nil, protocolError()
		}
		switch message.Role {
		case agentprotocol.MessageRoleSystem, agentprotocol.MessageRoleUser:
			content, err := textOnlyContent(message.Content)
			if err != nil {
				return nil, err
			}
			converted = append(converted, deepSeekMessage{Role: string(message.Role), Content: content})
		case agentprotocol.MessageRoleAssistant:
			text, call, err := assistantContent(message.Content)
			if err != nil {
				return nil, err
			}
			vendor := deepSeekMessage{Role: "assistant", Content: text}
			if call != nil {
				if call.ID == "" {
					return nil, protocolError()
				}
				if _, exists := seenCalls[call.ID]; exists {
					return nil, protocolError()
				}
				spec, exists := specs[call.Name]
				alias, known := deepSeekToolName(call.Name)
				if !exists || !known || !validToolInput(spec.InputSchema, call.Input) {
					return nil, protocolError()
				}
				arguments, err := canonicaljson.Value(call.Input)
				if err != nil {
					return nil, protocolError()
				}
				vendor.ToolCalls = []deepSeekToolCall{{ID: call.ID, Type: "function", Function: deepSeekToolFunction{Name: alias, Arguments: string(arguments)}}}
				pendingCallID = call.ID
				seenCalls[call.ID] = struct{}{}
			}
			converted = append(converted, vendor)
		case agentprotocol.MessageRoleTool:
			if pendingCallID == "" || len(message.Content) != 1 || message.Content[0].Type != agentprotocol.ContentBlockTypeToolResult || message.Content[0].ToolResult == nil {
				return nil, protocolError()
			}
			content, err := canonicaljson.Value(*message.Content[0].ToolResult)
			if err != nil {
				return nil, protocolError()
			}
			converted = append(converted, deepSeekMessage{Role: "tool", Content: string(content), ToolCallID: pendingCallID})
			pendingCallID = ""
		default:
			return nil, protocolError()
		}
	}
	if pendingCallID != "" {
		return nil, protocolError()
	}
	return converted, nil
}

func textOnlyContent(blocks []agentprotocol.ContentBlock) (string, error) {
	if len(blocks) == 0 {
		return "", protocolError()
	}
	var text strings.Builder
	for _, block := range blocks {
		if block.Type != agentprotocol.ContentBlockTypeText || block.Text == nil {
			return "", protocolError()
		}
		text.WriteString(*block.Text)
	}
	return text.String(), nil
}

func assistantContent(blocks []agentprotocol.ContentBlock) (string, *agentprotocol.ToolCall, error) {
	if len(blocks) == 0 {
		return "", nil, protocolError()
	}
	var text strings.Builder
	var call *agentprotocol.ToolCall
	for _, block := range blocks {
		switch block.Type {
		case agentprotocol.ContentBlockTypeText:
			if block.Text == nil {
				return "", nil, protocolError()
			}
			text.WriteString(*block.Text)
		case agentprotocol.ContentBlockTypeToolCall:
			if block.ToolCall == nil || call != nil {
				return "", nil, protocolError()
			}
			call = block.ToolCall
		default:
			return "", nil, protocolError()
		}
	}
	return text.String(), call, nil
}

func validToolInput(schema map[string]any, input any) bool {
	const schemaID = "urn:dayorder:agentprovider:tool-input"
	raw, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return false
	}
	compiler, err := agentprotocol.NewSchemaCompiler()
	if err != nil || compiler.AddResource(schemaID, document) != nil {
		return false
	}
	compiled, err := compiler.Compile(schemaID)
	if err != nil {
		return false
	}
	valueRaw, err := json.Marshal(input)
	if err != nil {
		return false
	}
	var value any
	if err := json.Unmarshal(valueRaw, &value); err != nil {
		return false
	}
	return compiled.Validate(value) == nil
}

func projectDeepSeekSchema(schema map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("schema contains trailing data")
	}
	projected, ok := projectSchemaValue(value).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema is not an object")
	}
	return projected, nil
}

func projectSchemaValue(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	result := make(map[string]any, len(object))
	for key, item := range object {
		if key == "maxUtf8Bytes" {
			continue
		}
		if key == "$vocabulary" {
			if vocabulary, ok := item.(map[string]any); ok {
				copyVocabulary := make(map[string]any, len(vocabulary))
				for vocabularyURL, required := range vocabulary {
					if vocabularyURL != maxUTF8BytesVocabularyURL {
						copyVocabulary[vocabularyURL] = required
					}
				}
				result[key] = copyVocabulary
				continue
			}
		}
		switch key {
		case "$defs", "definitions", "properties", "patternProperties", "dependentSchemas":
			if schemas, ok := item.(map[string]any); ok {
				projected := make(map[string]any, len(schemas))
				for name, schema := range schemas {
					projected[name] = projectSchemaValue(schema)
				}
				result[key] = projected
				continue
			}
		case "additionalProperties", "unevaluatedProperties", "propertyNames", "items", "contains", "not", "if", "then", "else", "contentSchema":
			result[key] = projectSchemaValue(item)
			continue
		case "allOf", "anyOf", "oneOf", "prefixItems":
			if schemas, ok := item.([]any); ok {
				projected := make([]any, len(schemas))
				for index, schema := range schemas {
					projected[index] = projectSchemaValue(schema)
				}
				result[key] = projected
				continue
			}
		}
		result[key] = item
	}
	return result
}

func deepSeekToolName(name string) (string, bool) {
	for _, alias := range deepSeekToolAliases {
		if alias.internal == name {
			return alias.vendor, true
		}
	}
	return "", false
}

var deepSeekToolAliases = [...]struct{ internal, vendor string }{
	{internal: "skill_list", vendor: "skill_list"},
	{internal: "skill_load", vendor: "skill_load"},
	{internal: "dayorder.calendar.read", vendor: "dayorder_calendar_read"},
}
