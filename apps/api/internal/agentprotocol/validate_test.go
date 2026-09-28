package agentprotocol

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRuntimeInput(t *testing.T) {
	if err := Validate(DefinitionRuntimeInput, map[string]any{"type": "user_message", "text": "plan today"}); err != nil {
		t.Fatal(err)
	}

	var target *ValidationError
	err := Validate(DefinitionRuntimeInput, map[string]any{"type": "user_message", "extra": true})
	if !errors.As(err, &target) || target.Code() != "validation_failed" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateConformanceCaseRequiresMatchingArrays(t *testing.T) {
	initialState := map[string]any{
		"protocolVersion": "2.0",
		"runId":           "run-1",
		"executionMode":   "foreground",
		"phase":           "idle",
		"sequence":        0,
		"stepCount":       0,
		"messages":        []any{},
		"capabilitySnapshot": map[string]any{
			"runtimeVersion": "2.0.0",
			"executionMode":  "foreground",
			"toolIds":        []any{},
			"skills":         []any{},
			"scope":          map[string]any{"domains": []any{}},
		},
		"budget": map[string]any{
			"maxSteps":             1,
			"maxTokens":            1,
			"maxDurationMs":        1,
			"maxWorkers":           1,
			"maxConcurrency":       1,
			"maxRepeatedToolCalls": 1,
		},
		"usage":             map[string]any{"inputTokens": 0, "outputTokens": 0, "totalTokens": 0},
		"repeatedToolCalls": 0,
	}

	var target *ValidationError
	err := Validate(DefinitionConformanceCase, map[string]any{
		"name":                "mismatched arrays",
		"protocolVersion":     "2.0",
		"initialState":        initialState,
		"inputs":              []any{map[string]any{"type": "user_message", "text": "plan today"}},
		"expectedTransitions": []any{},
	})
	if !errors.As(err, &target) || target.Code() != "validation_failed" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateCompletedProviderEventRequiresStopReasonAndUsage(t *testing.T) {
	tests := []struct {
		name  string
		event map[string]any
	}{
		{name: "neither", event: map[string]any{"type": "completed"}},
		{name: "stop reason only", event: map[string]any{"type": "completed", "stopReason": "end_turn"}},
		{name: "usage only", event: map[string]any{"type": "completed", "usage": map[string]any{"inputTokens": 1, "outputTokens": 1, "totalTokens": 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var target *ValidationError
			err := Validate(DefinitionProviderEvent, test.event)
			if !errors.As(err, &target) || target.Code() != "validation_failed" {
				t.Fatalf("error = %v, want validation_failed", err)
			}
		})
	}
}

func TestValidateProviderEnvelopeRequiresStrictVersionedSequence(t *testing.T) {
	envelope := map[string]any{
		"protocolVersion": "2.0",
		"runId":           "run-1",
		"turnId":          "turn-1",
		"sequence":        1,
		"event":           map[string]any{"type": "text_delta", "text": "hello"},
	}
	if err := Validate(DefinitionProviderEnvelope, envelope); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"old version":   func(value map[string]any) { value["protocolVersion"] = "1.0" },
		"zero sequence": func(value map[string]any) { value["sequence"] = 0 },
		"unknown field": func(value map[string]any) { value["extra"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := make(map[string]any, len(envelope)+1)
			for key, value := range envelope {
				invalid[key] = value
			}
			mutate(invalid)
			var target *ValidationError
			err := Validate(DefinitionProviderEnvelope, invalid)
			if !errors.As(err, &target) || target.Code() != "validation_failed" {
				t.Fatalf("error = %v, want validation_failed", err)
			}
		})
	}
}

func TestValidateCrossHostTimerBoundaries(t *testing.T) {
	budget := map[string]any{
		"maxSteps": 1, "maxTokens": 1, "maxDurationMs": 2_147_483_647,
		"maxWorkers": 1, "maxConcurrency": 1, "maxRepeatedToolCalls": 1,
	}
	tool := map[string]any{
		"id": "test.clock.read", "description": "Read a deterministic test clock",
		"inputSchema": map[string]any{"type": "object"}, "outputSchema": map[string]any{"type": "object"},
		"sideEffect": "read", "requiredDomains": []any{"test"}, "executionTargets": []any{"server"},
		"approvalPolicy": "never", "idempotent": true, "timeoutMs": 2_147_483_647, "resultMaxBytes": 1,
	}

	tests := []struct {
		name       string
		definition Definition
		valid      map[string]any
		field      string
	}{
		{name: "Run duration", definition: DefinitionBudget, valid: budget, field: "maxDurationMs"},
		{name: "Tool timeout", definition: DefinitionToolSpec, valid: tool, field: "timeoutMs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.definition, test.valid); err != nil {
				t.Fatalf("maximum rejected: %v", err)
			}
			above := make(map[string]any, len(test.valid))
			for key, value := range test.valid {
				above[key] = value
			}
			above[test.field] = 2_147_483_648
			var target *ValidationError
			err := Validate(test.definition, above)
			if !errors.As(err, &target) || target.Code() != "validation_failed" {
				t.Fatalf("max + 1 error = %v, want validation_failed", err)
			}
		})
	}
}

func TestValidateCalendarReadInputBoundaries(t *testing.T) {
	valid := map[string]any{
		"start":  "2026-09-05T00:00:00+08:00",
		"end":    "2026-09-06T00:00:00+08:00",
		"cursor": strings.Repeat("日", 1_365),
		"limit":  50,
	}
	if err := Validate(DefinitionCalendarReadInput, valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	tests := []map[string]any{
		{"start": valid["start"], "end": valid["end"], "extra": true},
		{"start": "not-a-time", "end": valid["end"]},
		{"start": valid["start"], "end": valid["end"], "limit": 0},
		{"start": valid["start"], "end": valid["end"], "limit": 51},
		{"start": valid["start"], "end": valid["end"], "limit": 1.5},
		{"start": valid["start"], "end": valid["end"], "cursor": strings.Repeat("x", 4_097)},
		{"start": valid["start"], "end": valid["end"], "cursor": strings.Repeat("日", 1_366)},
		{"end": valid["end"]},
	}
	for index, invalid := range tests {
		if err := Validate(DefinitionCalendarReadInput, invalid); err == nil {
			t.Fatalf("invalid input %d accepted: %#v", index, invalid)
		}
	}
}

func TestValidateCalendarReadDataFields(t *testing.T) {
	event := map[string]any{
		"id":       "550e8400-e29b-41d4-a716-446655440000",
		"title":    "Planning",
		"startAt":  "2026-09-05T09:00:00Z",
		"endAt":    "2026-09-05T09:30:00Z",
		"timezone": "Asia/Shanghai",
		"kind":     "meeting",
		"version":  1,
	}
	valid := map[string]any{
		"events":  []any{event},
		"window":  map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"},
		"hasMore": false, "nextCursor": nil,
	}
	if err := Validate(DefinitionCalendarReadData, valid); err != nil {
		t.Fatalf("valid data rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown root":  func(value map[string]any) { value["extra"] = true },
		"invalid UUID":  func(value map[string]any) { value["events"].([]any)[0].(map[string]any)["id"] = "not-a-uuid" },
		"invalid time":  func(value map[string]any) { value["events"].([]any)[0].(map[string]any)["startAt"] = "tomorrow" },
		"zero version":  func(value map[string]any) { value["events"].([]any)[0].(map[string]any)["version"] = 0 },
		"unknown event": func(value map[string]any) { value["events"].([]any)[0].(map[string]any)["extra"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			copyEvent := make(map[string]any, len(event)+1)
			for key, value := range event {
				copyEvent[key] = value
			}
			invalid := map[string]any{
				"events":  []any{copyEvent},
				"window":  map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"},
				"hasMore": false, "nextCursor": nil,
			}
			mutate(invalid)
			if err := Validate(DefinitionCalendarReadData, invalid); err == nil {
				t.Fatalf("invalid data accepted: %#v", invalid)
			}
		})
	}
}

func TestValidateReadonlyRunStartAndCalendarRequest(t *testing.T) {
	start := map[string]any{
		"intent": "Summarize my calendar", "executionMode": "foreground",
		"scope": map[string]any{
			"domains": []any{"calendar"}, "from": "2026-09-05T00:00:00+08:00", "to": "2026-09-06T00:00:00+08:00",
		},
		"timezone": "Asia/Shanghai", "modelProfile": "client/default",
	}
	request := map[string]any{
		"callId": "call-1",
		"input":  map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"},
	}
	if err := Validate(DefinitionReadonlyRunStart, start); err != nil {
		t.Fatalf("valid start rejected: %v", err)
	}
	if err := Validate(DefinitionCalendarReadRequest, request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	invalidStarts := []map[string]any{
		{"intent": strings.Repeat("x", 2_001), "executionMode": "foreground", "scope": start["scope"], "timezone": "Asia/Shanghai", "modelProfile": "client/default"},
		{"intent": "x", "executionMode": "foreground", "scope": map[string]any{"domains": []any{"task"}, "from": "2026-09-05T00:00:00Z", "to": "2026-09-06T00:00:00Z"}, "timezone": "UTC", "modelProfile": "client/default"},
		{"intent": "x", "executionMode": "foreground", "scope": map[string]any{"domains": []any{"calendar"}, "from": "2026-09-05T00:00:00Z"}, "timezone": "UTC", "modelProfile": "client/default"},
		{"intent": "x", "executionMode": "foreground", "scope": map[string]any{"domains": []any{"calendar"}, "entityIds": []any{"event-1"}, "from": "2026-09-05T00:00:00Z", "to": "2026-09-06T00:00:00Z"}, "timezone": "UTC", "modelProfile": "client/default"},
		{"intent": "x", "executionMode": "foreground", "scope": start["scope"], "timezone": "UTC", "modelProfile": "client/default", "extra": true},
	}
	for index, invalid := range invalidStarts {
		if err := Validate(DefinitionReadonlyRunStart, invalid); err == nil {
			t.Fatalf("invalid start %d accepted: %#v", index, invalid)
		}
	}
	request["extra"] = true
	if err := Validate(DefinitionCalendarReadRequest, request); err == nil {
		t.Fatalf("request with unknown field accepted")
	}
}

func TestValidateReadonlyRunViewAndFinishLimits(t *testing.T) {
	view := map[string]any{
		"protocolVersion": "2.0", "runId": "550e8400-e29b-41d4-a716-446655440000",
		"executionMode": "background", "status": "completed", "version": 1,
		"capabilitySnapshot": map[string]any{
			"runtimeVersion": "2.0.0", "executionMode": "background", "toolIds": []any{"dayorder.calendar.read"},
			"skills": []any{}, "scope": map[string]any{"domains": []any{"calendar"}},
		},
		"budget": map[string]any{
			"maxSteps": 1, "maxTokens": 1, "maxDurationMs": 1, "maxWorkers": 1,
			"maxConcurrency": 1, "maxRepeatedToolCalls": 1,
		},
		"modelProfile": "server/default", "serverNow": "2026-09-05T00:00:00Z",
		"deadlineAt": "2026-09-05T00:01:00Z", "usage": map[string]any{"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
		"usageComplete": true, "resultOrigin": "server_runtime", "summary": "Done",
	}
	finish := map[string]any{
		"phase": "completed", "summary": "Done",
		"steps": []any{map[string]any{"title": "Read calendar", "detail": "One page"}},
	}
	if err := Validate(DefinitionReadonlyRunView, view); err != nil {
		t.Fatalf("valid view rejected: %v", err)
	}
	if err := Validate(DefinitionReadonlyRunFinish, finish); err != nil {
		t.Fatalf("valid finish rejected: %v", err)
	}
	for _, invalid := range []map[string]any{
		copyMapWith(view, "runId", "not-a-uuid"),
		copyMapWith(view, "serverNow", "today"),
		copyMapWith(view, "version", 0),
		copyMapWith(view, "extra", true),
	} {
		if err := Validate(DefinitionReadonlyRunView, invalid); err == nil {
			t.Fatalf("invalid view accepted: %#v", invalid)
		}
	}
	steps17 := make([]any, 17)
	for index := range steps17 {
		steps17[index] = map[string]any{"title": "x", "detail": "x"}
	}
	for _, invalid := range []map[string]any{
		copyMapWith(finish, "summary", strings.Repeat("x", 8_001)),
		copyMapWith(finish, "steps", steps17),
		copyMapWith(finish, "steps", []any{map[string]any{"title": strings.Repeat("x", 241), "detail": "ok"}}),
		copyMapWith(finish, "steps", []any{map[string]any{"title": "ok", "detail": strings.Repeat("x", 2_001)}}),
		copyMapWith(finish, "extra", true),
	} {
		if err := Validate(DefinitionReadonlyRunFinish, invalid); err == nil {
			t.Fatalf("invalid finish accepted: %#v", invalid)
		}
	}
}

func copyMapWith(source map[string]any, key string, value any) map[string]any {
	copied := make(map[string]any, len(source)+1)
	for sourceKey, sourceValue := range source {
		copied[sourceKey] = sourceValue
	}
	copied[key] = value
	return copied
}
