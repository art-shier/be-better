package agentexecution

import (
	"time"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type ExecutionMode = agentprotocol.ExecutionMode
type CapabilitySnapshot = agentprotocol.CapabilitySnapshot
type Budget = agentprotocol.Budget
type Usage = agentprotocol.Usage

type Record struct {
	Run       model.AgentRun
	Execution Execution
}

type Execution struct {
	UserID          uuid.UUID
	RunID           uuid.UUID
	Token           uuid.UUID
	Mode            ExecutionMode
	ProtocolVersion string
	RuntimeVersion  string
	ModelProfile    string
	Timezone        string
	ResultOrigin    string
	Capabilities    CapabilitySnapshot
	Budget          Budget
	Deadline        time.Time
	KnownUsage      Usage
	ReservedTokens  int
	UsageComplete   bool
}

type Operation struct {
	UserID         uuid.UUID
	RunID          uuid.UUID
	Kind           string
	ID             string
	State          string
	Hash           [32]byte
	Attempts       int
	ReservedTokens int
	Usage          Usage
	UsageComplete  bool
	ErrorCode      string
	StartedAt      time.Time
	FinishedAt     time.Time
}
