package observability

import (
	"io"
	"log/slog"
	"strings"
)

const redactedValue = "[REDACTED]"

// NewLogger creates the process logger used by production binaries. Sensitive
// structured attributes are redacted at the handler boundary so a future log
// call cannot accidentally emit credentials or private user content.
func NewLogger(output io.Writer, service string, level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if sensitiveLogKey(attribute.Key) {
				attribute.Value = slog.StringValue(redactedValue)
			}
			return attribute
		},
	})
	return slog.New(handler).With("service", service)
}

func sensitiveLogKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	for _, fragment := range []string{
		"password", "passwd", "authorization", "cookie", "secret", "token",
		"session", "body", "content", "note", "payload", "hmac", "apikey",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
