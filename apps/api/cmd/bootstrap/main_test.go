package main

import (
	"bytes"
	"strings"
	"testing"

	"dayorder.local/api/internal/dbbootstrap"
)

func TestParseOptionsDefaultsToApplyMode(t *testing.T) {
	got, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preflight {
		t.Fatal("no arguments unexpectedly selected preflight mode")
	}
}

func TestParseOptionsSelectsReadOnlyPreflight(t *testing.T) {
	got, err := parseOptions([]string{"-preflight"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Preflight {
		t.Fatal("-preflight did not select read-only mode")
	}
}

func TestParseOptionsRejectsPositionalAndUnknownArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"dayorder-test"},
		{"-unknown"},
		{"-database", "dayorder-test"},
		{"-host", "db.example"},
		{"-username", "admin"},
		{"-password", "secret"},
		{"-reset"},
		{"-drop"},
		{"-force"},
	} {
		if _, err := parseOptions(arguments); err == nil {
			t.Fatalf("unsafe or unknown arguments unexpectedly accepted: %q", arguments[0])
		}
	}
}

func TestWriteBootstrapResultPrintsOnlyFixedStatus(t *testing.T) {
	result := dbbootstrap.Result{Databases: []dbbootstrap.DatabaseResult{
		{Name: "dayorder-test", Created: true, Version: 7},
		{Name: "dayorder", Created: false, Version: 7},
	}}
	var output bytes.Buffer
	if err := writeBootstrapResult(&output, result); err != nil {
		t.Fatal(err)
	}
	want := "database dayorder-test: created, schema version 7\n" +
		"database dayorder: existing, schema version 7\n"
	if output.String() != want {
		t.Fatalf("Bootstrap output = %q", output.String())
	}
}

func TestWriteBootstrapResultRejectsUnexpectedTargetsWithoutPrintingThem(t *testing.T) {
	const unexpected = "untrusted-database-name"
	var output bytes.Buffer
	err := writeBootstrapResult(&output, dbbootstrap.Result{Databases: []dbbootstrap.DatabaseResult{{Name: unexpected, Version: 7}}})
	if err == nil {
		t.Fatal("unexpected Bootstrap result target was accepted")
	}
	if strings.Contains(output.String(), unexpected) || output.Len() != 0 {
		t.Fatal("unexpected Bootstrap result target was printed")
	}
}
