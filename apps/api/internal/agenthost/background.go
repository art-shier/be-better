package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentbinding"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentgateway"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentruntime"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

const (
	backgroundMonitorInterval = 250 * time.Millisecond
	backgroundFinalizeWindow  = 5 * time.Second
	backgroundStaleWindow     = 5 * time.Minute
)

type Config struct {
	Runs     *service.AgentReadonlyService
	Calendar *service.AgentCalendarReadService
	Gateway  *agentgateway.Gateway
	Observer agentexecution.Observer
	Logger   *slog.Logger
}

type Background struct {
	runs       readonlyRuns
	calendar   *service.AgentCalendarReadService
	gateway    *agentgateway.Gateway
	profile    agentskill.SkillProfile
	cancelTurn func(uuid.UUID, error)
	observer   agentexecution.Observer
	logger     *slog.Logger
}

type readonlyRuns interface {
	Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) (agentexecution.Record, error)
	Authorize(context.Context, agentexecution.Actor, uuid.UUID) (agentexecution.Record, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (agentprotocol.ReadonlyRunView, error)
	Complete(context.Context, agentexecution.Actor, uuid.UUID, agentprotocol.RuntimeState, []model.AgentStepDraft) error
}

func NewBackground(config Config) (*Background, error) {
	if config.Runs == nil || config.Calendar == nil || config.Gateway == nil {
		return nil, errors.New("background runs, calendar, and gateway are required")
	}
	profile, err := agentskill.ParseBundle(agentassets.CalendarOverviewBundle())
	if err != nil {
		return nil, fmt.Errorf("load background calendar Skill: %w", err)
	}
	if config.Observer != nil {
		config.Calendar.SetObserver(config.Observer)
	}
	return &Background{
		runs: config.Runs, calendar: config.Calendar, gateway: config.Gateway,
		profile: profile, cancelTurn: config.Gateway.Cancel, observer: config.Observer, logger: config.Logger,
	}, nil
}

func (host *Background) Process(ctx context.Context, event model.OutboxEvent) error {
	_, err := host.ProcessWithTrace(ctx, event)
	return err
}

// ProcessWithTrace executes one background Run and returns the actual Runtime
// trace after persistence reconciliation has completed. Paths that do not run
// the Driver return a nil trace.
func (host *Background) ProcessWithTrace(ctx context.Context, event model.OutboxEvent) (*agentruntime.Trace, error) {
	runID, err := validateReadonlyEvent(event)
	if err != nil {
		return nil, err
	}
	record, err := host.runs.Claim(ctx, event.UserID, runID, event.LockToken, event.Attempts > 1)
	if err != nil {
		return nil, fmt.Errorf("claim readonly run: %w", err)
	}
	if readonlyTerminal(record.Run.Status) {
		return nil, nil
	}
	if err = validateClaimedReadonlyRecord(record, event); err != nil {
		return nil, err
	}
	admittedAt := time.Now()
	queueWait := admittedAt.Sub(record.Run.CreatedAt)
	if queueWait < 0 {
		queueWait = 0
	}
	host.observeSlot(record, "started", "", queueWait)
	slotOutcome, slotCode := "failed", string(agentprotocol.ErrorCodeInternalError)
	defer func() { host.observeSlot(record, slotOutcome, slotCode, 0) }()

	actor := agentexecution.Actor{UserID: event.UserID, Token: event.LockToken, Mode: agentprotocol.ExecutionModeBackground}
	tools, policy, err := host.tools(actor, record)
	if err != nil {
		return nil, err
	}
	driver, err := agentruntime.NewDriver(agentruntime.DriverConfig{
		Provider: host.gateway.ForRun(actor, runID), Tools: tools, Approvals: denyApprovals{},
		ModelProfile: record.Execution.ModelProfile, Policy: policy, Deadline: record.Execution.Deadline,
	})
	if err != nil {
		return nil, fmt.Errorf("construct background runtime Driver: %w", err)
	}
	state := agentruntime.NewState(agentruntime.Config{
		RunID: runID.String(), ExecutionMode: record.Execution.Mode,
		CapabilitySnapshot: record.Execution.Capabilities, Budget: record.Execution.Budget,
	})

	runCtx, stop := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stop(nil)
	done := make(chan struct{})
	go host.monitor(ctx, runCtx, stop, done, actor, runID)
	intent := record.Run.Intent
	trace, runErr := driver.Run(runCtx, state, agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: &intent})
	stop(nil)
	<-done
	slotOutcome, slotCode = runtimeSlotOutcome(trace.State, runErr)
	if runErr != nil {
		return &trace, host.reconcileRunError(actor.UserID, runID, runErr)
	}
	return &trace, host.complete(actor, runID, trace.State)
}

func (host *Background) observeSlot(record agentexecution.Record, outcome, code string, queueWait time.Duration) {
	observation := agentexecution.Observation{
		Kind: "background_slot", Mode: string(record.Execution.Mode), ModelProfile: record.Execution.ModelProfile,
		Outcome: outcome, ErrorCode: code, QueueWait: queueWait,
	}
	if host.observer != nil {
		host.observer.ObserveAgent(observation)
	}
	if host.logger == nil {
		return
	}
	message := "agent background slot released"
	if outcome == "started" {
		message = "agent background slot acquired"
	}
	host.logger.Info(message,
		"runId", record.Run.ID.String(), "mode", record.Execution.Mode, "profile", record.Execution.ModelProfile,
		"outcome", outcome, "errorCode", code, "queueWaitMs", queueWait.Milliseconds(),
		"protocolVersion", record.Execution.ProtocolVersion, "runtimeVersion", record.Execution.RuntimeVersion,
		"skillDigest", host.profile.Descriptor.Digest,
	)
}

func runtimeSlotOutcome(state agentprotocol.RuntimeState, runErr error) (string, string) {
	if runErr != nil {
		return "failed", string(agentprotocol.ErrorCodeInternalError)
	}
	switch state.Phase {
	case agentprotocol.RuntimePhaseCompleted:
		return "completed", ""
	case agentprotocol.RuntimePhaseCancelled:
		return "cancelled", string(agentprotocol.ErrorCodeCancelled)
	case agentprotocol.RuntimePhaseFailed:
		if state.Error != nil {
			return "failed", string(state.Error.Code)
		}
	}
	return "failed", string(agentprotocol.ErrorCodeInternalError)
}

func validateReadonlyEvent(event model.OutboxEvent) (uuid.UUID, error) {
	if event.ID == uuid.Nil || event.EventType != "agent.readonly.run.requested" || event.UserID == uuid.Nil ||
		event.AggregateType != "agent_run" || event.AggregateID == uuid.Nil || event.LockToken == uuid.Nil || event.Attempts < 1 {
		return uuid.Nil, errors.New("invalid readonly agent event")
	}
	var payload struct {
		FormatVersion int       `json:"formatVersion"`
		RunID         uuid.UUID `json:"runId"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.FormatVersion != 1 ||
		payload.RunID == uuid.Nil || payload.RunID != event.AggregateID {
		return uuid.Nil, errors.New("invalid readonly agent event payload")
	}
	return payload.RunID, nil
}

func validateClaimedReadonlyRecord(record agentexecution.Record, event model.OutboxEvent) error {
	if record.Run.ID != event.AggregateID || record.Execution.UserID != event.UserID ||
		record.Execution.RunID != event.AggregateID || record.Execution.Token != event.LockToken ||
		record.Execution.Mode != agentprotocol.ExecutionModeBackground ||
		record.Run.Status != string(agentprotocol.ReadonlyRunViewStatusAnalyzing) ||
		record.Execution.ProtocolVersion != "2.0" || record.Execution.RuntimeVersion != "2.0.0" ||
		record.Execution.ModelProfile == "" || record.Execution.Deadline.IsZero() {
		return errors.New("claimed readonly run does not match the event or runtime contract")
	}
	if time.Duration(record.Execution.Budget.MaxDurationMs)*time.Millisecond+backgroundFinalizeWindow >= backgroundStaleWindow {
		return errors.New("readonly runtime and finalization exceed the outbox stale window")
	}
	candidate := agentprotocol.RuntimeState{
		ProtocolVersion: record.Execution.ProtocolVersion, RunID: record.Run.ID.String(),
		ExecutionMode: record.Execution.Mode, Phase: agentprotocol.RuntimePhaseIdle,
		Messages: []agentprotocol.Message{}, CapabilitySnapshot: record.Execution.Capabilities,
		Budget: record.Execution.Budget, Usage: agentprotocol.Usage{},
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeState, candidate); err != nil {
		return fmt.Errorf("invalid frozen readonly runtime configuration: %w", err)
	}
	return nil
}

func (host *Background) tools(actor agentexecution.Actor, record agentexecution.Record) (*agenttool.Registry, agenttool.Policy, error) {
	if len(record.Execution.Capabilities.Skills) != 1 ||
		record.Execution.Capabilities.Skills[0].Name != host.profile.Descriptor.Name ||
		record.Execution.Capabilities.Skills[0].Version != host.profile.Descriptor.Version ||
		record.Execution.Capabilities.Skills[0].Digest != host.profile.Descriptor.Digest {
		return nil, agenttool.Policy{}, errors.New("frozen readonly Skill does not match the builtin asset")
	}
	skills, err := agentskill.NewRegistry([]agentskill.SkillProfile{host.profile})
	if err != nil {
		return nil, agenttool.Policy{}, fmt.Errorf("construct background Skill Registry: %w", err)
	}
	policy := agenttool.Policy{Allow: []string{"*"}}
	tools := &agenttool.Registry{}
	calendar, err := agentbinding.NewCalendar(actor, record.Run.ID, host.calendar)
	if err != nil {
		return nil, agenttool.Policy{}, fmt.Errorf("bind background calendar Tool: %w", err)
	}
	bindings := append([]agenttool.Binding{calendar}, agentskill.MetaBindings(skills, record.Execution.Capabilities, tools, policy)...)
	for _, binding := range bindings {
		if err = tools.Register(binding); err != nil {
			return nil, agenttool.Policy{}, fmt.Errorf("register background Tool: %w", err)
		}
	}
	want := slices.Clone(record.Execution.Capabilities.ToolIds)
	if got := agenttool.EffectiveToolIDs(want, tools, record.Execution.Capabilities, policy); !slices.Equal(got, []string{"dayorder.calendar.read", "skill_list", "skill_load"}) {
		return nil, agenttool.Policy{}, errors.New("frozen readonly Tool grant does not resolve to the builtin bindings")
	}
	return tools, policy, nil
}

func (host *Background) monitor(parent, runCtx context.Context, stop context.CancelCauseFunc, done chan<- struct{}, actor agentexecution.Actor, runID uuid.UUID) {
	defer close(done)
	parentDone := make(chan struct{})
	go func() {
		defer close(parentDone)
		select {
		case <-parent.Done():
			host.stop(runID, stop, "interrupted")
		case <-runCtx.Done():
		}
	}()
	defer func() { <-parentDone }()
	ticker := time.NewTicker(backgroundMonitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			if _, err := host.runs.Authorize(runCtx, actor, runID); err == nil {
				continue
			}
			if runCtx.Err() != nil {
				return
			}
			kind := "interrupted"
			view, getErr := host.runs.Get(runCtx, actor.UserID, runID)
			if getErr == nil {
				switch {
				case view.Status == agentprotocol.ReadonlyRunViewStatusStopped:
					kind = "user"
				case view.Status == agentprotocol.ReadonlyRunViewStatusFailed && view.Error != nil && view.Error.Code == agentprotocol.ErrorCodeTimeout:
					kind = "timeout"
				}
			}
			host.stop(runID, stop, kind)
			return
		}
	}
}

func (host *Background) stop(runID uuid.UUID, stop context.CancelCauseFunc, kind string) {
	switch kind {
	case "user":
		host.cancelTurn(runID, context.Canceled)
	case "timeout":
		host.cancelTurn(runID, context.DeadlineExceeded)
	default:
		host.cancelTurn(runID, &agentexecution.Error{Agent: agentprotocol.AgentError{
			Code: agentprotocol.ErrorCodeInternalError, Message: "execution_interrupted", Retryable: false,
		}})
	}
	stop(agentruntime.StopCause{Kind: kind})
}

func (host *Background) reconcileRunError(userID, runID uuid.UUID, runErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), backgroundFinalizeWindow)
	defer cancel()
	if terminal, _ := host.durableTerminal(ctx, userID, runID); terminal {
		return nil
	}
	return fmt.Errorf("run background readonly agent: %w", runErr)
}

func (host *Background) complete(actor agentexecution.Actor, runID uuid.UUID, state agentprotocol.RuntimeState) error {
	ctx, cancel := context.WithTimeout(context.Background(), backgroundFinalizeWindow)
	defer cancel()
	for {
		err := host.runs.Complete(ctx, actor, runID, state, nil)
		if err == nil {
			return nil
		}
		terminal, getErr := host.durableTerminal(ctx, actor.UserID, runID)
		if getErr == nil && terminal {
			return nil
		}
		if errors.Is(err, service.ErrValidation) || errors.Is(err, model.ErrConflict) {
			return fmt.Errorf("complete background readonly run: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("complete background readonly run: %w", err)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (host *Background) durableTerminal(ctx context.Context, userID, runID uuid.UUID) (bool, error) {
	view, err := host.runs.Get(ctx, userID, runID)
	if err != nil {
		return false, err
	}
	return readonlyTerminal(string(view.Status)), nil
}

func readonlyTerminal(status string) bool {
	return status == string(agentprotocol.ReadonlyRunViewStatusCompleted) ||
		status == string(agentprotocol.ReadonlyRunViewStatusFailed) ||
		status == string(agentprotocol.ReadonlyRunViewStatusStopped)
}

type denyApprovals struct{}

func (denyApprovals) Request(context.Context, string, agentprotocol.ToolCall) (agentruntime.Decision, error) {
	return agentruntime.DecisionDeny, nil
}
