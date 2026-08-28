package postgres

import (
	"context"
	"errors"
	"fmt"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type TaskRepository struct{}

func NewTaskRepository() *TaskRepository { return &TaskRepository{} }

func (*TaskRepository) CreateTask(ctx context.Context, tx database.Tx, userID uuid.UUID, task model.Task) (model.Task, error) {
	row, err := db.New(tx).CreateTask(ctx, db.CreateTaskParams{
		ID: pgUUID(task.ID), UserID: pgUUID(userID), Title: task.Title, Status: task.Status,
		Priority: task.Priority, EstimateMinutes: int32(task.EstimateMinutes), DueAt: pgOptionalTime(task.DueAt),
		ScheduledStart: pgOptionalTime(task.ScheduledStart), ScheduledEnd: pgOptionalTime(task.ScheduledEnd),
		GoalID: pgOptionalUUID(task.GoalID), SourceRecordID: pgOptionalUUID(task.SourceRecordID),
		CompletedAt: pgOptionalTime(task.CompletedAt),
	})
	if err != nil {
		return model.Task{}, mapDatabaseError("create task", err)
	}
	return taskFromRow(row), nil
}

func (*TaskRepository) GetTask(ctx context.Context, tx database.Tx, userID, taskID uuid.UUID) (model.Task, error) {
	row, err := db.New(tx).GetTask(ctx, pgUUID(userID), pgUUID(taskID))
	if err != nil {
		return model.Task{}, mapDatabaseError("get task", err)
	}
	return taskFromRow(row), nil
}

func (*TaskRepository) ListTasks(ctx context.Context, tx database.Tx, userID uuid.UUID, status *string, after *model.ResourcePosition, limit int) ([]model.Task, error) {
	statusValue := pgtype.Text{}
	if status != nil {
		statusValue = pgtype.Text{String: *status, Valid: true}
	}
	afterTime := pgtype.Timestamptz{}
	afterID := pgtype.UUID{}
	if after != nil {
		afterTime = pgTime(after.UpdatedAt)
		afterID = pgUUID(after.ID)
	}
	rows, err := db.New(tx).ListTasks(ctx, db.ListTasksParams{
		UserID: pgUUID(userID), Status: statusValue, AfterUpdatedAt: afterTime,
		AfterID: afterID, PageSize: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	tasks := make([]model.Task, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, taskFromRow(row))
	}
	return tasks, nil
}

func (*TaskRepository) UpdateTask(ctx context.Context, tx database.Tx, userID uuid.UUID, task model.Task, expectedVersion int64) (model.Task, error) {
	queries := db.New(tx)
	row, err := queries.UpdateTask(ctx, db.UpdateTaskParams{
		Title: task.Title, Status: task.Status, Priority: task.Priority,
		EstimateMinutes: int32(task.EstimateMinutes), DueAt: pgOptionalTime(task.DueAt),
		ScheduledStart: pgOptionalTime(task.ScheduledStart), ScheduledEnd: pgOptionalTime(task.ScheduledEnd),
		GoalID: pgOptionalUUID(task.GoalID), SourceRecordID: pgOptionalUUID(task.SourceRecordID),
		CompletedAt: pgOptionalTime(task.CompletedAt), UserID: pgUUID(userID), ID: pgUUID(task.ID),
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return model.Task{}, taskWriteError(ctx, queries, userID, task.ID, "update task", err)
	}
	return taskFromRow(row), nil
}

func (*TaskRepository) DeleteTask(ctx context.Context, tx database.Tx, userID, taskID uuid.UUID, expectedVersion int64) (model.Task, error) {
	queries := db.New(tx)
	row, err := queries.SoftDeleteTask(ctx, pgUUID(userID), pgUUID(taskID), expectedVersion)
	if err != nil {
		return model.Task{}, taskWriteError(ctx, queries, userID, taskID, "delete task", err)
	}
	return taskFromRow(row), nil
}

func taskWriteError(ctx context.Context, queries *db.Queries, userID, taskID uuid.UUID, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	if _, readErr := queries.GetTask(ctx, pgUUID(userID), pgUUID(taskID)); errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	} else if readErr != nil {
		return fmt.Errorf("check task after failed write: %w", readErr)
	}
	return model.ErrConflict
}

func taskFromRow(row *db.DayorderTask) model.Task {
	return model.Task{
		ID: uuid.UUID(row.ID.Bytes), Title: row.Title, Status: row.Status, Priority: row.Priority,
		EstimateMinutes: int(row.EstimateMinutes), DueAt: optionalTime(row.DueAt),
		ScheduledStart: optionalTime(row.ScheduledStart), ScheduledEnd: optionalTime(row.ScheduledEnd),
		GoalID: optionalUUID(row.GoalID), SourceRecordID: optionalUUID(row.SourceRecordID),
		CompletedAt: optionalTime(row.CompletedAt), Version: row.Version,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt),
	}
}
