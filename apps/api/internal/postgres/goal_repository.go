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

type GoalRepository struct{}

func NewGoalRepository() *GoalRepository { return &GoalRepository{} }

func (*GoalRepository) CreateGoal(ctx context.Context, tx database.Tx, userID uuid.UUID, goal model.Goal) (model.Goal, error) {
	row, err := db.New(tx).CreateGoal(ctx, db.CreateGoalParams{
		ID: pgUUID(goal.ID), UserID: pgUUID(userID), Title: goal.Title, Why: goal.Why,
		Area: goal.Area, MetricType: goal.MetricType, TargetValue: pgNumeric(goal.TargetValue),
		CurrentValue: pgNumeric(goal.CurrentValue), Unit: goal.Unit, StartDate: pgDate(goal.StartDate),
		DueDate: pgOptionalDate(goal.DueDate), Status: goal.Status, Health: goal.Health,
	})
	if err != nil {
		return model.Goal{}, mapDatabaseError("create goal", err)
	}
	return goalFromRow(row), nil
}

func (*GoalRepository) GetGoal(ctx context.Context, tx database.Tx, userID, goalID uuid.UUID) (model.Goal, error) {
	row, err := db.New(tx).GetGoal(ctx, pgUUID(userID), pgUUID(goalID))
	if err != nil {
		return model.Goal{}, mapDatabaseError("get goal", err)
	}
	return goalFromRow(row), nil
}

func (*GoalRepository) ListGoals(ctx context.Context, tx database.Tx, userID uuid.UUID, after *model.ResourcePosition, limit int) ([]model.Goal, error) {
	afterTime := pgtype.Timestamptz{}
	afterID := pgtype.UUID{}
	if after != nil {
		afterTime = pgTime(after.UpdatedAt)
		afterID = pgUUID(after.ID)
	}
	rows, err := db.New(tx).ListGoals(ctx, pgUUID(userID), afterTime, afterID, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list goals: %w", err)
	}
	goals := make([]model.Goal, 0, len(rows))
	for _, row := range rows {
		goals = append(goals, goalFromRow(row))
	}
	return goals, nil
}

func (*GoalRepository) UpdateGoal(ctx context.Context, tx database.Tx, userID uuid.UUID, goal model.Goal, expectedVersion int64) (model.Goal, error) {
	queries := db.New(tx)
	row, err := queries.UpdateGoal(ctx, db.UpdateGoalParams{
		Title: goal.Title, Why: goal.Why, Area: goal.Area, MetricType: goal.MetricType,
		TargetValue: pgNumeric(goal.TargetValue), CurrentValue: pgNumeric(goal.CurrentValue),
		Unit: goal.Unit, StartDate: pgDate(goal.StartDate), DueDate: pgOptionalDate(goal.DueDate),
		Status: goal.Status, Health: goal.Health, UserID: pgUUID(userID), ID: pgUUID(goal.ID),
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return model.Goal{}, goalWriteError(ctx, queries, userID, goal.ID, "update goal", err)
	}
	return goalFromRow(row), nil
}

func (*GoalRepository) DeleteGoal(ctx context.Context, tx database.Tx, userID, goalID uuid.UUID, expectedVersion int64) (model.Goal, []model.GoalMilestone, []model.Task, error) {
	queries := db.New(tx)
	row, err := queries.SoftDeleteGoal(ctx, pgUUID(userID), pgUUID(goalID), expectedVersion)
	if err != nil {
		return model.Goal{}, nil, nil, goalWriteError(ctx, queries, userID, goalID, "delete goal", err)
	}
	milestoneRows, err := queries.SoftDeleteGoalMilestones(ctx, pgUUID(userID), pgUUID(goalID))
	if err != nil {
		return model.Goal{}, nil, nil, fmt.Errorf("delete goal milestones: %w", err)
	}
	taskRows, err := queries.DetachGoalTasks(ctx, pgUUID(userID), pgUUID(goalID))
	if err != nil {
		return model.Goal{}, nil, nil, fmt.Errorf("detach goal tasks: %w", err)
	}
	milestones := make([]model.GoalMilestone, 0, len(milestoneRows))
	for _, milestoneRow := range milestoneRows {
		milestones = append(milestones, milestoneFromRow(milestoneRow))
	}
	tasks := make([]model.Task, 0, len(taskRows))
	for _, taskRow := range taskRows {
		tasks = append(tasks, taskFromRow(taskRow))
	}
	return goalFromRow(row), milestones, tasks, nil
}

func (*GoalRepository) CreateMilestone(ctx context.Context, tx database.Tx, userID uuid.UUID, milestone model.GoalMilestone) (model.GoalMilestone, error) {
	row, err := db.New(tx).CreateGoalMilestone(ctx, db.CreateGoalMilestoneParams{
		ID: pgUUID(milestone.ID), UserID: pgUUID(userID), GoalID: pgUUID(milestone.GoalID),
		Title: milestone.Title, DueAt: pgOptionalTime(milestone.DueAt),
		CompletedAt: pgOptionalTime(milestone.CompletedAt), SortOrder: int32(milestone.SortOrder),
	})
	if err != nil {
		return model.GoalMilestone{}, mapDatabaseError("create milestone", err)
	}
	return milestoneFromRow(row), nil
}

func (*GoalRepository) GetMilestone(ctx context.Context, tx database.Tx, userID, milestoneID uuid.UUID) (model.GoalMilestone, error) {
	row, err := db.New(tx).GetGoalMilestone(ctx, pgUUID(userID), pgUUID(milestoneID))
	if err != nil {
		return model.GoalMilestone{}, mapDatabaseError("get milestone", err)
	}
	return milestoneFromRow(row), nil
}

func (*GoalRepository) ListMilestones(ctx context.Context, tx database.Tx, userID, goalID uuid.UUID) ([]model.GoalMilestone, error) {
	rows, err := db.New(tx).ListGoalMilestones(ctx, pgUUID(userID), pgUUID(goalID))
	if err != nil {
		return nil, fmt.Errorf("list milestones: %w", err)
	}
	milestones := make([]model.GoalMilestone, 0, len(rows))
	for _, row := range rows {
		milestones = append(milestones, milestoneFromRow(row))
	}
	return milestones, nil
}

func (*GoalRepository) UpdateMilestone(ctx context.Context, tx database.Tx, userID uuid.UUID, milestone model.GoalMilestone, expectedVersion int64) (model.GoalMilestone, error) {
	queries := db.New(tx)
	row, err := queries.UpdateGoalMilestone(ctx, db.UpdateGoalMilestoneParams{
		Title: milestone.Title, DueAt: pgOptionalTime(milestone.DueAt),
		CompletedAt: pgOptionalTime(milestone.CompletedAt), SortOrder: int32(milestone.SortOrder),
		UserID: pgUUID(userID), ID: pgUUID(milestone.ID), ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return model.GoalMilestone{}, milestoneWriteError(ctx, queries, userID, milestone.ID, "update milestone", err)
	}
	return milestoneFromRow(row), nil
}

func (*GoalRepository) DeleteMilestone(ctx context.Context, tx database.Tx, userID, milestoneID uuid.UUID, expectedVersion int64) (model.GoalMilestone, error) {
	queries := db.New(tx)
	row, err := queries.SoftDeleteGoalMilestone(ctx, pgUUID(userID), pgUUID(milestoneID), expectedVersion)
	if err != nil {
		return model.GoalMilestone{}, milestoneWriteError(ctx, queries, userID, milestoneID, "delete milestone", err)
	}
	return milestoneFromRow(row), nil
}

func goalWriteError(ctx context.Context, queries *db.Queries, userID, goalID uuid.UUID, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	if _, readErr := queries.GetGoal(ctx, pgUUID(userID), pgUUID(goalID)); errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	} else if readErr != nil {
		return fmt.Errorf("check goal after failed write: %w", readErr)
	}
	return model.ErrConflict
}

func milestoneWriteError(ctx context.Context, queries *db.Queries, userID, milestoneID uuid.UUID, operation string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapDatabaseError(operation, err)
	}
	if _, readErr := queries.GetGoalMilestone(ctx, pgUUID(userID), pgUUID(milestoneID)); errors.Is(readErr, pgx.ErrNoRows) {
		return model.ErrNotFound
	} else if readErr != nil {
		return fmt.Errorf("check milestone after failed write: %w", readErr)
	}
	return model.ErrConflict
}

func goalFromRow(row *db.DayorderGoal) model.Goal {
	return model.Goal{
		ID: uuid.UUID(row.ID.Bytes), Title: row.Title, Why: row.Why, Area: row.Area,
		MetricType: row.MetricType, TargetValue: numericFloat(row.TargetValue),
		CurrentValue: numericFloat(row.CurrentValue), Unit: row.Unit,
		StartDate: dateString(row.StartDate), DueDate: optionalDateString(row.DueDate),
		Status: row.Status, Health: row.Health, Version: row.Version,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt),
	}
}

func milestoneFromRow(row *db.DayorderGoalMilestone) model.GoalMilestone {
	return model.GoalMilestone{
		ID: uuid.UUID(row.ID.Bytes), GoalID: uuid.UUID(row.GoalID.Bytes), Title: row.Title,
		DueAt: optionalTime(row.DueAt), CompletedAt: optionalTime(row.CompletedAt),
		SortOrder: int(row.SortOrder), Version: row.Version, CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(), DeletedAt: optionalTime(row.DeletedAt),
	}
}
