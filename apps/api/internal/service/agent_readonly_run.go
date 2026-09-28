package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	readonlyProtocolVersion = "2.0"
	readonlyRuntimeVersion  = "2.0.0"

	readonlyCancelStageCommandStart      = "command_start"
	readonlyCancelStageLoadRun           = "load_run"
	readonlyCancelStageSaveTimeout       = "save_timeout"
	readonlyCancelStageSaveCancel        = "save_cancel"
	readonlyCancelStageOperationReturned = "command_operation_returned"
	readonlyCancelStageDecodeResponse    = "decode_response"

	readonlyCancelErrorInternal         = "internal"
	readonlyCancelErrorContextCanceled  = "context_canceled"
	readonlyCancelErrorDeadlineExceeded = "deadline_exceeded"

	readonlyCancelContextActive           = "active"
	readonlyCancelContextCanceled         = "canceled"
	readonlyCancelContextDeadlineExceeded = "deadline_exceeded"
)

var defaultReadonlyBudget = agentprotocol.Budget{
	MaxSteps: 8, MaxTokens: 16000, MaxDurationMs: 120000,
	MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2,
}

type AgentReadonlyConfig struct {
	Store        agentexecution.Store
	Transactor   UserTransactor
	Commands     *CommandService
	SyncWriter   CommandSyncWriter
	AuditWriter  CommandAuditWriter
	Capabilities map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot
	Profiles     []string
	Now          func() time.Time
	Budget       agentprotocol.Budget
	Observer     agentexecution.Observer
	Logger       *slog.Logger
}

type AgentReadonlyService struct {
	store        agentexecution.Store
	transactor   UserTransactor
	commands     *CommandService
	syncWriter   CommandSyncWriter
	auditWriter  CommandAuditWriter
	capabilities map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot
	profiles     []string
	now          func() time.Time
	budget       agentprotocol.Budget
	observer     agentexecution.Observer
	logger       *slog.Logger
}

func NewAgentReadonlyService(config AgentReadonlyConfig) (*AgentReadonlyService, error) {
	if config.Store == nil || config.Transactor == nil || config.Commands == nil || config.SyncWriter == nil || config.AuditWriter == nil {
		return nil, errors.New("readonly run store, transactor, commands, sync, and audit are required")
	}
	budget, err := readonlyBudget(config.Budget)
	if err != nil {
		return nil, err
	}
	capabilities, err := cloneReadonlyCapabilities(config.Capabilities)
	if err != nil {
		return nil, err
	}
	profiles, err := readonlyProfiles(config.Profiles)
	if err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &AgentReadonlyService{
		store: config.Store, transactor: config.Transactor, commands: config.Commands,
		syncWriter: config.SyncWriter, auditWriter: config.AuditWriter,
		capabilities: capabilities, profiles: profiles, now: now, budget: budget,
		observer: config.Observer, logger: config.Logger,
	}, nil
}

func (service *AgentReadonlyService) Create(ctx context.Context, mutation MutationContext, input agentprotocol.ReadonlyRunStart) (agentprotocol.ReadonlyRunView, error) {
	if service == nil {
		return agentprotocol.ReadonlyRunView{}, errors.New("readonly run service is required")
	}
	if err := agentexecution.ValidateScope(input.Scope); err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionReadonlyRunStart, input); err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if strings.TrimSpace(input.Intent) == "" {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: readonly run intent is required", ErrValidation)
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: invalid readonly run timezone", ErrValidation)
	}
	if !slices.Contains(service.profiles, input.ModelProfile) {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: unsupported readonly model profile", ErrValidation)
	}
	baseCapabilities, ok := service.capabilities[input.ExecutionMode]
	if !ok {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: unsupported readonly execution mode", ErrValidation)
	}

	runID := uuid.New()
	capabilities := cloneCapabilitySnapshot(baseCapabilities)
	capabilities.Scope = cloneAgentScope(input.Scope)
	if err := agentprotocol.Validate(agentprotocol.DefinitionCapabilitySnapshot, capabilities); err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: invalid readonly capabilities: %v", ErrValidation, err)
	}
	scope, err := json.Marshal(input.Scope)
	if err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("encode readonly scope: %w", err)
	}
	resultOrigin := agentprotocol.ReadonlyRunViewResultOriginServerRuntime
	if input.ExecutionMode == agentprotocol.ExecutionModeForeground {
		resultOrigin = agentprotocol.ReadonlyRunViewResultOriginClientReported
	}
	requestBody, err := json.Marshal(input)
	if err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("encode readonly run request: %w", err)
	}
	var view agentprotocol.ReadonlyRunView
	var timedOut []agentexecution.Record
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.readonly.create", requestBody, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		if err := service.store.LockAccount(ctx, tx, mutation.UserID); err != nil {
			return CommandResult{}, err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		record := agentexecution.Record{
			Run: model.AgentRun{
				ID: runID, Intent: input.Intent, Status: string(agentprotocol.ReadonlyRunViewStatusReady),
				ActionMode: "read", Scope: scope, Version: 1, CreatedAt: now, UpdatedAt: now,
			},
			Execution: agentexecution.Execution{
				UserID: mutation.UserID, RunID: runID, Mode: input.ExecutionMode,
				ProtocolVersion: readonlyProtocolVersion, RuntimeVersion: readonlyRuntimeVersion,
				ModelProfile: input.ModelProfile, Timezone: input.Timezone, ResultOrigin: string(resultOrigin),
				Capabilities: cloneCapabilitySnapshot(capabilities), Budget: service.budget,
				Deadline:      now.Add(time.Duration(service.budget.MaxDurationMs) * time.Millisecond),
				UsageComplete: true,
			},
		}
		view = readonlyRunView(record, now)
		body, encodeErr := json.Marshal(view)
		if encodeErr != nil {
			return CommandResult{}, fmt.Errorf("encode readonly run view: %w", encodeErr)
		}
		changes := make([]model.SyncChangeDraft, 0, 2)
		audits := make([]model.AuditDraft, 0, 2)
		active, err := service.store.Active(ctx, tx, mutation.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		for index := range active {
			if !deadlineExpired(active[index], now) {
				return CommandResult{}, model.ErrConflict
			}
			expectedVersion, expectedToken := active[index].Run.Version, active[index].Execution.Token
			timeoutReadonlyRecord(&active[index], now)
			if err = service.store.Save(ctx, tx, active[index], expectedVersion, expectedToken); err != nil {
				return CommandResult{}, err
			}
			active[index].Run.Version = expectedVersion + 1
			timedOut = append(timedOut, active[index])
			changes = append(changes, readonlyRunChange(active[index]))
			audits = append(audits, readonlyRunAudit("agent.readonly.timeout", active[index].Run.ID, "system"))
		}
		created, err := service.store.CountCreatedSince(ctx, tx, mutation.UserID, now.Add(-time.Minute))
		if err != nil {
			return CommandResult{}, err
		}
		if created >= 10 {
			if len(changes) > 0 {
				return CommandResult{Status: 409, Body: json.RawMessage(`{"error":"conflict"}`), Changes: changes, Audits: audits}, nil
			}
			return CommandResult{}, model.ErrConflict
		}
		if err := service.store.Create(ctx, tx, record); err != nil {
			return CommandResult{}, err
		}
		changes = append(changes, model.SyncChangeDraft{EntityType: "agent_run", EntityID: runID, Operation: "create", EntityVersion: 1})
		audits = append(audits, model.AuditDraft{Action: "agent.readonly.create", Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: runID}}})
		return CommandResult{
			Status: 201, Body: body, Outbox: agentexecution.RunOutbox(input.ExecutionMode, runID),
			Changes: changes, Audits: audits,
		}, nil
	})
	if err != nil {
		return agentprotocol.ReadonlyRunView{}, err
	}
	for _, terminal := range timedOut {
		service.observeRunTerminal(terminal)
	}
	if response.Status == 409 {
		return agentprotocol.ReadonlyRunView{}, model.ErrConflict
	}
	if err = json.Unmarshal(response.Body, &view); err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("decode readonly run response: %w", err)
	}
	return view, nil
}

func (service *AgentReadonlyService) Get(ctx context.Context, userID, runID uuid.UUID) (agentprotocol.ReadonlyRunView, error) {
	if service == nil || userID == uuid.Nil || runID == uuid.Nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: readonly run user and run IDs are required", ErrValidation)
	}
	var record agentexecution.Record
	var now time.Time
	timedOut := false
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var err error
		record, err = service.store.Get(ctx, tx, userID, runID, true)
		if err != nil {
			return err
		}
		now = service.now().UTC().Truncate(time.Microsecond)
		if isReadonlyTerminal(record.Run.Status) || !deadlineExpired(record, now) {
			return nil
		}
		expectedVersion, expectedToken := record.Run.Version, record.Execution.Token
		timeoutReadonlyRecord(&record, now)
		if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, expectedToken, "agent.readonly.timeout", "system"); err != nil {
			return err
		}
		timedOut = true
		return nil
	})
	if err != nil {
		return agentprotocol.ReadonlyRunView{}, err
	}
	if timedOut {
		service.observeRunTerminal(record)
	}
	return readonlyRunView(record, now), nil
}

func (service *AgentReadonlyService) Cancel(ctx context.Context, mutation MutationContext, runID uuid.UUID, expectedVersion int64) (agentprotocol.ReadonlyRunView, error) {
	if service == nil || runID == uuid.Nil || expectedVersion < 1 {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: readonly run ID and expected version are required", ErrValidation)
	}
	if mutation.RequestID == uuid.Nil {
		mutation.RequestID = uuid.New()
	}
	requestBody, _ := json.Marshal(struct {
		RunID           uuid.UUID `json:"runId"`
		ExpectedVersion int64     `json:"expectedVersion"`
	}{RunID: runID, ExpectedVersion: expectedVersion})
	var terminal *agentexecution.Record
	lastStage := readonlyCancelStageCommandStart
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.readonly.cancel", requestBody, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		lastStage = readonlyCancelStageLoadRun
		record, err := service.store.Get(ctx, tx, mutation.UserID, runID, true)
		if err != nil {
			return CommandResult{}, err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if isReadonlyTerminal(record.Run.Status) {
			return CommandResult{}, model.ErrConflict
		}
		if record.Run.Version != expectedVersion {
			return CommandResult{}, model.ErrConflict
		}
		if deadlineExpired(record, now) {
			timeoutReadonlyRecord(&record, now)
			lastStage = readonlyCancelStageSaveTimeout
			if err = service.store.Save(ctx, tx, record, expectedVersion, record.Execution.Token); err != nil {
				return CommandResult{}, err
			}
			record.Run.Version = expectedVersion + 1
			terminal = &record
			lastStage = readonlyCancelStageOperationReturned
			return readonlyCommandResult(409, record, now, "agent.readonly.timeout"), nil
		}
		stopReadonlyRecord(&record, now, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "cancelled"})
		lastStage = readonlyCancelStageSaveCancel
		if err = service.store.Save(ctx, tx, record, expectedVersion, record.Execution.Token); err != nil {
			return CommandResult{}, err
		}
		record.Run.Version = expectedVersion + 1
		terminal = &record
		lastStage = readonlyCancelStageOperationReturned
		return readonlyCommandResult(200, record, now, "agent.readonly.cancel"), nil
	})
	if err != nil {
		if !readonlyCancelExpectedError(err) {
			service.observeReadonlyCancelFailure(ctx, mutation, runID, lastStage, err)
		}
		return agentprotocol.ReadonlyRunView{}, err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	var view agentprotocol.ReadonlyRunView
	lastStage = readonlyCancelStageDecodeResponse
	if err = json.Unmarshal(response.Body, &view); err != nil {
		err = fmt.Errorf("decode readonly cancel response: %w", err)
		service.observeReadonlyCancelFailure(ctx, mutation, runID, lastStage, err)
		return agentprotocol.ReadonlyRunView{}, err
	}
	if response.Status == 409 {
		return view, model.ErrConflict
	}
	return view, nil
}

func (service *AgentReadonlyService) observeReadonlyCancelFailure(ctx context.Context, mutation MutationContext, runID uuid.UUID, lastStage string, err error) {
	if service == nil || service.logger == nil {
		return
	}
	service.logger.Error("agent readonly cancellation failed",
		"runId", runID.String(), "requestId", mutation.RequestID.String(),
		"lastStage", lastStage, "errorCategory", readonlyCancelErrorCategory(err),
		"contextState", readonlyCancelContextState(ctx),
	)
}

func readonlyCancelExpectedError(err error) bool {
	return errors.Is(err, model.ErrNotFound) || errors.Is(err, model.ErrConflict) ||
		errors.Is(err, model.ErrDeviceNotActive) || errors.Is(err, ErrIdempotencyConflict) ||
		errors.Is(err, ErrValidation)
}

func readonlyCancelErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return readonlyCancelErrorContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return readonlyCancelErrorDeadlineExceeded
	default:
		return readonlyCancelErrorInternal
	}
}

func readonlyCancelContextState(ctx context.Context) string {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return readonlyCancelContextCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return readonlyCancelContextDeadlineExceeded
	default:
		return readonlyCancelContextActive
	}
}

func (service *AgentReadonlyService) Finish(ctx context.Context, mutation MutationContext, runID uuid.UUID, expectedVersion int64, input agentprotocol.ReadonlyRunFinish) (agentprotocol.ReadonlyRunView, error) {
	if service == nil || runID == uuid.Nil || expectedVersion < 1 {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("%w: readonly run ID and expected version are required", ErrValidation)
	}
	if err := validateReadonlyFinish(input); err != nil {
		return agentprotocol.ReadonlyRunView{}, err
	}
	requestBody, _ := json.Marshal(struct {
		RunID           uuid.UUID                       `json:"runId"`
		ExpectedVersion int64                           `json:"expectedVersion"`
		Finish          agentprotocol.ReadonlyRunFinish `json:"finish"`
	}{RunID: runID, ExpectedVersion: expectedVersion, Finish: input})
	var terminal *agentexecution.Record
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.readonly.finish", requestBody, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		record, err := service.store.Get(ctx, tx, mutation.UserID, runID, true)
		if err != nil {
			return CommandResult{}, err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if record.Execution.Mode != agentprotocol.ExecutionModeForeground {
			return CommandResult{}, fmt.Errorf("%w: only foreground readonly runs accept client finish", ErrValidation)
		}
		if isReadonlyTerminal(record.Run.Status) || record.Run.Version != expectedVersion {
			return CommandResult{}, model.ErrConflict
		}
		if deadlineExpired(record, now) {
			timeoutReadonlyRecord(&record, now)
			if err = service.store.Save(ctx, tx, record, expectedVersion, record.Execution.Token); err != nil {
				return CommandResult{}, err
			}
			record.Run.Version = expectedVersion + 1
			terminal = &record
			return readonlyCommandResult(409, record, now, "agent.readonly.timeout"), nil
		}
		finishReadonlyRecord(&record, input.Phase, input.Summary, input.Error, now)
		record.Execution.ResultOrigin = string(agentprotocol.ReadonlyRunViewResultOriginClientReported)
		if err = service.store.Save(ctx, tx, record, expectedVersion, record.Execution.Token); err != nil {
			return CommandResult{}, err
		}
		record.Run.Version = expectedVersion + 1
		steps := make([]model.AgentStepDraft, len(input.Steps))
		for index, step := range input.Steps {
			steps[index] = model.AgentStepDraft{Title: step.Title, Detail: step.Detail, Metadata: json.RawMessage(`{}`)}
		}
		if err = service.store.AddSteps(ctx, tx, mutation.UserID, runID, steps); err != nil {
			return CommandResult{}, err
		}
		terminal = &record
		return readonlyCommandResult(200, record, now, "agent.readonly.finish"), nil
	})
	if err != nil {
		return agentprotocol.ReadonlyRunView{}, err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	var view agentprotocol.ReadonlyRunView
	if err = json.Unmarshal(response.Body, &view); err != nil {
		return agentprotocol.ReadonlyRunView{}, fmt.Errorf("decode readonly finish response: %w", err)
	}
	if response.Status == 409 {
		return view, model.ErrConflict
	}
	return view, nil
}

func (service *AgentReadonlyService) Claim(ctx context.Context, userID, runID, newToken uuid.UUID, redelivered bool) (agentexecution.Record, error) {
	if service == nil || userID == uuid.Nil || runID == uuid.Nil || newToken == uuid.Nil {
		return agentexecution.Record{}, fmt.Errorf("%w: readonly claim user, run, and token are required", ErrValidation)
	}
	var record agentexecution.Record
	var terminal *agentexecution.Record
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var err error
		record, err = service.store.Get(ctx, tx, userID, runID, true)
		if err != nil {
			return err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if record.Execution.Mode != agentprotocol.ExecutionModeBackground {
			return fmt.Errorf("%w: foreground readonly runs cannot be claimed", ErrValidation)
		}
		if isReadonlyTerminal(record.Run.Status) {
			return nil
		}
		expectedVersion, expectedToken := record.Run.Version, record.Execution.Token
		if expectedToken != uuid.Nil && !redelivered {
			return model.ErrConflict
		}
		record.Execution.Token = newToken
		if deadlineExpired(record, now) {
			timeoutReadonlyRecord(&record, now)
			if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, expectedToken, "agent.readonly.timeout", "system"); err != nil {
				return err
			}
			terminal = &record
			return nil
		}
		if expectedToken != uuid.Nil {
			failReadonlyRecord(&record, now, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "execution_interrupted"})
			if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, expectedToken, "agent.readonly.fail", "agent"); err != nil {
				return err
			}
			terminal = &record
			return nil
		}
		record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusAnalyzing)
		if record.Run.StartedAt == nil {
			started := now
			record.Run.StartedAt = &started
		}
		return service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, expectedToken, "agent.readonly.claim", "agent")
	})
	if err != nil {
		return agentexecution.Record{}, err
	}
	if terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return record, nil
}

func (service *AgentReadonlyService) Complete(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, state agentprotocol.RuntimeState, steps []model.AgentStepDraft) error {
	if service == nil || runID == uuid.Nil {
		return fmt.Errorf("%w: readonly run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return err
	}
	if actor.Mode != agentprotocol.ExecutionModeBackground {
		return fmt.Errorf("%w: readonly completion is background-only", ErrValidation)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeState, state); err != nil {
		return fmt.Errorf("%w: invalid readonly runtime state: %v", ErrValidation, err)
	}
	if err := validateReadonlySteps(steps); err != nil {
		return err
	}
	if state.Phase != agentprotocol.RuntimePhaseCompleted && state.Phase != agentprotocol.RuntimePhaseFailed && state.Phase != agentprotocol.RuntimePhaseCancelled {
		return fmt.Errorf("%w: readonly completion requires a terminal runtime state", ErrValidation)
	}
	var terminal *agentexecution.Record
	err := service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, err := service.store.Get(ctx, tx, actor.UserID, runID, true)
		if err != nil {
			return err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if err = authorizeReadonlyActor(record, actor); err != nil {
			return err
		}
		if isReadonlyTerminal(record.Run.Status) {
			return nil
		}
		if !readonlyRuntimeMatches(record, state) {
			return fmt.Errorf("%w: runtime state does not match frozen readonly execution", ErrValidation)
		}
		expectedVersion := record.Run.Version
		if deadlineExpired(record, now) {
			timeoutReadonlyRecord(&record, now)
			if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, actor.Token, "agent.readonly.timeout", "system"); err != nil {
				return err
			}
			terminal = &record
			return nil
		}
		if err = projectReadonlyRuntime(&record, state, now); err != nil {
			return err
		}
		if err = service.store.Save(ctx, tx, record, expectedVersion, actor.Token); err != nil {
			return err
		}
		record.Run.Version = expectedVersion + 1
		if err = service.store.AddSteps(ctx, tx, actor.UserID, runID, preparedReadonlySteps(steps)); err != nil {
			return err
		}
		if err = service.recordReadonlyTransition(ctx, tx, record, "agent.readonly.complete", "agent"); err != nil {
			return err
		}
		terminal = &record
		return nil
	})
	if err == nil && terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return err
}

func (service *AgentReadonlyService) Fail(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, runError agentprotocol.AgentError) error {
	if service == nil || runID == uuid.Nil {
		return fmt.Errorf("%w: readonly run ID is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionAgentError, runError); err != nil {
		return fmt.Errorf("%w: invalid readonly run error: %v", ErrValidation, err)
	}
	var terminal *agentexecution.Record
	err := service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, err := service.store.Get(ctx, tx, actor.UserID, runID, true)
		if err != nil {
			return err
		}
		now := service.now().UTC().Truncate(time.Microsecond)
		if err = authorizeReadonlyActor(record, actor); err != nil {
			return err
		}
		if isReadonlyTerminal(record.Run.Status) {
			return nil
		}
		expectedVersion := record.Run.Version
		if deadlineExpired(record, now) {
			timeoutReadonlyRecord(&record, now)
			if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, actor.Token, "agent.readonly.timeout", "system"); err != nil {
				return err
			}
			terminal = &record
			return nil
		}
		if runError.Code == agentprotocol.ErrorCodeCancelled {
			stopReadonlyRecord(&record, now, runError)
		} else {
			failReadonlyRecord(&record, now, runError)
		}
		if err = service.saveReadonlyTransition(ctx, tx, &record, expectedVersion, actor.Token, "agent.readonly.fail", "agent"); err != nil {
			return err
		}
		terminal = &record
		return nil
	})
	if err == nil && terminal != nil {
		service.observeRunTerminal(*terminal)
	}
	return err
}

func (service *AgentReadonlyService) CancellationLatency(ctx context.Context, actor agentexecution.Actor, runID uuid.UUID, dependencyExitedAt time.Time) (time.Duration, bool, error) {
	if service == nil || runID == uuid.Nil || dependencyExitedAt.IsZero() {
		return 0, false, fmt.Errorf("%w: readonly cancellation observation metadata is required", ErrValidation)
	}
	if err := validateReadonlyActor(actor); err != nil {
		return 0, false, err
	}
	var requestedAt time.Time
	err := service.transactor.WithUser(ctx, actor.UserID, func(ctx context.Context, tx database.Tx) error {
		record, err := service.store.Get(ctx, tx, actor.UserID, runID, false)
		if err != nil {
			return err
		}
		if err = authorizeReadonlyActor(record, actor); err != nil {
			return err
		}
		if record.Run.Status != string(agentprotocol.ReadonlyRunViewStatusStopped) || record.Run.ErrorCode == nil ||
			*record.Run.ErrorCode != string(agentprotocol.ErrorCodeCancelled) || record.Run.FinishedAt == nil ||
			record.Run.FinishedAt.IsZero() || record.Run.FinishedAt.After(dependencyExitedAt) {
			return nil
		}
		requestedAt = *record.Run.FinishedAt
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	if requestedAt.IsZero() {
		return 0, false, nil
	}
	return dependencyExitedAt.Sub(requestedAt), true, nil
}

func (service *AgentReadonlyService) observeRunTerminal(record agentexecution.Record) {
	if service == nil {
		return
	}
	finished := record.Run.FinishedAt
	if finished == nil {
		return
	}
	started := record.Run.CreatedAt
	if record.Run.StartedAt != nil {
		started = *record.Run.StartedAt
	}
	duration := finished.Sub(started)
	if duration < 0 {
		duration = 0
	}
	outcome := "failed"
	switch record.Run.Status {
	case string(agentprotocol.ReadonlyRunViewStatusCompleted):
		outcome = "completed"
	case string(agentprotocol.ReadonlyRunViewStatusStopped):
		outcome = "cancelled"
	}
	code := ""
	if record.Run.ErrorCode != nil {
		code = *record.Run.ErrorCode
	}
	observation := agentexecution.Observation{
		Kind: "run", Mode: string(record.Execution.Mode), ModelProfile: record.Execution.ModelProfile,
		Outcome: outcome, ErrorCode: code, Duration: duration,
		Usage: record.Execution.KnownUsage, UsageComplete: record.Execution.UsageComplete,
	}
	if service.observer != nil {
		service.observer.ObserveAgent(observation)
	}
	if service.logger != nil {
		service.logger.Info("agent run completed",
			"runId", record.Run.ID.String(), "mode", record.Execution.Mode,
			"profile", record.Execution.ModelProfile, "outcome", outcome, "errorCode", code,
			"protocolVersion", record.Execution.ProtocolVersion, "runtimeVersion", record.Execution.RuntimeVersion,
			"skillDigest", record.Execution.Capabilities.Skills[0].Digest,
			"durationMs", duration.Milliseconds(), "inputTokens", record.Execution.KnownUsage.InputTokens,
			"outputTokens", record.Execution.KnownUsage.OutputTokens, "totalTokens", record.Execution.KnownUsage.TotalTokens,
			"usageComplete", record.Execution.UsageComplete,
		)
	}
}

func (service *AgentReadonlyService) saveReadonlyTransition(ctx context.Context, tx database.Tx, record *agentexecution.Record, expectedVersion int64, expectedToken uuid.UUID, action, actorType string) error {
	if err := service.store.Save(ctx, tx, *record, expectedVersion, expectedToken); err != nil {
		return err
	}
	record.Run.Version = expectedVersion + 1
	return service.recordReadonlyTransition(ctx, tx, *record, action, actorType)
}

func (service *AgentReadonlyService) recordReadonlyTransition(ctx context.Context, tx database.Tx, record agentexecution.Record, action, actorType string) error {
	if err := service.syncWriter.Record(ctx, tx, record.Execution.UserID, []model.SyncChangeDraft{readonlyRunChange(record)}); err != nil {
		return err
	}
	return service.auditWriter.Record(ctx, tx, record.Execution.UserID, []model.AuditDraft{readonlyRunAudit(action, record.Run.ID, actorType)})
}

func readonlyCommandResult(status int, record agentexecution.Record, now time.Time, action string) CommandResult {
	body, _ := json.Marshal(readonlyRunView(record, now))
	actorType := ""
	if action == "agent.readonly.timeout" {
		actorType = "system"
	}
	return CommandResult{
		Status: status, Body: body,
		Changes: []model.SyncChangeDraft{readonlyRunChange(record)},
		Audits:  []model.AuditDraft{readonlyRunAudit(action, record.Run.ID, actorType)},
	}
}

func readonlyRunChange(record agentexecution.Record) model.SyncChangeDraft {
	return model.SyncChangeDraft{EntityType: "agent_run", EntityID: record.Run.ID, Operation: "update", EntityVersion: record.Run.Version}
}

func readonlyRunAudit(action string, runID uuid.UUID, actorType string) model.AuditDraft {
	draft := model.AuditDraft{Action: action, Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: runID}}}
	if actorType != "" {
		draft.ActorType = actorType
		if actorType == "agent" {
			actorID := runID
			draft.ActorID = &actorID
		}
	}
	return draft
}

func validateReadonlyFinish(input agentprotocol.ReadonlyRunFinish) error {
	if err := agentprotocol.Validate(agentprotocol.DefinitionReadonlyRunFinish, input); err != nil {
		return fmt.Errorf("%w: invalid readonly finish: %v", ErrValidation, err)
	}
	for _, step := range input.Steps {
		if length := utf8.RuneCountInString(strings.TrimSpace(step.Title)); length < 1 || length > 240 || utf8.RuneCountInString(step.Detail) > 2000 {
			return fmt.Errorf("%w: invalid readonly finish step", ErrValidation)
		}
	}
	switch input.Phase {
	case agentprotocol.ReadonlyRunFinishPhaseCompleted:
		if input.Error != nil {
			return fmt.Errorf("%w: completed readonly finish cannot include an error", ErrValidation)
		}
	case agentprotocol.ReadonlyRunFinishPhaseFailed:
		if input.Error == nil {
			return fmt.Errorf("%w: failed readonly finish requires an error", ErrValidation)
		}
	case agentprotocol.ReadonlyRunFinishPhaseCancelled:
		if input.Error != nil && input.Error.Code != agentprotocol.ErrorCodeCancelled {
			return fmt.Errorf("%w: cancelled readonly finish has an invalid error", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: invalid readonly finish phase", ErrValidation)
	}
	return nil
}

func validateReadonlyActor(actor agentexecution.Actor) error {
	if actor.UserID == uuid.Nil {
		return fmt.Errorf("%w: readonly actor user is required", ErrValidation)
	}
	switch actor.Mode {
	case agentprotocol.ExecutionModeForeground:
		if actor.Token != uuid.Nil {
			return fmt.Errorf("%w: foreground readonly actor token must be empty", ErrValidation)
		}
	case agentprotocol.ExecutionModeBackground:
		if actor.Token == uuid.Nil {
			return fmt.Errorf("%w: background readonly actor token is required", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: readonly actor mode is invalid", ErrValidation)
	}
	return nil
}

func authorizeReadonlyActor(record agentexecution.Record, actor agentexecution.Actor) error {
	if record.Execution.UserID != actor.UserID || record.Execution.Mode != actor.Mode || record.Execution.Token != actor.Token {
		return model.ErrConflict
	}
	return nil
}

func validateReadonlySteps(steps []model.AgentStepDraft) error {
	if len(steps) > 16 {
		return fmt.Errorf("%w: too many readonly run steps", ErrValidation)
	}
	for _, step := range steps {
		if length := utf8.RuneCountInString(strings.TrimSpace(step.Title)); length < 1 || length > 240 || utf8.RuneCountInString(step.Detail) > 2000 {
			return fmt.Errorf("%w: invalid readonly run step", ErrValidation)
		}
		if len(step.Metadata) != 0 {
			var metadata map[string]any
			if err := json.Unmarshal(step.Metadata, &metadata); err != nil || metadata == nil {
				return fmt.Errorf("%w: readonly run step metadata must be an object", ErrValidation)
			}
		}
	}
	return nil
}

func preparedReadonlySteps(steps []model.AgentStepDraft) []model.AgentStepDraft {
	prepared := make([]model.AgentStepDraft, len(steps))
	for index, step := range steps {
		step.Metadata = slices.Clone(step.Metadata)
		if len(step.Metadata) == 0 {
			step.Metadata = json.RawMessage(`{}`)
		}
		prepared[index] = step
	}
	return prepared
}

func readonlyRuntimeMatches(record agentexecution.Record, state agentprotocol.RuntimeState) bool {
	return state.ProtocolVersion == record.Execution.ProtocolVersion &&
		state.RunID == record.Run.ID.String() && state.ExecutionMode == record.Execution.Mode &&
		state.Budget == record.Execution.Budget && reflect.DeepEqual(state.CapabilitySnapshot, record.Execution.Capabilities) &&
		state.Usage.TotalTokens == state.Usage.InputTokens+state.Usage.OutputTokens &&
		state.Usage.TotalTokens <= record.Execution.Budget.MaxTokens &&
		state.Usage.InputTokens <= record.Execution.KnownUsage.InputTokens &&
		state.Usage.OutputTokens <= record.Execution.KnownUsage.OutputTokens &&
		state.Usage.TotalTokens <= record.Execution.KnownUsage.TotalTokens
}

func projectReadonlyRuntime(record *agentexecution.Record, state agentprotocol.RuntimeState, now time.Time) error {
	switch state.Phase {
	case agentprotocol.RuntimePhaseCompleted:
		if state.Error != nil {
			return fmt.Errorf("%w: completed runtime state cannot include an error", ErrValidation)
		}
		summary := completedReadonlySummary(state.Messages)
		finishReadonlyRecord(record, agentprotocol.ReadonlyRunFinishPhaseCompleted, summary, nil, now)
	case agentprotocol.RuntimePhaseFailed:
		if state.Error == nil {
			return fmt.Errorf("%w: failed runtime state requires an error", ErrValidation)
		}
		finishReadonlyRecord(record, agentprotocol.ReadonlyRunFinishPhaseFailed, valueOrEmpty(state.AssistantDraft), state.Error, now)
	case agentprotocol.RuntimePhaseCancelled:
		if state.Error != nil && state.Error.Code != agentprotocol.ErrorCodeCancelled {
			return fmt.Errorf("%w: cancelled runtime state has an invalid error", ErrValidation)
		}
		finishReadonlyRecord(record, agentprotocol.ReadonlyRunFinishPhaseCancelled, valueOrEmpty(state.AssistantDraft), state.Error, now)
	}
	record.Execution.ResultOrigin = string(agentprotocol.ReadonlyRunViewResultOriginServerRuntime)
	return nil
}

func completedReadonlySummary(messages []agentprotocol.Message) string {
	if len(messages) == 0 {
		return ""
	}
	message := messages[len(messages)-1]
	if message.Role != agentprotocol.MessageRoleAssistant {
		return ""
	}
	const maximumRunes = 8000
	var summary strings.Builder
	written := 0
	for _, block := range message.Content {
		if block.Type != agentprotocol.ContentBlockTypeText || block.Text == nil {
			continue
		}
		for _, character := range *block.Text {
			if written == maximumRunes {
				return summary.String()
			}
			summary.WriteRune(character)
			written++
		}
	}
	return summary.String()
}

func finishReadonlyRecord(record *agentexecution.Record, phase agentprotocol.ReadonlyRunFinishPhase, summary string, runError *agentprotocol.AgentError, now time.Time) {
	record.Run.Summary = &summary
	switch phase {
	case agentprotocol.ReadonlyRunFinishPhaseCompleted:
		record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusCompleted)
		clearReadonlyError(record)
	case agentprotocol.ReadonlyRunFinishPhaseFailed:
		record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusFailed)
		if runError != nil {
			setReadonlyError(record, *runError)
		}
	case agentprotocol.ReadonlyRunFinishPhaseCancelled:
		record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusStopped)
		cancelled := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "cancelled"}
		if runError != nil {
			cancelled = *runError
		}
		setReadonlyError(record, cancelled)
	}
	finished := now
	record.Run.FinishedAt = &finished
}

func timeoutReadonlyRecord(record *agentexecution.Record, now time.Time) {
	failReadonlyRecord(record, now, agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "timeout"})
}

func failReadonlyRecord(record *agentexecution.Record, now time.Time, runError agentprotocol.AgentError) {
	record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusFailed)
	setReadonlyError(record, runError)
	finished := now
	record.Run.FinishedAt = &finished
}

func stopReadonlyRecord(record *agentexecution.Record, now time.Time, runError agentprotocol.AgentError) {
	record.Run.Status = string(agentprotocol.ReadonlyRunViewStatusStopped)
	setReadonlyError(record, runError)
	finished := now
	record.Run.FinishedAt = &finished
}

func setReadonlyError(record *agentexecution.Record, runError agentprotocol.AgentError) {
	code, message := string(runError.Code), runError.Message
	record.Run.ErrorCode = &code
	record.Run.ErrorMessage = &message
}

func clearReadonlyError(record *agentexecution.Record) {
	record.Run.ErrorCode = nil
	record.Run.ErrorMessage = nil
}

func deadlineExpired(record agentexecution.Record, now time.Time) bool {
	return !record.Execution.Deadline.After(now)
}

func isReadonlyTerminal(status string) bool {
	return status == string(agentprotocol.ReadonlyRunViewStatusCompleted) ||
		status == string(agentprotocol.ReadonlyRunViewStatusFailed) ||
		status == string(agentprotocol.ReadonlyRunViewStatusStopped)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func readonlyBudget(value agentprotocol.Budget) (agentprotocol.Budget, error) {
	if value == (agentprotocol.Budget{}) {
		return defaultReadonlyBudget, nil
	}
	if value.MaxSteps < 1 || value.MaxSteps > defaultReadonlyBudget.MaxSteps ||
		value.MaxTokens < 1 || value.MaxTokens > defaultReadonlyBudget.MaxTokens ||
		value.MaxDurationMs < 1 || value.MaxDurationMs > defaultReadonlyBudget.MaxDurationMs ||
		value.MaxWorkers < 1 || value.MaxWorkers > defaultReadonlyBudget.MaxWorkers ||
		value.MaxConcurrency < 1 || value.MaxConcurrency > defaultReadonlyBudget.MaxConcurrency ||
		value.MaxRepeatedToolCalls < 1 || value.MaxRepeatedToolCalls > defaultReadonlyBudget.MaxRepeatedToolCalls {
		return agentprotocol.Budget{}, errors.New("readonly run budget must be positive and no greater than the default")
	}
	return value, nil
}

func cloneReadonlyCapabilities(input map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot) (map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot, error) {
	if len(input) != 2 {
		return nil, errors.New("readonly run foreground and background capabilities are required")
	}
	result := make(map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot, len(input))
	for mode, snapshot := range input {
		if mode != agentprotocol.ExecutionModeForeground && mode != agentprotocol.ExecutionModeBackground {
			return nil, errors.New("readonly run capability mode is invalid")
		}
		if snapshot.ExecutionMode != mode || snapshot.RuntimeVersion != readonlyRuntimeVersion {
			return nil, errors.New("readonly run capability mode or runtime version is invalid")
		}
		if len(snapshot.Scope.Domains) != 1 || snapshot.Scope.Domains[0] != "calendar" || snapshot.Scope.EntityIds != nil {
			return nil, errors.New("readonly run capability scope is invalid")
		}
		if !completeReadonlyToolGrant(snapshot.ToolIds) {
			return nil, errors.New("readonly run capability tools are invalid")
		}
		if len(snapshot.Skills) != 1 || snapshot.Skills[0].Name != "calendar-overview" || snapshot.Skills[0].Version != agentprotocol.SemVer("1.0.0") || strings.TrimSpace(snapshot.Skills[0].Digest) == "" {
			return nil, errors.New("readonly run capability skills are invalid")
		}
		if err := agentprotocol.Validate(agentprotocol.DefinitionCapabilitySnapshot, snapshot); err != nil {
			return nil, fmt.Errorf("readonly run capability snapshot is invalid: %w", err)
		}
		result[mode] = cloneCapabilitySnapshot(snapshot)
	}
	for _, mode := range []agentprotocol.ExecutionMode{agentprotocol.ExecutionModeForeground, agentprotocol.ExecutionModeBackground} {
		if _, ok := result[mode]; !ok {
			return nil, errors.New("readonly run foreground and background capabilities are required")
		}
	}
	return result, nil
}

func completeReadonlyToolGrant(toolIDs []string) bool {
	if len(toolIDs) != 3 {
		return false
	}
	want := map[string]struct{}{
		"skill_list": {}, "skill_load": {}, "dayorder.calendar.read": {},
	}
	for _, toolID := range toolIDs {
		if _, exists := want[toolID]; !exists {
			return false
		}
		delete(want, toolID)
	}
	return len(want) == 0
}

func readonlyProfiles(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, errors.New("readonly run model profiles are required")
	}
	result := make([]string, len(input))
	seen := make(map[string]struct{}, len(input))
	for index, profile := range input {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return nil, errors.New("readonly run model profile cannot be empty")
		}
		if _, exists := seen[profile]; exists {
			return nil, errors.New("readonly run model profiles cannot contain duplicates")
		}
		seen[profile] = struct{}{}
		result[index] = profile
	}
	return result, nil
}

func cloneCapabilitySnapshot(value agentprotocol.CapabilitySnapshot) agentprotocol.CapabilitySnapshot {
	value.ToolIds = slices.Clone(value.ToolIds)
	value.Skills = slices.Clone(value.Skills)
	value.Scope = cloneAgentScope(value.Scope)
	return value
}

func cloneAgentScope(value agentprotocol.AgentScope) agentprotocol.AgentScope {
	value.Domains = slices.Clone(value.Domains)
	value.EntityIds = slices.Clone(value.EntityIds)
	if value.From != nil {
		from := *value.From
		value.From = &from
	}
	if value.To != nil {
		to := *value.To
		value.To = &to
	}
	return value
}

func readonlyRunView(record agentexecution.Record, now time.Time) agentprotocol.ReadonlyRunView {
	view := agentprotocol.ReadonlyRunView{
		ProtocolVersion:    record.Execution.ProtocolVersion,
		RunID:              agentprotocol.UUID(record.Run.ID.String()),
		ExecutionMode:      record.Execution.Mode,
		Status:             agentprotocol.ReadonlyRunViewStatus(record.Run.Status),
		Version:            int(record.Run.Version),
		CapabilitySnapshot: cloneCapabilitySnapshot(record.Execution.Capabilities),
		Budget:             record.Execution.Budget,
		ModelProfile:       record.Execution.ModelProfile,
		ServerNow:          agentprotocol.DateTime(now.UTC().Format(time.RFC3339Nano)),
		DeadlineAt:         agentprotocol.DateTime(record.Execution.Deadline.UTC().Format(time.RFC3339Nano)),
		Usage:              record.Execution.KnownUsage,
		UsageComplete:      record.Execution.UsageComplete,
		ResultOrigin:       agentprotocol.ReadonlyRunViewResultOrigin(record.Execution.ResultOrigin),
	}
	if record.Run.Summary != nil {
		summary := *record.Run.Summary
		view.Summary = &summary
	}
	if record.Run.ErrorCode != nil {
		message := ""
		if record.Run.ErrorMessage != nil {
			message = *record.Run.ErrorMessage
		}
		view.Error = &agentprotocol.AgentError{Code: agentprotocol.ErrorCode(*record.Run.ErrorCode), Message: message}
	}
	return view
}
