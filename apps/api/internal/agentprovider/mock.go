package agentprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"sync"

	"dayorder.local/api/internal/agentprotocol"
)

// ScriptedProvider is deterministic test infrastructure. It performs no I/O.
type ScriptedProvider struct {
	mu           sync.Mutex
	scripts      [][]agentprotocol.ProviderEvent
	nextScript   int
	requestCount int
}

// NewScriptedProvider copies scripts for deterministic test execution.
func NewScriptedProvider(scripts [][]agentprotocol.ProviderEvent) *ScriptedProvider {
	return &ScriptedProvider{scripts: mustCopyScripts(scripts)}
}

// RequestCount reports the number of scripts requested from the Provider.
func (p *ScriptedProvider) RequestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requestCount
}

// Stream yields a copied script in event order and stops when ctx is done.
func (p *ScriptedProvider) Stream(ctx context.Context, _ agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		if ctx.Err() != nil {
			return
		}

		p.mu.Lock()
		p.requestCount++
		index := p.nextScript
		p.nextScript++
		var script []agentprotocol.ProviderEvent
		found := index < len(p.scripts)
		if found {
			script = mustCopyEvents(p.scripts[index])
		}
		p.mu.Unlock()

		if !found {
			event := agentprotocol.ProviderEvent{
				Type: agentprotocol.ProviderEventTypeError,
				Error: &agentprotocol.AgentError{
					Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "scripted provider exhausted", Retryable: false,
				},
			}
			if ctx.Err() == nil {
				yield(event, nil)
			}
			return
		}

		for _, event := range script {
			if ctx.Err() != nil || !yield(mustCopyEvent(event), nil) {
				return
			}
		}
	}
}

func mustCopyScripts(scripts [][]agentprotocol.ProviderEvent) [][]agentprotocol.ProviderEvent {
	raw, err := json.Marshal(scripts)
	if err != nil {
		panic(fmt.Sprintf("copy Provider scripts: %v", err))
	}
	var copied [][]agentprotocol.ProviderEvent
	if err := json.Unmarshal(raw, &copied); err != nil {
		panic(fmt.Sprintf("copy Provider scripts: %v", err))
	}
	return copied
}

func mustCopyEvents(events []agentprotocol.ProviderEvent) []agentprotocol.ProviderEvent {
	return mustCopyScripts([][]agentprotocol.ProviderEvent{events})[0]
}

func mustCopyEvent(event agentprotocol.ProviderEvent) agentprotocol.ProviderEvent {
	return mustCopyEvents([]agentprotocol.ProviderEvent{event})[0]
}
