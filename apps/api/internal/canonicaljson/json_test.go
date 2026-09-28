package canonicaljson_test

import (
	"testing"

	"dayorder.local/api/internal/canonicaljson"
)

func TestBytesCanonicalizesEquivalentJSON(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("{\"b\":2,\"a\":1.0}"),
		[]byte("{\n \"a\":1,\"b\":2}"),
	} {
		got, err := canonicaljson.Bytes(input)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "{\"a\":1,\"b\":2}" {
			t.Fatalf("Bytes() = %s", got)
		}
	}
}

func TestBytesRejectsMultipleDocuments(t *testing.T) {
	if _, err := canonicaljson.Bytes([]byte("{} []")); err == nil {
		t.Fatal("Bytes() accepted multiple JSON documents")
	}
}

func TestValueUsesTheSameCanonicalPolicy(t *testing.T) {
	got, err := canonicaljson.Value(map[string]any{"value": 1.0, "control": "\n"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"control\":\"\\n\",\"value\":1}" {
		t.Fatalf("Value() = %s", got)
	}
}
