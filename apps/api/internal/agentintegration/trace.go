package agentintegration

import (
	"context"
	"encoding/json"
	"sync"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentruntime"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	maxRuntimeTraces     = 64
	maxRuntimeTraceBytes = 4 << 20
	maxRuntimeTraceTotal = 16 << 20

	traceStatusReady   = "ready"
	traceStatusPending = "pending"
	traceStatusMissing = "missing"
	traceStatusError   = "error"

	traceErrorSnapshotFailed   = "trace_snapshot_failed"
	traceErrorSizeExceeded     = "trace_size_exceeded"
	traceErrorCapacityExceeded = "trace_capacity_exceeded"
	traceErrorUnavailable      = "trace_unavailable"
)

type testRuntimeTrace struct {
	State      agentprotocol.RuntimeState    `json:"state"`
	Inputs     []agentprotocol.RuntimeInput  `json:"inputs"`
	Effects    []agentprotocol.RuntimeEffect `json:"effects"`
	ModelTurns int                           `json:"modelTurns"`
}

type testTraceDiagnostic struct {
	Status    string            `json:"status"`
	ErrorCode string            `json:"errorCode,omitempty"`
	Value     *testRuntimeTrace `json:"value,omitempty"`
}

type traceRecord struct {
	raw       json.RawMessage
	errorCode string
}

type runtimeTraceRecorder struct {
	mu       sync.Mutex
	records  map[uuid.UUID]traceRecord
	total    int
	overflow bool
	closed   bool
}

func newRuntimeTraceRecorder() *runtimeTraceRecorder {
	return &runtimeTraceRecorder{records: make(map[uuid.UUID]traceRecord)}
}

func (recorder *runtimeTraceRecorder) Record(runID uuid.UUID, trace *agentruntime.Trace) {
	if recorder == nil || runID == uuid.Nil || trace == nil {
		return
	}
	snapshot := testRuntimeTrace{
		State: trace.State, Inputs: trace.Inputs, Effects: trace.Effects, ModelTurns: trace.ModelTurns,
	}
	raw, err := json.Marshal(snapshot)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closed {
		return
	}
	if existing, ok := recorder.records[runID]; ok && len(existing.raw) > 0 {
		return
	}
	if _, ok := recorder.records[runID]; !ok && len(recorder.records) >= maxRuntimeTraces {
		recorder.overflow = true
		return
	}
	if err != nil {
		recorder.records[runID] = traceRecord{errorCode: traceErrorSnapshotFailed}
		return
	}
	if len(raw) > maxRuntimeTraceBytes {
		recorder.records[runID] = traceRecord{errorCode: traceErrorSizeExceeded}
		return
	}
	if recorder.total+len(raw) > maxRuntimeTraceTotal {
		recorder.records[runID] = traceRecord{errorCode: traceErrorCapacityExceeded}
		return
	}
	recorder.records[runID] = traceRecord{raw: raw}
	recorder.total += len(raw)
}

func (recorder *runtimeTraceRecorder) Diagnostic(runID uuid.UUID, terminal bool) testTraceDiagnostic {
	if recorder == nil {
		return testTraceDiagnostic{Status: traceStatusError, ErrorCode: traceErrorUnavailable}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closed {
		return testTraceDiagnostic{Status: traceStatusError, ErrorCode: traceErrorUnavailable}
	}
	if record, ok := recorder.records[runID]; ok {
		if record.errorCode != "" {
			return testTraceDiagnostic{Status: traceStatusError, ErrorCode: record.errorCode}
		}
		var value testRuntimeTrace
		if err := json.Unmarshal(record.raw, &value); err != nil {
			return testTraceDiagnostic{Status: traceStatusError, ErrorCode: traceErrorSnapshotFailed}
		}
		return testTraceDiagnostic{Status: traceStatusReady, Value: &value}
	}
	if recorder.overflow {
		return testTraceDiagnostic{Status: traceStatusError, ErrorCode: traceErrorCapacityExceeded}
	}
	if terminal {
		return testTraceDiagnostic{Status: traceStatusMissing}
	}
	return testTraceDiagnostic{Status: traceStatusPending}
}

func (recorder *runtimeTraceRecorder) Close() {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.records = nil
	recorder.total = 0
	recorder.overflow = false
	recorder.closed = true
}

type processWithTrace interface {
	ProcessWithTrace(context.Context, model.OutboxEvent) (*agentruntime.Trace, error)
}

type traceRecordingProcessor struct {
	base   processWithTrace
	traces *runtimeTraceRecorder
}

func (processor traceRecordingProcessor) Process(ctx context.Context, event model.OutboxEvent) error {
	trace, err := processor.base.ProcessWithTrace(ctx, event)
	processor.traces.Record(event.AggregateID, trace)
	return err
}
