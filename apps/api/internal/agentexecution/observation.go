package agentexecution

import (
	"time"

	"dayorder.local/api/internal/agentprotocol"
)

type Observation struct {
	Kind          string
	Mode          string
	ToolID        string
	ModelProfile  string
	Outcome       string
	ErrorCode     string
	Duration      time.Duration
	QueueWait     time.Duration
	CancelLatency time.Duration
	Usage         agentprotocol.Usage
	UsageComplete bool
	Attempts      int
}

type Observer interface {
	ObserveAgent(Observation)
}
