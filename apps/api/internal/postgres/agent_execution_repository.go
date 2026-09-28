package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type AgentExecutionRepository struct {
	agent   *AgentRepository
	newUUID func() uuid.UUID
}

func NewAgentExecutionRepository() *AgentExecutionRepository {
	return &AgentExecutionRepository{agent: NewAgentRepository(), newUUID: uuid.New}
}

func (repository *AgentExecutionRepository) LockAccount(ctx context.Context, tx database.Tx, userID uuid.UUID) error {
	if err := db.New(tx).LockAgentExecutionAccount(ctx, userID.String()); err != nil {
		return mapDatabaseError("lock agent execution account", err)
	}
	return nil
}

func (repository *AgentExecutionRepository) Create(ctx context.Context, tx database.Tx, record agentexecution.Record) error {
	capabilities, budget, usage, err := executionJSON(record.Execution)
	if err != nil {
		return err
	}
	queries := db.New(tx)
	if err = queries.CreateReadonlyAgentRun(ctx, db.CreateReadonlyAgentRunParams{
		ID: pgUUID(record.Run.ID), UserID: pgUUID(record.Execution.UserID), Intent: record.Run.Intent,
		Status: record.Run.Status, ActionMode: record.Run.ActionMode, Scope: append([]byte(nil), record.Run.Scope...),
		Provider: pgOptionalText(record.Run.Provider), Model: pgOptionalText(record.Run.Model),
		StartedAt: pgOptionalTime(record.Run.StartedAt), FinishedAt: pgOptionalTime(record.Run.FinishedAt),
		Summary: pgOptionalText(record.Run.Summary), ErrorCode: pgOptionalText(record.Run.ErrorCode),
		ErrorMessage: pgOptionalText(record.Run.ErrorMessage),
	}); err != nil {
		return mapDatabaseError("create readonly agent run", err)
	}
	if err = queries.CreateReadonlyExecution(ctx, db.CreateReadonlyExecutionParams{
		UserID: pgUUID(record.Execution.UserID), RunID: pgUUID(record.Execution.RunID), Token: pgUUID(record.Execution.Token),
		ExecutionMode: string(record.Execution.Mode), ProtocolVersion: record.Execution.ProtocolVersion,
		RuntimeVersion: record.Execution.RuntimeVersion, ModelProfile: record.Execution.ModelProfile,
		Timezone: record.Execution.Timezone, Capabilities: capabilities, Budget: budget,
		Deadline: pgTime(record.Execution.Deadline), KnownUsage: usage,
		ReservedTokens: int32(record.Execution.ReservedTokens), UsageComplete: record.Execution.UsageComplete,
		ResultOrigin: record.Execution.ResultOrigin,
	}); err != nil {
		return mapDatabaseError("create readonly agent execution", err)
	}
	return nil
}

func (repository *AgentExecutionRepository) Get(ctx context.Context, tx database.Tx, userID, runID uuid.UUID, forUpdate bool) (agentexecution.Record, error) {
	queries := db.New(tx)
	var runRow *db.DayorderAgentRun
	var executionRow *db.DayorderAgentRunExecution
	var err error
	if forUpdate {
		runRow, err = queries.GetAgentRunForUpdate(ctx, pgUUID(userID), pgUUID(runID))
		if err == nil {
			executionRow, err = queries.GetReadonlyExecutionForUpdate(ctx, pgUUID(userID), pgUUID(runID))
		}
	} else {
		runRow, err = queries.GetAgentRun(ctx, pgUUID(userID), pgUUID(runID))
		if err == nil {
			executionRow, err = queries.GetReadonlyExecution(ctx, pgUUID(userID), pgUUID(runID))
		}
	}
	if err != nil {
		return agentexecution.Record{}, mapDatabaseError("get readonly agent execution", err)
	}
	run, err := repository.agent.hydrateRun(ctx, queries, userID, agentRunFromRow(runRow))
	if err != nil {
		return agentexecution.Record{}, err
	}
	execution, err := executionFromRow(executionRow)
	if err != nil {
		return agentexecution.Record{}, err
	}
	return agentexecution.Record{Run: run, Execution: execution}, nil
}

func (repository *AgentExecutionRepository) Active(ctx context.Context, tx database.Tx, userID uuid.UUID) ([]agentexecution.Record, error) {
	rows, err := db.New(tx).ListActiveReadonlyExecutionIDs(ctx, pgUUID(userID))
	if err != nil {
		return nil, mapDatabaseError("list active readonly agent executions", err)
	}
	records := make([]agentexecution.Record, 0, len(rows))
	for _, row := range rows {
		record, getErr := repository.Get(ctx, tx, userID, uuid.UUID(row.Bytes), false)
		if getErr != nil {
			return nil, getErr
		}
		records = append(records, record)
	}
	return records, nil
}

func (repository *AgentExecutionRepository) CountCreatedSince(ctx context.Context, tx database.Tx, userID uuid.UUID, since time.Time) (int, error) {
	count, err := db.New(tx).CountReadonlyExecutionsCreatedSince(ctx, pgUUID(userID), pgTime(since))
	if err != nil {
		return 0, mapDatabaseError("count readonly agent executions", err)
	}
	return int(count), nil
}

func (repository *AgentExecutionRepository) Save(ctx context.Context, tx database.Tx, record agentexecution.Record, expectedVersion int64, expectedToken uuid.UUID) error {
	usage, err := json.Marshal(record.Execution.KnownUsage)
	if err != nil {
		return fmt.Errorf("encode readonly agent execution usage: %w", err)
	}
	updated, err := db.New(tx).SaveReadonlyExecution(ctx, db.SaveReadonlyExecutionParams{
		NewToken: pgUUID(record.Execution.Token), KnownUsage: usage,
		ReservedTokens: int32(record.Execution.ReservedTokens), UsageComplete: record.Execution.UsageComplete,
		ResultOrigin: record.Execution.ResultOrigin, UserID: pgUUID(record.Execution.UserID), RunID: pgUUID(record.Execution.RunID),
		ExpectedToken: pgUUID(expectedToken), Status: record.Run.Status, Provider: pgOptionalText(record.Run.Provider),
		Model: pgOptionalText(record.Run.Model), StartedAt: pgOptionalTime(record.Run.StartedAt),
		FinishedAt: pgOptionalTime(record.Run.FinishedAt), Summary: pgOptionalText(record.Run.Summary),
		ErrorCode: pgOptionalText(record.Run.ErrorCode), ErrorMessage: pgOptionalText(record.Run.ErrorMessage),
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return mapDatabaseError("save readonly agent execution", err)
	}
	if updated != 1 {
		return model.ErrConflict
	}
	return nil
}

func (repository *AgentExecutionRepository) Operations(ctx context.Context, tx database.Tx, userID, runID uuid.UUID) ([]agentexecution.Operation, error) {
	rows, err := db.New(tx).ListReadonlyOperations(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return nil, mapDatabaseError("list readonly agent operations", err)
	}
	operations := make([]agentexecution.Operation, 0, len(rows))
	for _, row := range rows {
		operation, convertErr := operationFromRow(row)
		if convertErr != nil {
			return nil, convertErr
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func (repository *AgentExecutionRepository) PutOperation(ctx context.Context, tx database.Tx, operation agentexecution.Operation, expectedState string) error {
	usage, err := json.Marshal(operation.Usage)
	if err != nil {
		return fmt.Errorf("encode readonly agent operation usage: %w", err)
	}
	queries := db.New(tx)
	if expectedState == "" {
		err = queries.InsertReadonlyOperation(ctx, db.InsertReadonlyOperationParams{
			UserID: pgUUID(operation.UserID), RunID: pgUUID(operation.RunID), Kind: operation.Kind,
			OperationID: operation.ID, State: operation.State, OperationHash: append([]byte(nil), operation.Hash[:]...),
			Attempts: int32(operation.Attempts), ReservedTokens: int32(operation.ReservedTokens), Usage: usage,
			UsageComplete: operation.UsageComplete, ErrorCode: operation.ErrorCode, StartedAt: pgTime(operation.StartedAt),
			FinishedAt: pgZeroableTime(operation.FinishedAt),
		})
		if err != nil {
			return mapDatabaseError("insert readonly agent operation", err)
		}
		return nil
	}
	updated, err := queries.UpdateReadonlyOperation(ctx, db.UpdateReadonlyOperationParams{
		State: operation.State, Attempts: int32(operation.Attempts), ReservedTokens: int32(operation.ReservedTokens),
		Usage: usage, UsageComplete: operation.UsageComplete, ErrorCode: operation.ErrorCode,
		FinishedAt: pgZeroableTime(operation.FinishedAt),
		UserID:     pgUUID(operation.UserID), RunID: pgUUID(operation.RunID), Kind: operation.Kind,
		OperationID: operation.ID, ExpectedState: expectedState,
	})
	if err != nil {
		return mapDatabaseError("update readonly agent operation", err)
	}
	if updated != 1 {
		return model.ErrConflict
	}
	return nil
}

func (repository *AgentExecutionRepository) AddRefs(ctx context.Context, tx database.Tx, userID, runID uuid.UUID, refs []model.AgentSourceRefDraft) error {
	queries := db.New(tx)
	for _, ref := range refs {
		if err := queries.InsertReadonlySourceRef(ctx, db.InsertReadonlySourceRefParams{
			ID: pgUUID(repository.newUUID()), UserID: pgUUID(userID), RunID: pgUUID(runID),
			EntityType: ref.EntityType, EntityID: pgUUID(ref.EntityID), EntityVersion: ref.EntityVersion,
			LabelSnapshot: ref.LabelSnapshot,
		}); err != nil {
			return mapDatabaseError("add readonly agent source reference", err)
		}
	}
	return nil
}

func (repository *AgentExecutionRepository) AddSteps(ctx context.Context, tx database.Tx, userID, runID uuid.UUID, steps []model.AgentStepDraft) error {
	queries := db.New(tx)
	sequence, err := queries.NextReadonlyAgentStepSequence(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return mapDatabaseError("read next readonly agent step sequence", err)
	}
	for index, step := range steps {
		if _, err = queries.CreateAgentStep(ctx, db.CreateAgentStepParams{
			ID: pgUUID(repository.newUUID()), UserID: pgUUID(userID), RunID: pgUUID(runID),
			SequenceNo: sequence + int32(index), Title: step.Title, Detail: step.Detail,
			Status: "done", Metadata: append([]byte(nil), step.Metadata...),
		}); err != nil {
			return mapDatabaseError("add readonly agent step", err)
		}
	}
	return nil
}

func executionJSON(execution agentexecution.Execution) ([]byte, []byte, []byte, error) {
	capabilities, err := json.Marshal(execution.Capabilities)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode readonly agent capabilities: %w", err)
	}
	budget, err := json.Marshal(execution.Budget)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode readonly agent budget: %w", err)
	}
	usage, err := json.Marshal(execution.KnownUsage)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode readonly agent usage: %w", err)
	}
	return capabilities, budget, usage, nil
}

func executionFromRow(row *db.DayorderAgentRunExecution) (agentexecution.Execution, error) {
	execution := agentexecution.Execution{
		UserID: uuid.UUID(row.UserID.Bytes), RunID: uuid.UUID(row.RunID.Bytes), Token: uuid.UUID(row.Token.Bytes),
		Mode: agentprotocol.ExecutionMode(row.ExecutionMode), ProtocolVersion: row.ProtocolVersion,
		RuntimeVersion: row.RuntimeVersion, ModelProfile: row.ModelProfile, Timezone: row.Timezone,
		ResultOrigin: row.ResultOrigin, Deadline: row.Deadline.Time.UTC(), ReservedTokens: int(row.ReservedTokens),
		UsageComplete: row.UsageComplete,
	}
	if err := json.Unmarshal(row.Capabilities, &execution.Capabilities); err != nil {
		return agentexecution.Execution{}, fmt.Errorf("decode readonly agent capabilities: %w", err)
	}
	if err := json.Unmarshal(row.Budget, &execution.Budget); err != nil {
		return agentexecution.Execution{}, fmt.Errorf("decode readonly agent budget: %w", err)
	}
	if err := json.Unmarshal(row.KnownUsage, &execution.KnownUsage); err != nil {
		return agentexecution.Execution{}, fmt.Errorf("decode readonly agent usage: %w", err)
	}
	return execution, nil
}

func operationFromRow(row *db.DayorderAgentRunOperation) (agentexecution.Operation, error) {
	operation := agentexecution.Operation{
		UserID: uuid.UUID(row.UserID.Bytes), RunID: uuid.UUID(row.RunID.Bytes), Kind: row.Kind,
		ID: row.OperationID, State: row.State, Attempts: int(row.Attempts), ReservedTokens: int(row.ReservedTokens),
		UsageComplete: row.UsageComplete, ErrorCode: row.ErrorCode, StartedAt: row.StartedAt.Time.UTC(),
	}
	copy(operation.Hash[:], row.OperationHash)
	if row.FinishedAt.Valid {
		operation.FinishedAt = row.FinishedAt.Time.UTC()
	}
	if err := json.Unmarshal(row.Usage, &operation.Usage); err != nil {
		return agentexecution.Operation{}, fmt.Errorf("decode readonly agent operation usage: %w", err)
	}
	return operation, nil
}

func pgZeroableTime(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgTime(value)
}

var _ agentexecution.Store = (*AgentExecutionRepository)(nil)
