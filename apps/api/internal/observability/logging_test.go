package observability

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerWritesJSONAndRedactsSensitiveAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(&output, "api", slog.LevelInfo)

	logger.Info("request completed",
		"requestId", "5f75f493-75b9-4e08-9d15-c973bdc1eeda",
		"password", "secret-password",
		"sessionToken", "secret-session",
		"noteContent", "private-note-body",
		"status", 200,
	)

	logged := output.String()
	for _, secret := range []string{"secret-password", "secret-session", "private-note-body"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log leaked %q: %s", secret, logged)
		}
	}
	for _, expected := range []string{`"service":"api"`, `"status":200`, `"password":"[REDACTED]"`} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("log missing %s: %s", expected, logged)
		}
	}
}

func TestNewLoggerHonorsMinimumLevel(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(&output, "worker", slog.LevelWarn)
	logger.Info("not written")
	logger.Warn("written")

	if strings.Contains(output.String(), "not written") || !strings.Contains(output.String(), "written") {
		t.Fatalf("unexpected level filtering: %s", output.String())
	}
}
