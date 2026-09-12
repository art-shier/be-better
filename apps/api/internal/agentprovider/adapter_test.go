package agentprovider

import (
	"strings"
	"testing"
	"time"

	"dayorder.local/api/internal/agentprotocol"
)

func TestProviderErrorOnlyExposesSafeCategory(t *testing.T) {
	err := (&ProviderError{Code: agentprotocol.ErrorCodeProviderRateLimited, Status: 429, RetryAfter: time.Minute, Retryable: true}).Error()
	if err != "provider_rate_limited" || strings.Contains(err, "429") {
		t.Fatalf("Error() = %q", err)
	}
}
