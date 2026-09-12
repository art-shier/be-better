package agentruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"dayorder.local/api/internal/agentprotocol"
)

func TestConformanceReplaysCanonicalFixtures(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		root = filepath.Dir(root)
	}
	paths, err := filepath.Glob(filepath.Join(root, "contracts", "agent", "conformance", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) != 11 {
		t.Fatalf("fixture count = %d, want 11", len(paths))
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture agentprotocol.ConformanceCase
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err := agentprotocol.Validate(agentprotocol.DefinitionConformanceCase, fixture); err != nil {
			t.Fatalf("%s: fixture validation: %v", path, err)
		}
		state := fixture.InitialState
		for index, input := range fixture.Inputs {
			actual, err := Advance(state, input)
			if err != nil {
				t.Fatalf("%s transition %d: %v", path, index, err)
			}
			if got, want := mustJSON(t, actual), mustJSON(t, fixture.ExpectedTransitions[index]); got != want {
				t.Fatalf("%s transition %d mismatch\ngot:  %s\nwant: %s", path, index, got, want)
			}
			state = actual.State
		}
	}
}
