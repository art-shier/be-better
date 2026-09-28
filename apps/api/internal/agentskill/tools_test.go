package agentskill

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttool"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func toJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func metaBindings(t *testing.T, disabled bool) []agenttool.Binding {
	t.Helper()
	replacements := [][2]string{}
	if disabled {
		replacements = append(replacements, [2]string{"disable-model-invocation: false", "disable-model-invocation: true"})
	}
	registry, err := NewRegistry([]SkillProfile{profileWith(t, replacements...)})
	if err != nil {
		t.Fatal(err)
	}
	tools := &agenttool.Registry{}
	registerTool(t, tools, "dayorder.calendar.read", []string{"calendar"})
	return MetaBindings(registry, capabilitySnapshot(), tools, agenttool.Policy{Allow: []string{"*"}})
}

func bindingByID(t *testing.T, bindings []agenttool.Binding, id string) agenttool.Binding {
	t.Helper()
	for _, binding := range bindings {
		if binding.Spec().ID == id {
			return binding
		}
	}
	t.Fatalf("binding %q not found", id)
	return nil
}

func TestMetaBindingsPublishExactClosedSpecs(t *testing.T) {
	bindings := metaBindings(t, false)
	if got := []string{bindings[0].Spec().ID, bindings[1].Spec().ID}; !reflect.DeepEqual(got, []string{"skill_list", "skill_load"}) {
		t.Fatalf("binding IDs = %v", got)
	}
	for _, binding := range bindings {
		spec := binding.Spec()
		if spec.SideEffect != agentprotocol.SideEffectRead || len(spec.RequiredDomains) != 0 ||
			!reflect.DeepEqual(spec.ExecutionTargets, []agentprotocol.ToolSpecExecutionTargetsElem{agentprotocol.ToolSpecExecutionTargetsElemClient, agentprotocol.ToolSpecExecutionTargetsElemServer}) ||
			spec.ApprovalPolicy != agentprotocol.ToolSpecApprovalPolicyNever || !spec.Idempotent || spec.TimeoutMs != 5000 || spec.ResultMaxBytes != 256*1024 {
			t.Fatalf("spec metadata = %#v", spec)
		}
		if err := agentprotocol.Validate(agentprotocol.DefinitionToolSpec, spec); err != nil {
			t.Fatalf("invalid ToolSpec: %v", err)
		}
	}

	list := bindings[0].Spec()
	load := bindings[1].Spec()
	wantListInput := agentprotocol.ToolSpecInputSchema{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	wantLoadInput := agentprotocol.ToolSpecInputSchema{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"}, "additionalProperties": false}
	if !reflect.DeepEqual(normalizeForTest(t, list.InputSchema), normalizeForTest(t, wantListInput)) || !reflect.DeepEqual(normalizeForTest(t, load.InputSchema), normalizeForTest(t, wantLoadInput)) {
		t.Fatalf("input schemas = %#v / %#v", list.InputSchema, load.InputSchema)
	}
	assertClosedOutputSchemas(t, list.OutputSchema, load.OutputSchema)
}

func normalizeForTest(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func compileSchema(t *testing.T, value any) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:test", normalizeForTest(t, value)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:test")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func assertClosedOutputSchemas(t *testing.T, list, load any) {
	t.Helper()
	listSchema := compileSchema(t, list)
	loadSchema := compileSchema(t, load)
	descriptor := map[string]any{
		"name": "calendar-management", "version": "1.0.0", "digest": goldenDigest,
		"description": "Read and propose changes to DayOrder calendar data.", "scope": "system", "executionTarget": "either",
		"backgroundAllowed": true, "userInvocable": true, "disableModelInvocation": false, "riskLevel": "medium",
	}
	activation := map[string]any{
		"skill":        map[string]any{"name": "calendar-management", "version": "1.0.0", "digest": goldenDigest},
		"instructions": "# Calendar Management\n", "supportingFiles": []any{map[string]any{"path": "references/usage.md", "kind": "reference", "size": float64(74)}},
		"requestedToolIds": []any{"dayorder.calendar.read"}, "activeToolIds": []any{},
	}
	if err := listSchema.Validate(map[string]any{"skills": []any{descriptor}}); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	if err := loadSchema.Validate(map[string]any{"activation": activation}); err != nil {
		t.Fatalf("valid load rejected: %v", err)
	}
	delete(descriptor, "riskLevel")
	if err := listSchema.Validate(map[string]any{"skills": []any{descriptor}}); err == nil {
		t.Fatal("descriptor without riskLevel accepted")
	}
	descriptor["riskLevel"] = "medium"
	descriptor["body"] = "must stay undisclosed"
	if err := listSchema.Validate(map[string]any{"skills": []any{descriptor}}); err == nil {
		t.Fatal("descriptor with body accepted")
	}
	delete(activation, "activeToolIds")
	if err := loadSchema.Validate(map[string]any{"activation": activation}); err == nil {
		t.Fatal("activation without activeToolIds accepted")
	}
	activation["activeToolIds"] = []any{}
	activation["supportingFiles"] = []any{map[string]any{"path": "references/usage.md", "kind": "reference", "size": float64(74), "content": "must stay internal"}}
	if err := loadSchema.Validate(map[string]any{"activation": activation}); err == nil {
		t.Fatal("activation supporting file with content accepted")
	}
	activation["supportingFiles"] = []any{map[string]any{"path": "references/usage.md", "kind": "reference", "size": float64(74)}}
	activation["skill"] = map[string]any{"name": "calendar-management", "version": "1.0.0"}
	if err := loadSchema.Validate(map[string]any{"activation": activation}); err == nil {
		t.Fatal("activation skill without digest accepted")
	}
}

func TestMetaBindingsListAndLoadProgressivelyWithoutPermissionGrant(t *testing.T) {
	bindings := metaBindings(t, false)
	listResult, err := bindingByID(t, bindings, "skill_list").Invoke(context.Background(), map[string]any{}, agenttool.Context{RunID: "run-1", CallID: "call-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !listResult.Ok || strings.Contains(toJSON(t, listResult), "Read only the requested date range") {
		t.Fatalf("list result = %#v", listResult)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolResult, listResult); err != nil {
		t.Fatalf("list result invalid: %v", err)
	}

	loadResult, err := bindingByID(t, bindings, "skill_load").Invoke(context.Background(), map[string]any{"name": "calendar-management"}, agenttool.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if !loadResult.Ok || !strings.Contains(toJSON(t, loadResult), "Read only the requested date range") || strings.Contains(toJSON(t, loadResult), "permissionGrant") {
		t.Fatalf("load result = %#v", loadResult)
	}
	activation := loadResult.Data["activation"].(agentprotocol.SkillActivation)
	if len(activation.ActiveToolIds) != 0 || !reflect.DeepEqual(activation.RequestedToolIds, []string{"dayorder.calendar.propose-change", "dayorder.calendar.read"}) {
		t.Fatalf("activation = %#v", activation)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolResult, loadResult); err != nil {
		t.Fatalf("load result invalid: %v", err)
	}
}

func TestMetaBindingsHideModelDisabledSkills(t *testing.T) {
	bindings := metaBindings(t, true)
	list, _ := bindingByID(t, bindings, "skill_list").Invoke(context.Background(), map[string]any{}, agenttool.Context{})
	if got := toJSON(t, list.Data); got != `{"skills":[]}` {
		t.Fatalf("hidden list data = %s", got)
	}
	load, _ := bindingByID(t, bindings, "skill_load").Invoke(context.Background(), map[string]any{"name": "calendar-management"}, agenttool.Context{})
	assertFailure(t, load, agentprotocol.ErrorCodeCapabilityUnavailable, "Skill is not available to the model: calendar-management")
}

func TestMetaBindingsReturnStableValidationFailures(t *testing.T) {
	cases := []struct {
		id      string
		input   map[string]any
		message string
	}{
		{"skill_list", nil, "skill_list input must be an empty object"},
		{"skill_list", map[string]any{"unexpected": true}, "skill_list input must be an empty object"},
		{"skill_load", nil, "skill_load input must contain only a non-empty string name"},
		{"skill_load", map[string]any{}, "skill_load input must contain only a non-empty string name"},
		{"skill_load", map[string]any{"name": ""}, "skill_load input must contain only a non-empty string name"},
		{"skill_load", map[string]any{"name": 4}, "skill_load input must contain only a non-empty string name"},
		{"skill_load", map[string]any{"name": "calendar-management", "unexpected": true}, "skill_load input must contain only a non-empty string name"},
	}
	bindings := metaBindings(t, false)
	for _, tc := range cases {
		t.Run(tc.id+toJSON(t, tc.input), func(t *testing.T) {
			result, err := bindingByID(t, bindings, tc.id).Invoke(context.Background(), tc.input, agenttool.Context{})
			if err != nil {
				t.Fatal(err)
			}
			assertFailure(t, result, agentprotocol.ErrorCodeValidationFailed, tc.message)
		})
	}
}

func assertFailure(t *testing.T, result agentprotocol.ToolResult, code agentprotocol.ErrorCode, message string) {
	t.Helper()
	if result.Ok || result.Error == nil || result.Error.Code != code || result.Error.Message != message || result.Error.Retryable || result.Data != nil {
		t.Fatalf("failure result = %#v", result)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolResult, result); err != nil {
		t.Fatalf("failure result invalid: %v", err)
	}
}

func TestMetaBindingsReturnIsolatedSpecsAndResults(t *testing.T) {
	bindings := metaBindings(t, false)
	list := bindingByID(t, bindings, "skill_list")
	spec := list.Spec()
	spec.InputSchema["type"] = "array"
	if list.Spec().InputSchema["type"] != "object" {
		t.Fatal("Spec() returned aliased schema")
	}

	first, _ := list.Invoke(context.Background(), map[string]any{}, agenttool.Context{})
	first.Data["skills"].([]agentprotocol.SkillDescriptor)[0].Name = "mutated"
	second, _ := list.Invoke(context.Background(), map[string]any{}, agenttool.Context{})
	if second.Data["skills"].([]agentprotocol.SkillDescriptor)[0].Name != "calendar-management" {
		t.Fatalf("second list = %#v", second)
	}

	load := bindingByID(t, bindings, "skill_load")
	firstLoad, _ := load.Invoke(context.Background(), map[string]any{"name": "calendar-management"}, agenttool.Context{})
	activation := firstLoad.Data["activation"].(agentprotocol.SkillActivation)
	activation.RequestedToolIds = activation.RequestedToolIds[:0]
	secondLoad, _ := load.Invoke(context.Background(), map[string]any{"name": "calendar-management"}, agenttool.Context{})
	if got := secondLoad.Data["activation"].(agentprotocol.SkillActivation).RequestedToolIds; len(got) != 2 {
		t.Fatalf("second load requested IDs = %v", got)
	}
}
