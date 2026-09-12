// Package agentprovider defines Provider boundaries for the Agent Runtime.
package agentprovider

import (
	"context"
	"iter"

	"dayorder.local/api/internal/agentprotocol"
)

// StreamProvider emits one normalized event stream for a model turn.
type StreamProvider interface {
	Stream(context.Context, agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error]
}
