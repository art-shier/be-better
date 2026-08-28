package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type AgentRepository struct {
	goals    *GoalRepository
	tasks    *TaskRepository
	calendar *CalendarRepository
	content  *ContentRepository
	newUUID  func() uuid.UUID
}

func NewAgentRepository() *AgentRepository {
	return &AgentRepository{
		goals: NewGoalRepository(), tasks: NewTaskRepository(), calendar: NewCalendarRepository(),
		content: NewContentRepository(), newUUID: uuid.New,
	}
}

func (repository *AgentRepository) CreateRun(ctx context.Context, tx database.Tx, userID uuid.UUID, run model.AgentRun) (model.AgentRun, error) {
	row, err := db.New(tx).CreateAgentRun(ctx, db.CreateAgentRunParams{
		ID: pgUUID(run.ID), UserID: pgUUID(userID), Intent: run.Intent, Status: run.Status,
		ActionMode: run.ActionMode, Scope: bytes.Clone(run.Scope), Provider: pgOptionalText(run.Provider),
		Model: pgOptionalText(run.Model), StartedAt: pgOptionalTime(run.StartedAt),
	})
	if err != nil {
		return model.AgentRun{}, mapDatabaseError("create agent run", err)
	}
	return agentRunFromRow(row), nil
}

func (repository *AgentRepository) GetRun(ctx context.Context, tx database.Tx, userID, runID uuid.UUID) (model.AgentRun, error) {
	queries := db.New(tx)
	row, err := queries.GetAgentRun(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return model.AgentRun{}, mapDatabaseError("get agent run", err)
	}
	return repository.hydrateRun(ctx, queries, userID, agentRunFromRow(row))
}

func (repository *AgentRepository) ListRuns(ctx context.Context, tx database.Tx, userID uuid.UUID, after *model.ResourcePosition, limit int) ([]model.AgentRun, error) {
	afterTime, afterID := pgtype.Timestamptz{}, pgtype.UUID{}
	if after != nil {
		afterTime, afterID = pgTime(after.UpdatedAt), pgUUID(after.ID)
	}
	queries := db.New(tx)
	rows, err := queries.ListAgentRuns(ctx, pgUUID(userID), afterTime, afterID, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list agent runs: %w", err)
	}
	runs := make([]model.AgentRun, 0, len(rows))
	for _, row := range rows {
		run, hydrateErr := repository.hydrateRun(ctx, queries, userID, agentRunFromRow(row))
		if hydrateErr != nil {
			return nil, hydrateErr
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (repository *AgentRepository) ApplyChange(ctx context.Context, tx database.Tx, userID, changeID uuid.UUID, expectedVersion int64, resolvedAt time.Time) (model.AgentApplyResult, error) {
	queries := db.New(tx)
	changeRow, err := queries.GetAgentChangeForUpdate(ctx, pgUUID(userID), pgUUID(changeID))
	if err != nil {
		return model.AgentApplyResult{}, mapDatabaseError("get agent change", err)
	}
	if changeRow.Version != expectedVersion || changeRow.Status != "pending" {
		return model.AgentApplyResult{}, model.ErrConflict
	}
	runRow, err := queries.GetAgentRunForUpdate(ctx, pgUUID(userID), changeRow.RunID)
	if err != nil {
		return model.AgentApplyResult{}, mapDatabaseError("get agent run for change", err)
	}
	if runRow.Status != "waiting" {
		return model.AgentApplyResult{}, model.ErrConflict
	}

	target, err := repository.applyTarget(ctx, tx, userID, agentChangeFromRow(changeRow))
	if err != nil {
		return model.AgentApplyResult{}, err
	}
	updatedChangeRow, err := queries.MarkAgentChangeApplied(ctx, db.MarkAgentChangeAppliedParams{
		AppliedTargetID: pgUUID(target.id), ResolvedAt: pgTime(resolvedAt), UserID: pgUUID(userID),
		ID: pgUUID(changeID), ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return model.AgentApplyResult{}, agentChangeWriteError(ctx, queries, userID, changeID, "apply agent change", err)
	}

	finalRunRow, completeErr := queries.CompleteAgentRunIfResolved(ctx, pgTime(resolvedAt), pgUUID(userID), changeRow.RunID)
	runUpdated := completeErr == nil
	if errors.Is(completeErr, pgx.ErrNoRows) {
		finalRunRow, completeErr = queries.GetAgentRun(ctx, pgUUID(userID), changeRow.RunID)
	}
	if completeErr != nil {
		return model.AgentApplyResult{}, fmt.Errorf("resolve agent run after applying change: %w", completeErr)
	}
	run, err := repository.hydrateRun(ctx, queries, userID, agentRunFromRow(finalRunRow))
	if err != nil {
		return model.AgentApplyResult{}, err
	}
	return model.AgentApplyResult{
		Change: agentChangeFromRow(updatedChangeRow), Run: run, RunUpdated: runUpdated,
		TargetType: changeRow.TargetType, TargetID: target.id, TargetOperation: target.operation,
		TargetVersion: target.version, BeforeData: target.before, AfterData: target.after,
	}, nil
}

func (repository *AgentRepository) RejectChange(ctx context.Context, tx database.Tx, userID, changeID uuid.UUID, expectedVersion int64, resolvedAt time.Time) (model.AgentApplyResult, error) {
	queries := db.New(tx)
	changeRow, err := queries.GetAgentChangeForUpdate(ctx, pgUUID(userID), pgUUID(changeID))
	if err != nil {
		return model.AgentApplyResult{}, mapDatabaseError("get agent change", err)
	}
	if changeRow.Version != expectedVersion || changeRow.Status != "pending" {
		return model.AgentApplyResult{}, model.ErrConflict
	}
	runRow, err := queries.GetAgentRunForUpdate(ctx, pgUUID(userID), changeRow.RunID)
	if err != nil {
		return model.AgentApplyResult{}, mapDatabaseError("get agent run for rejection", err)
	}
	if runRow.Status != "waiting" {
		return model.AgentApplyResult{}, model.ErrConflict
	}
	updatedChangeRow, err := queries.MarkAgentChangeRejected(ctx, pgTime(resolvedAt), pgUUID(userID), pgUUID(changeID), expectedVersion)
	if err != nil {
		return model.AgentApplyResult{}, agentChangeWriteError(ctx, queries, userID, changeID, "reject agent change", err)
	}
	finalRunRow, completeErr := queries.CompleteAgentRunIfResolved(ctx, pgTime(resolvedAt), pgUUID(userID), changeRow.RunID)
	runUpdated := completeErr == nil
	if errors.Is(completeErr, pgx.ErrNoRows) {
		finalRunRow, completeErr = queries.GetAgentRun(ctx, pgUUID(userID), changeRow.RunID)
	}
	if completeErr != nil {
		return model.AgentApplyResult{}, fmt.Errorf("resolve agent run after rejecting change: %w", completeErr)
	}
	run, err := repository.hydrateRun(ctx, queries, userID, agentRunFromRow(finalRunRow))
	if err != nil {
		return model.AgentApplyResult{}, err
	}
	return model.AgentApplyResult{Change: agentChangeFromRow(updatedChangeRow), Run: run, RunUpdated: runUpdated}, nil
}

func (repository *AgentRepository) StopRun(ctx context.Context, tx database.Tx, userID, runID uuid.UUID, expectedVersion int64, stoppedAt time.Time) (model.AgentRun, error) {
	queries := db.New(tx)
	row, err := queries.StopAgentRun(ctx, pgTime(stoppedAt), pgUUID(userID), pgUUID(runID), expectedVersion)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return model.AgentRun{}, mapDatabaseError("stop agent run", err)
		}
		if _, readErr := queries.GetAgentRun(ctx, pgUUID(userID), pgUUID(runID)); errors.Is(readErr, pgx.ErrNoRows) {
			return model.AgentRun{}, model.ErrNotFound
		} else if readErr != nil {
			return model.AgentRun{}, fmt.Errorf("check agent run after failed stop: %w", readErr)
		}
		return model.AgentRun{}, model.ErrConflict
	}
	return repository.hydrateRun(ctx, queries, userID, agentRunFromRow(row))
}

func (repository *AgentRepository) PrepareAnalysis(ctx context.Context, tx database.Tx, userID, runID uuid.UUID, startedAt time.Time) (model.AgentSnapshot, bool, error) {
	queries := db.New(tx)
	row, err := queries.GetAgentRunForUpdate(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return model.AgentSnapshot{}, false, mapDatabaseError("get agent run for analysis", err)
	}
	transitioned := false
	if row.Status == "ready" {
		row, err = queries.StartAgentRunAnalysis(ctx, pgTime(startedAt), pgUUID(userID), pgUUID(runID), row.Version)
		if err != nil {
			return model.AgentSnapshot{}, false, agentRunWriteError(ctx, queries, userID, runID, "start agent analysis", err)
		}
		transitioned = true
	}
	run := agentRunFromRow(row)
	if run.Status != "analyzing" {
		return model.AgentSnapshot{Run: run}, false, nil
	}
	var scope model.AgentScope
	if err = json.Unmarshal(run.Scope, &scope); err != nil {
		return model.AgentSnapshot{}, false, fmt.Errorf("decode stored agent scope: %w", err)
	}
	settings, err := queries.GetUserSettings(ctx, pgUUID(userID))
	if err != nil {
		return model.AgentSnapshot{}, false, mapDatabaseError("get settings for agent analysis", err)
	}
	if err = authorizeAgentScope(settings.Settings, scope); err != nil {
		return model.AgentSnapshot{}, false, err
	}
	snapshot, err := repository.loadSnapshot(ctx, tx, userID, run, scope)
	return snapshot, transitioned, err
}

func (repository *AgentRepository) CompleteAnalysis(
	ctx context.Context,
	tx database.Tx,
	userID, runID uuid.UUID,
	expectedVersion int64,
	plan model.AgentPlan,
	provider, providerModel string,
	completedAt time.Time,
) (model.AgentRun, error) {
	queries := db.New(tx)
	runRow, err := queries.GetAgentRunForUpdate(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return model.AgentRun{}, mapDatabaseError("get agent run to complete analysis", err)
	}
	if runRow.Version != expectedVersion || runRow.Status != "analyzing" {
		return model.AgentRun{}, model.ErrConflict
	}
	existingSteps, err := queries.ListAgentSteps(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("check existing agent steps: %w", err)
	}
	existingChanges, err := queries.ListAgentChanges(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("check existing agent changes: %w", err)
	}
	existingRefs, err := queries.ListAgentSourceRefs(ctx, pgUUID(userID), pgUUID(runID))
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("check existing agent references: %w", err)
	}
	if len(existingSteps)+len(existingChanges)+len(existingRefs) != 0 {
		return model.AgentRun{}, model.ErrConflict
	}
	for index, draft := range plan.Steps {
		metadata := draft.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		if _, err = queries.CreateAgentStep(ctx, db.CreateAgentStepParams{
			ID: pgUUID(repository.newUUID()), UserID: pgUUID(userID), RunID: pgUUID(runID), SequenceNo: int32(index + 1),
			Title: strings.TrimSpace(draft.Title), Detail: draft.Detail, Status: "done", Metadata: bytes.Clone(metadata),
			StartedAt: pgTime(completedAt), FinishedAt: pgTime(completedAt),
		}); err != nil {
			return model.AgentRun{}, mapDatabaseError("create agent step", err)
		}
	}
	for _, draft := range plan.Changes {
		if _, err = queries.CreateAgentChange(ctx, db.CreateAgentChangeParams{
			ID: pgUUID(repository.newUUID()), UserID: pgUUID(userID), RunID: pgUUID(runID),
			ChangeType: draft.ChangeType, TargetType: draft.TargetType, TargetID: pgOptionalUUID(draft.TargetID),
			BaseVersion: pgOptionalInt64(draft.BaseVersion), Patch: bytes.Clone(draft.Patch),
			PreviewBefore: bytes.Clone(draft.PreviewBefore), PreviewAfter: bytes.Clone(draft.PreviewAfter),
			Reason: strings.TrimSpace(draft.Reason),
		}); err != nil {
			return model.AgentRun{}, mapDatabaseError("create agent change", err)
		}
	}
	for _, draft := range plan.SourceRefs {
		if _, err = queries.CreateAgentSourceRef(ctx, db.CreateAgentSourceRefParams{
			ID: pgUUID(repository.newUUID()), UserID: pgUUID(userID), RunID: pgUUID(runID),
			EntityType: draft.EntityType, EntityID: pgUUID(draft.EntityID), EntityVersion: draft.EntityVersion,
			LabelSnapshot: strings.TrimSpace(draft.LabelSnapshot),
		}); err != nil {
			return model.AgentRun{}, mapDatabaseError("create agent source reference", err)
		}
	}
	status := "completed"
	if runRow.ActionMode == "confirm" && len(plan.Changes) > 0 {
		status = "waiting"
	}
	provider = strings.TrimSpace(provider)
	providerModel = strings.TrimSpace(providerModel)
	summary := strings.TrimSpace(plan.Summary)
	row, err := queries.FinishAgentRunAnalysis(ctx, db.FinishAgentRunAnalysisParams{
		Status: status, Provider: pgOptionalText(&provider), Model: pgOptionalText(&providerModel), Summary: pgOptionalText(&summary),
		FinishedAt: pgTime(completedAt), UserID: pgUUID(userID), ID: pgUUID(runID), ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return model.AgentRun{}, agentRunWriteError(ctx, queries, userID, runID, "complete agent analysis", err)
	}
	return repository.hydrateRun(ctx, queries, userID, agentRunFromRow(row))
}

func (repository *AgentRepository) FailAnalysis(ctx context.Context, tx database.Tx, userID, runID uuid.UUID, code, message string, failedAt time.Time) (model.AgentRun, error) {
	queries := db.New(tx)
	row, err := queries.FailAgentRun(ctx, db.FailAgentRunParams{
		ErrorCode: pgOptionalText(&code), ErrorMessage: pgOptionalText(&message), FinishedAt: pgTime(failedAt),
		UserID: pgUUID(userID), ID: pgUUID(runID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = queries.GetAgentRun(ctx, pgUUID(userID), pgUUID(runID))
		if err == nil && row.Status != "failed" {
			return model.AgentRun{}, model.ErrConflict
		}
	}
	if err != nil {
		return model.AgentRun{}, mapDatabaseError("fail agent run", err)
	}
	return repository.hydrateRun(ctx, queries, userID, agentRunFromRow(row))
}

func (repository *AgentRepository) loadSnapshot(ctx context.Context, tx database.Tx, userID uuid.UUID, run model.AgentRun, scope model.AgentScope) (model.AgentSnapshot, error) {
	const snapshotLimit = 500
	snapshot := model.AgentSnapshot{Run: run, Goals: []model.Goal{}, Tasks: []model.Task{}, Events: []model.CalendarEvent{}, Records: []model.Record{}, Notes: []model.Note{}}
	domains := make(map[string]bool, len(scope.Domains))
	for _, domain := range scope.Domains {
		domains[domain] = true
	}
	selected := make(map[uuid.UUID]bool, len(scope.EntityIDs))
	for _, id := range scope.EntityIDs {
		selected[id] = true
	}
	include := func(id uuid.UUID) bool { return len(selected) == 0 || selected[id] }
	if domains["goals"] {
		values, err := repository.goals.ListGoals(ctx, tx, userID, nil, snapshotLimit+1)
		if err != nil {
			return model.AgentSnapshot{}, err
		}
		if len(values) > snapshotLimit {
			return model.AgentSnapshot{}, model.ErrConflict
		}
		for _, value := range values {
			if include(value.ID) {
				snapshot.Goals = append(snapshot.Goals, value)
			}
		}
	}
	if domains["tasks"] {
		values, err := repository.tasks.ListTasks(ctx, tx, userID, nil, nil, snapshotLimit+1)
		if err != nil {
			return model.AgentSnapshot{}, err
		}
		if len(values) > snapshotLimit {
			return model.AgentSnapshot{}, model.ErrConflict
		}
		for _, value := range values {
			if include(value.ID) {
				snapshot.Tasks = append(snapshot.Tasks, value)
			}
		}
	}
	if domains["calendar"] {
		values, err := repository.calendar.ListEvents(ctx, tx, userID, scope.From, scope.To, nil, snapshotLimit+1)
		if err != nil {
			return model.AgentSnapshot{}, err
		}
		if len(values) > snapshotLimit {
			return model.AgentSnapshot{}, model.ErrConflict
		}
		for _, value := range values {
			if include(value.ID) {
				snapshot.Events = append(snapshot.Events, value)
			}
		}
	}
	if domains["records"] {
		values, err := repository.content.ListRecords(ctx, tx, userID, nil, snapshotLimit+1)
		if err != nil {
			return model.AgentSnapshot{}, err
		}
		if len(values) > snapshotLimit {
			return model.AgentSnapshot{}, model.ErrConflict
		}
		for _, value := range values {
			if include(value.ID) && inAgentTimeRange(value.OccurredAt, scope.From, scope.To) {
				snapshot.Records = append(snapshot.Records, value)
			}
		}
	}
	if domains["notes"] {
		values, err := repository.content.ListNotes(ctx, tx, userID, nil, snapshotLimit+1)
		if err != nil {
			return model.AgentSnapshot{}, err
		}
		if len(values) > snapshotLimit {
			return model.AgentSnapshot{}, model.ErrConflict
		}
		for _, value := range values {
			if include(value.ID) {
				snapshot.Notes = append(snapshot.Notes, value)
			}
		}
	}
	return snapshot, nil
}

func authorizeAgentScope(raw json.RawMessage, scope model.AgentScope) error {
	settings := struct {
		AIEnabled   *bool `json:"aiEnabled"`
		Permissions struct {
			Goals        *bool `json:"goals"`
			Calendar     *bool `json:"calendar"`
			Records      *bool `json:"records"`
			PrivateNotes *bool `json:"privateNotes"`
		} `json:"permissions"`
	}{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return fmt.Errorf("decode settings for agent authorization: %w", err)
	}
	if settings.AIEnabled != nil && !*settings.AIEnabled {
		return model.ErrConflict
	}
	allowed := map[string]bool{
		"goals": defaultTrue(settings.Permissions.Goals), "tasks": defaultTrue(settings.Permissions.Goals),
		"calendar": defaultTrue(settings.Permissions.Calendar), "records": defaultTrue(settings.Permissions.Records),
		"notes": settings.Permissions.PrivateNotes != nil && *settings.Permissions.PrivateNotes,
	}
	for _, domain := range scope.Domains {
		if !allowed[domain] {
			return model.ErrConflict
		}
	}
	return nil
}

func defaultTrue(value *bool) bool { return value == nil || *value }

func inAgentTimeRange(value time.Time, from, to *time.Time) bool {
	return (from == nil || !value.Before(*from)) && (to == nil || !value.After(*to))
}

func (repository *AgentRepository) hydrateRun(ctx context.Context, queries *db.Queries, userID uuid.UUID, run model.AgentRun) (model.AgentRun, error) {
	stepRows, err := queries.ListAgentSteps(ctx, pgUUID(userID), pgUUID(run.ID))
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("list agent steps: %w", err)
	}
	changeRows, err := queries.ListAgentChanges(ctx, pgUUID(userID), pgUUID(run.ID))
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("list agent changes: %w", err)
	}
	refRows, err := queries.ListAgentSourceRefs(ctx, pgUUID(userID), pgUUID(run.ID))
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("list agent source references: %w", err)
	}
	run.Steps = make([]model.AgentStep, 0, len(stepRows))
	for _, row := range stepRows {
		run.Steps = append(run.Steps, agentStepFromRow(row))
	}
	run.Changes = make([]model.AgentChange, 0, len(changeRows))
	for _, row := range changeRows {
		run.Changes = append(run.Changes, agentChangeFromRow(row))
	}
	run.SourceRefs = make([]model.AgentSourceRef, 0, len(refRows))
	for _, row := range refRows {
		run.SourceRefs = append(run.SourceRefs, agentSourceRefFromRow(row))
	}
	return run, nil
}

type appliedAgentTarget struct {
	id        uuid.UUID
	operation string
	version   int64
	before    json.RawMessage
	after     json.RawMessage
}

type repositoryPatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func (repository *AgentRepository) applyTarget(ctx context.Context, tx database.Tx, userID uuid.UUID, change model.AgentChange) (appliedAgentTarget, error) {
	var operations []repositoryPatchOperation
	if err := json.Unmarshal(change.Patch, &operations); err != nil || len(operations) == 0 {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored agent patch")
	}
	switch change.ChangeType {
	case "reschedule-task":
		return repository.applyTaskSchedule(ctx, tx, userID, change, operations)
	case "create-task":
		return repository.applyTaskCreate(ctx, tx, userID, operations)
	case "create-event":
		return repository.applyEventCreate(ctx, tx, userID, operations)
	case "archive-record":
		return repository.applyRecordArchive(ctx, tx, userID, change, operations)
	case "link-note":
		return repository.applyNoteLink(ctx, tx, userID, change, operations)
	default:
		return appliedAgentTarget{}, fmt.Errorf("unsupported stored agent change type")
	}
}

func (repository *AgentRepository) applyTaskSchedule(ctx context.Context, tx database.Tx, userID uuid.UUID, change model.AgentChange, operations []repositoryPatchOperation) (appliedAgentTarget, error) {
	if change.TargetID == nil || change.BaseVersion == nil || change.TargetType != "task" {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored task reschedule change")
	}
	before, err := repository.tasks.GetTask(ctx, tx, userID, *change.TargetID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	after := before
	for _, operation := range operations {
		if operation.Op != "add" && operation.Op != "replace" && operation.Op != "remove" {
			return appliedAgentTarget{}, fmt.Errorf("invalid stored task patch operation")
		}
		switch operation.Path {
		case "/scheduledStart":
			after.ScheduledStart, err = repositoryOptionalTime(operation)
		case "/scheduledEnd":
			after.ScheduledEnd, err = repositoryOptionalTime(operation)
		default:
			return appliedAgentTarget{}, fmt.Errorf("invalid stored task patch path")
		}
		if err != nil {
			return appliedAgentTarget{}, err
		}
	}
	if after.ScheduledEnd != nil && (after.ScheduledStart == nil || after.ScheduledEnd.Before(*after.ScheduledStart)) {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored task schedule")
	}
	updated, err := repository.tasks.UpdateTask(ctx, tx, userID, after, *change.BaseVersion)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	return targetResult("update", before.ID, updated.Version, before, updated)
}

func (repository *AgentRepository) applyTaskCreate(ctx context.Context, tx database.Tx, userID uuid.UUID, operations []repositoryPatchOperation) (appliedAgentTarget, error) {
	task := model.Task{ID: repository.newUUID(), Status: "todo", Priority: "normal"}
	for _, operation := range operations {
		if operation.Op != "add" {
			return appliedAgentTarget{}, fmt.Errorf("invalid stored task create operation")
		}
		var err error
		switch operation.Path {
		case "/title":
			err = json.Unmarshal(operation.Value, &task.Title)
			task.Title = strings.TrimSpace(task.Title)
		case "/status":
			err = json.Unmarshal(operation.Value, &task.Status)
		case "/priority":
			err = json.Unmarshal(operation.Value, &task.Priority)
		case "/estimateMinutes":
			err = json.Unmarshal(operation.Value, &task.EstimateMinutes)
		case "/dueAt":
			task.DueAt, err = repositoryOptionalTime(operation)
		case "/scheduledStart":
			task.ScheduledStart, err = repositoryOptionalTime(operation)
		case "/scheduledEnd":
			task.ScheduledEnd, err = repositoryOptionalTime(operation)
		case "/goalId":
			task.GoalID, err = repositoryOptionalUUID(operation)
		case "/sourceRecordId":
			task.SourceRecordID, err = repositoryOptionalUUID(operation)
		default:
			return appliedAgentTarget{}, fmt.Errorf("invalid stored task create path")
		}
		if err != nil {
			return appliedAgentTarget{}, fmt.Errorf("decode stored task patch: %w", err)
		}
	}
	if task.Title == "" || !map[string]bool{"todo": true, "doing": true, "done": true, "archived": true}[task.Status] || !map[string]bool{"normal": true, "important": true}[task.Priority] || task.EstimateMinutes < 0 || task.Status == "done" {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored task create fields")
	}
	if task.ScheduledEnd != nil && (task.ScheduledStart == nil || task.ScheduledEnd.Before(*task.ScheduledStart)) {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored task create schedule")
	}
	created, err := repository.tasks.CreateTask(ctx, tx, userID, task)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	return targetResult("create", created.ID, created.Version, nil, created)
}

func (repository *AgentRepository) applyEventCreate(ctx context.Context, tx database.Tx, userID uuid.UUID, operations []repositoryPatchOperation) (appliedAgentTarget, error) {
	event := model.CalendarEvent{ID: repository.newUUID(), Timezone: "UTC", Kind: "focus"}
	for _, operation := range operations {
		if operation.Op != "add" {
			return appliedAgentTarget{}, fmt.Errorf("invalid stored event create operation")
		}
		var err error
		switch operation.Path {
		case "/title":
			err = json.Unmarshal(operation.Value, &event.Title)
			event.Title = strings.TrimSpace(event.Title)
		case "/startAt":
			err = json.Unmarshal(operation.Value, &event.StartAt)
		case "/endAt":
			err = json.Unmarshal(operation.Value, &event.EndAt)
		case "/timezone":
			err = json.Unmarshal(operation.Value, &event.Timezone)
		case "/location":
			event.Location, err = repositoryOptionalString(operation)
		case "/kind":
			err = json.Unmarshal(operation.Value, &event.Kind)
		case "/sourceCalendar":
			event.SourceCalendar, err = repositoryOptionalString(operation)
		case "/goalId":
			event.GoalID, err = repositoryOptionalUUID(operation)
		default:
			return appliedAgentTarget{}, fmt.Errorf("invalid stored event create path")
		}
		if err != nil {
			return appliedAgentTarget{}, fmt.Errorf("decode stored event patch: %w", err)
		}
	}
	if event.Title == "" || event.StartAt.IsZero() || event.EndAt.Before(event.StartAt) || !map[string]bool{"fixed": true, "focus": true, "health": true, "personal": true}[event.Kind] {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored event create fields")
	}
	if _, err := time.LoadLocation(event.Timezone); err != nil {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored event timezone")
	}
	created, err := repository.calendar.CreateEvent(ctx, tx, userID, event)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	return targetResult("create", created.ID, created.Version, nil, created)
}

func (repository *AgentRepository) applyRecordArchive(ctx context.Context, tx database.Tx, userID uuid.UUID, change model.AgentChange, operations []repositoryPatchOperation) (appliedAgentTarget, error) {
	if change.TargetID == nil || change.BaseVersion == nil || change.TargetType != "record" || len(operations) != 1 || operations[0].Path != "/archivedAt" {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored record archive change")
	}
	before, err := repository.content.GetRecord(ctx, tx, userID, *change.TargetID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	after := before
	after.ArchivedAt, err = repositoryOptionalTime(operations[0])
	if err != nil || after.ArchivedAt == nil {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored record archive time")
	}
	updated, err := repository.content.UpdateRecord(ctx, tx, userID, after, *change.BaseVersion)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	return targetResult("update", before.ID, updated.Version, before, updated)
}

func (repository *AgentRepository) applyNoteLink(ctx context.Context, tx database.Tx, userID uuid.UUID, change model.AgentChange, operations []repositoryPatchOperation) (appliedAgentTarget, error) {
	if change.TargetID == nil || change.BaseVersion == nil || change.TargetType != "note" || len(operations) != 1 || operations[0].Op != "add" || operations[0].Path != "/linkedEntityIds/-" {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored note link change")
	}
	var targetID uuid.UUID
	if err := json.Unmarshal(operations[0].Value, &targetID); err != nil || targetID == uuid.Nil || targetID == *change.TargetID {
		return appliedAgentTarget{}, fmt.Errorf("invalid stored note link target")
	}
	targetType, err := repository.content.ResolveEntityType(ctx, tx, userID, targetID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	before, err := repository.content.GetNote(ctx, tx, userID, *change.TargetID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	existing, err := repository.content.ListNoteLinks(ctx, tx, userID, before.ID)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	for _, link := range existing {
		before.LinkedEntityIDs = append(before.LinkedEntityIDs, link.TargetID)
		if link.TargetID == targetID {
			return appliedAgentTarget{}, model.ErrConflict
		}
	}
	if len(existing) >= 50 {
		return appliedAgentTarget{}, model.ErrConflict
	}
	updated, err := repository.content.UpdateNote(ctx, tx, userID, before, *change.BaseVersion)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	existing = append(existing, model.EntityLink{
		ID: repository.newUUID(), SourceType: "note", SourceID: before.ID,
		TargetType: targetType, TargetID: targetID, RelationType: "references",
	})
	if err = repository.content.ReplaceNoteLinks(ctx, tx, userID, before.ID, existing); err != nil {
		return appliedAgentTarget{}, err
	}
	updated.LinkedEntityIDs = append(append([]uuid.UUID(nil), before.LinkedEntityIDs...), targetID)
	return targetResult("update", before.ID, updated.Version, before, updated)
}

func repositoryOptionalTime(operation repositoryPatchOperation) (*time.Time, error) {
	if operation.Op == "remove" || string(operation.Value) == "null" {
		return nil, nil
	}
	var value time.Time
	if err := json.Unmarshal(operation.Value, &value); err != nil || value.IsZero() {
		return nil, errors.New("invalid patch time")
	}
	value = value.UTC()
	return &value, nil
}

func repositoryOptionalUUID(operation repositoryPatchOperation) (*uuid.UUID, error) {
	if operation.Op == "remove" || string(operation.Value) == "null" {
		return nil, nil
	}
	var value uuid.UUID
	if err := json.Unmarshal(operation.Value, &value); err != nil || value == uuid.Nil {
		return nil, errors.New("invalid patch UUID")
	}
	return &value, nil
}

func repositoryOptionalString(operation repositoryPatchOperation) (*string, error) {
	if operation.Op == "remove" || string(operation.Value) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(operation.Value, &value); err != nil {
		return nil, errors.New("invalid patch string")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	return &value, nil
}

func targetResult(operation string, id uuid.UUID, version int64, before, after any) (appliedAgentTarget, error) {
	var beforeData json.RawMessage
	if before != nil {
		encoded, err := json.Marshal(before)
		if err != nil {
			return appliedAgentTarget{}, err
		}
		beforeData = encoded
	}
	afterData, err := json.Marshal(after)
	if err != nil {
		return appliedAgentTarget{}, err
	}
	return appliedAgentTarget{id: id, operation: operation, version: version, before: beforeData, after: afterData}, nil
}

func agentChangeWriteError(ctx context.Context, queries *db.Queries, userID, changeID uuid.UUID, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	row, readErr := queries.GetAgentChange(ctx, pgUUID(userID), pgUUID(changeID))
	if errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	if readErr != nil {
		return fmt.Errorf("check agent change after failed write: %w", readErr)
	}
	if row.Status != "pending" {
		return model.ErrConflict
	}
	return model.ErrConflict
}

func agentRunWriteError(ctx context.Context, queries *db.Queries, userID, runID uuid.UUID, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	if _, readErr := queries.GetAgentRun(ctx, pgUUID(userID), pgUUID(runID)); errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	} else if readErr != nil {
		return fmt.Errorf("check agent run after failed write: %w", readErr)
	}
	return model.ErrConflict
}

func agentRunFromRow(row *db.DayorderAgentRun) model.AgentRun {
	return model.AgentRun{
		ID: uuid.UUID(row.ID.Bytes), Intent: row.Intent, Status: row.Status, ActionMode: row.ActionMode,
		Scope: bytes.Clone(row.Scope), Provider: optionalText(row.Provider), Model: optionalText(row.Model),
		StartedAt: optionalTime(row.StartedAt), FinishedAt: optionalTime(row.FinishedAt), Summary: optionalText(row.Summary),
		ErrorCode: optionalText(row.ErrorCode), ErrorMessage: optionalText(row.ErrorMessage), Version: row.Version,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
		Steps: []model.AgentStep{}, Changes: []model.AgentChange{}, SourceRefs: []model.AgentSourceRef{},
	}
}

func agentStepFromRow(row *db.DayorderAgentStep) model.AgentStep {
	return model.AgentStep{
		ID: uuid.UUID(row.ID.Bytes), RunID: uuid.UUID(row.RunID.Bytes), SequenceNo: int(row.SequenceNo),
		Title: row.Title, Detail: row.Detail, Status: row.Status, Metadata: bytes.Clone(row.Metadata),
		StartedAt: optionalTime(row.StartedAt), FinishedAt: optionalTime(row.FinishedAt), Version: row.Version,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
}

func agentChangeFromRow(row *db.DayorderAgentChange) model.AgentChange {
	return model.AgentChange{
		ID: uuid.UUID(row.ID.Bytes), RunID: uuid.UUID(row.RunID.Bytes), ChangeType: row.ChangeType,
		TargetType: row.TargetType, TargetID: optionalUUID(row.TargetID), BaseVersion: optionalInt64(row.BaseVersion),
		Patch: bytes.Clone(row.Patch), PreviewBefore: bytes.Clone(row.PreviewBefore), PreviewAfter: bytes.Clone(row.PreviewAfter),
		Reason: row.Reason, Status: row.Status, AcceptedAt: optionalTime(row.AcceptedAt), AppliedAt: optionalTime(row.AppliedAt),
		Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
}

func agentSourceRefFromRow(row *db.DayorderAgentSourceRef) model.AgentSourceRef {
	return model.AgentSourceRef{
		ID: uuid.UUID(row.ID.Bytes), RunID: uuid.UUID(row.RunID.Bytes), EntityType: row.EntityType,
		EntityID: uuid.UUID(row.EntityID.Bytes), EntityVersion: row.EntityVersion,
		LabelSnapshot: row.LabelSnapshot, CreatedAt: row.CreatedAt.Time.UTC(),
	}
}

func optionalInt64(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	converted := value.Int64
	return &converted
}

func pgOptionalInt64(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}
