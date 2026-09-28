package agentexecution

import "dayorder.local/api/internal/agentprotocol"

// Error carries a protocol-safe agent failure through internal service layers.
type Error struct {
	Agent agentprotocol.AgentError
}

func (err Error) Error() string {
	if err.Agent.Message != "" {
		return err.Agent.Message
	}
	return string(err.Agent.Code)
}
