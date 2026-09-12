package agentprovider

import (
	"context"
	"iter"
	"net/http"
	"time"

	"dayorder.local/api/internal/agentprotocol"
)

const (
	maxTurnOutputTokens = 2048
	maxRequestMessages  = 64
	maxRequestBodyBytes = 256 << 10
	maxSSEDataBytes     = 64 << 10
	maxTurnSSEDataBytes = 1 << 20
	maxJSONDepth        = 16
	providerIdleTimeout = 15 * time.Second
)

// TurnOptions contains the server-selected model and per-turn output budget.
type TurnOptions struct {
	Model           string
	MaxOutputTokens int
}

// Adapter is the server-only streaming Provider boundary.
type Adapter interface {
	Stream(context.Context, agentprotocol.ModelTurnRequest, TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error]
}

type DeepSeekConfig struct {
	Endpoint          string
	APIKey            string
	AllowLoopbackHTTP bool
	Client            *http.Client
}

type FakeConfig struct {
	Window             agentprotocol.CalendarReadInput
	RecoverToolTimeout bool
	Fault              string
}

// ProviderError is a classified server-side Provider failure.
type ProviderError struct {
	Code       agentprotocol.ErrorCode
	Status     int
	RetryAfter time.Duration
	Retryable  bool
	KnownUsage *agentprotocol.Usage `json:"-"`
}

func (failure *ProviderError) Error() string {
	if failure == nil || failure.Code == "" {
		return "provider_error"
	}
	return string(failure.Code)
}

func protocolError() error {
	return &ProviderError{Code: agentprotocol.ErrorCodeProtocolIncompatible, Retryable: false}
}
