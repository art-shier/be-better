package agentprotocol

import (
	"strings"
	"testing"
)

func TestNewSchemaCompilerAssertsFormatsAndUTF8ByteLimits(t *testing.T) {
	compiler, err := NewSchemaCompiler()
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string", "format": "uuid"},
			"at":     map[string]any{"type": "string", "format": "date-time"},
			"cursor": map[string]any{"type": "string", "maxUtf8Bytes": 4_096},
		},
		"required": []any{"id", "at", "cursor"},
	}
	if err := compiler.AddResource("urn:test:configured-schema", schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("urn:test:configured-schema")
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{
		"id": "550e8400-e29b-41d4-a716-446655440000", "at": "2026-09-05T00:00:00Z",
		"cursor": strings.Repeat("日", 1_365),
	}
	if err := compiled.Validate(valid); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
	for key, value := range map[string]any{
		"id": "not-a-uuid", "at": "2026-02-30T00:00:00Z", "cursor": strings.Repeat("日", 1_366),
	} {
		invalid := copyMapWith(valid, key, value)
		if err := compiled.Validate(invalid); err == nil {
			t.Fatalf("invalid %s accepted: %#v", key, value)
		}
	}
}

func TestNewSchemaCompilerRejectsInvalidMaxUTF8BytesSchema(t *testing.T) {
	compiler, err := NewSchemaCompiler()
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("urn:test:invalid-byte-schema", map[string]any{
		"type": "string", "maxUtf8Bytes": -1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := compiler.Compile("urn:test:invalid-byte-schema"); err == nil {
		t.Fatal("invalid maxUtf8Bytes schema compiled")
	}
}
