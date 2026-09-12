package agentgateway

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type registryEntry struct {
	turn         *Turn
	pendingCause error
}

type turnRegistry struct {
	mu      sync.Mutex
	entries map[uuid.UUID]registryEntry
}

func newTurnRegistry() *turnRegistry {
	return &turnRegistry{entries: make(map[uuid.UUID]registryEntry)}
}

func (registry *turnRegistry) reserve(runID uuid.UUID) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.entries[runID]; exists {
		return false
	}
	registry.entries[runID] = registryEntry{}
	return true
}

func (registry *turnRegistry) activate(runID uuid.UUID, turn *Turn) error {
	registry.mu.Lock()
	entry, exists := registry.entries[runID]
	if exists {
		entry.turn = turn
		registry.entries[runID] = entry
	}
	registry.mu.Unlock()
	if !exists {
		return context.Canceled
	}
	if entry.pendingCause != nil {
		turn.cancelExplicit(entry.pendingCause)
	}
	return nil
}

func (registry *turnRegistry) release(runID uuid.UUID, turn *Turn) {
	registry.mu.Lock()
	entry, exists := registry.entries[runID]
	if exists && (turn == nil || entry.turn == turn) {
		delete(registry.entries, runID)
	}
	registry.mu.Unlock()
}

func (registry *turnRegistry) cancel(runID uuid.UUID, cause error) {
	registry.mu.Lock()
	entry, exists := registry.entries[runID]
	if exists && entry.turn == nil {
		entry.pendingCause = cause
		registry.entries[runID] = entry
	}
	registry.mu.Unlock()
	if exists && entry.turn != nil {
		entry.turn.cancelExplicit(cause)
	}
}
