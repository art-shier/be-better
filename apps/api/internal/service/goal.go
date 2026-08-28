package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const maxResourcePageSize = 100

type GoalStore interface {
	CreateGoal(context.Context, database.Tx, uuid.UUID, model.Goal) (model.Goal, error)
	GetGoal(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.Goal, error)
	ListGoals(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.Goal, error)
	UpdateGoal(context.Context, database.Tx, uuid.UUID, model.Goal, int64) (model.Goal, error)
	DeleteGoal(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.Goal, []model.GoalMilestone, []model.Task, error)
	CreateMilestone(context.Context, database.Tx, uuid.UUID, model.GoalMilestone) (model.GoalMilestone, error)
	GetMilestone(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.GoalMilestone, error)
	ListMilestones(context.Context, database.Tx, uuid.UUID, uuid.UUID) ([]model.GoalMilestone, error)
	UpdateMilestone(context.Context, database.Tx, uuid.UUID, model.GoalMilestone, int64) (model.GoalMilestone, error)
	DeleteMilestone(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.GoalMilestone, error)
}

type GoalService struct {
	store      GoalStore
	transactor UserTransactor
	commands   *CommandService
	cursors    *ResourceCursorCodec
	newUUID    func() uuid.UUID
}

type GoalPage struct {
	Goals      []model.Goal `json:"goals"`
	NextCursor string       `json:"nextCursor,omitempty"`
	HasMore    bool         `json:"hasMore"`
}

type CreateGoalInput struct {
	Title        string  `json:"title"`
	Why          string  `json:"why"`
	Area         string  `json:"area"`
	MetricType   string  `json:"metricType"`
	TargetValue  float64 `json:"targetValue"`
	CurrentValue float64 `json:"currentValue"`
	Unit         string  `json:"unit"`
	StartDate    string  `json:"startDate"`
	DueDate      *string `json:"dueDate"`
	Status       string  `json:"status"`
	Health       string  `json:"health"`
}

type UpdateGoalInput = CreateGoalInput

type CreateMilestoneInput struct {
	Title     string     `json:"title"`
	DueAt     *time.Time `json:"dueAt"`
	SortOrder int        `json:"sortOrder"`
}

type UpdateMilestoneInput struct {
	Title       string     `json:"title"`
	DueAt       *time.Time `json:"dueAt"`
	CompletedAt *time.Time `json:"completedAt"`
	SortOrder   int        `json:"sortOrder"`
}

func NewGoalService(store GoalStore, transactor UserTransactor, commands *CommandService, cursors *ResourceCursorCodec) (*GoalService, error) {
	if store == nil || transactor == nil || commands == nil || cursors == nil {
		return nil, errors.New("goal store, transactor, commands, and cursors are required")
	}
	return &GoalService{store: store, transactor: transactor, commands: commands, cursors: cursors, newUUID: uuid.New}, nil
}

func (service *GoalService) Create(ctx context.Context, mutation MutationContext, input CreateGoalInput) (model.Goal, error) {
	goal := model.Goal{ID: service.newUUID()}
	applyGoalInput(&goal, input)
	if err := validateGoal(goal); err != nil {
		return model.Goal{}, err
	}
	payload, _ := json.Marshal(input)
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "goal.create", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, createErr := service.store.CreateGoal(ctx, tx, mutation.UserID, goal)
		if createErr != nil {
			return CommandResult{}, createErr
		}
		body, audit := resourceJSON(created), model.AuditDraft{
			Action: "goal.create", AfterData: resourceJSON(created),
			Entities: []model.AuditEntity{{EntityType: "goal", EntityID: created.ID}},
		}
		return CommandResult{Status: 201, Body: body,
			Changes: []model.SyncChangeDraft{{EntityType: "goal", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}},
			Audits:  []model.AuditDraft{audit},
		}, nil
	})
	return decodeGoalResponse(response, err)
}

func (service *GoalService) Get(ctx context.Context, userID, goalID uuid.UUID) (model.Goal, error) {
	var goal model.Goal
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		goal, readErr = service.store.GetGoal(ctx, tx, userID, goalID)
		return readErr
	})
	return goal, err
}

func (service *GoalService) List(ctx context.Context, userID uuid.UUID, cursor string, pageSize int) (GoalPage, error) {
	if pageSize < 1 || pageSize > maxResourcePageSize {
		return GoalPage{}, fmt.Errorf("%w: page size must be between 1 and %d", ErrValidation, maxResourcePageSize)
	}
	var after *model.ResourcePosition
	if cursor != "" {
		decoded, err := service.cursors.Decode(userID, "goals", cursor)
		if err != nil {
			return GoalPage{}, err
		}
		after = &decoded
	}
	var goals []model.Goal
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		goals, readErr = service.store.ListGoals(ctx, tx, userID, after, pageSize+1)
		return readErr
	})
	if err != nil {
		return GoalPage{}, err
	}
	hasMore := len(goals) > pageSize
	if hasMore {
		goals = goals[:pageSize]
	}
	next := ""
	if hasMore && len(goals) > 0 {
		last := goals[len(goals)-1]
		next, err = service.cursors.Encode(userID, "goals", model.ResourcePosition{UpdatedAt: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return GoalPage{}, err
		}
	}
	return GoalPage{Goals: goals, NextCursor: next, HasMore: hasMore}, nil
}

func (service *GoalService) Update(ctx context.Context, mutation MutationContext, goalID uuid.UUID, expectedVersion int64, input UpdateGoalInput) (model.Goal, error) {
	if goalID == uuid.Nil || expectedVersion < 1 {
		return model.Goal{}, fmt.Errorf("%w: goal ID and expected version are required", ErrValidation)
	}
	candidate := model.Goal{ID: goalID}
	applyGoalInput(&candidate, input)
	if err := validateGoal(candidate); err != nil {
		return model.Goal{}, err
	}
	payload, _ := json.Marshal(struct {
		ID       uuid.UUID       `json:"id"`
		Expected int64           `json:"expectedVersion"`
		Input    UpdateGoalInput `json:"input"`
	}{goalID, expectedVersion, input})
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "goal.update", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetGoal(ctx, tx, mutation.UserID, goalID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		updated, updateErr := service.store.UpdateGoal(ctx, tx, mutation.UserID, candidate, expectedVersion)
		if updateErr != nil {
			return CommandResult{}, updateErr
		}
		return CommandResult{Status: 200, Body: resourceJSON(updated),
			Changes: []model.SyncChangeDraft{{EntityType: "goal", EntityID: updated.ID, Operation: "update", EntityVersion: updated.Version}},
			Audits:  []model.AuditDraft{{Action: "goal.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "goal", EntityID: updated.ID}}}},
		}, nil
	})
	return decodeGoalResponse(response, err)
}

func (service *GoalService) Delete(ctx context.Context, mutation MutationContext, goalID uuid.UUID, expectedVersion int64) error {
	if goalID == uuid.Nil || expectedVersion < 1 {
		return fmt.Errorf("%w: goal ID and expected version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"id": goalID, "expectedVersion": expectedVersion})
	_, err := service.commands.Execute(ctx, resourceCommand(mutation, "goal.delete", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetGoal(ctx, tx, mutation.UserID, goalID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		deleted, milestones, tasks, deleteErr := service.store.DeleteGoal(ctx, tx, mutation.UserID, goalID, expectedVersion)
		if deleteErr != nil {
			return CommandResult{}, deleteErr
		}
		changes := []model.SyncChangeDraft{{EntityType: "goal", EntityID: deleted.ID, Operation: "delete", EntityVersion: deleted.Version}}
		entities := []model.AuditEntity{{EntityType: "goal", EntityID: deleted.ID}}
		for _, milestone := range milestones {
			changes = append(changes, model.SyncChangeDraft{EntityType: "milestone", EntityID: milestone.ID, Operation: "delete", EntityVersion: milestone.Version})
			entities = append(entities, model.AuditEntity{EntityType: "milestone", EntityID: milestone.ID})
		}
		for _, task := range tasks {
			changes = append(changes, model.SyncChangeDraft{EntityType: "task", EntityID: task.ID, Operation: "update", EntityVersion: task.Version})
			entities = append(entities, model.AuditEntity{EntityType: "task", EntityID: task.ID})
		}
		return CommandResult{Status: 200, Body: resourceJSON(map[string]any{"id": deleted.ID, "version": deleted.Version}), Changes: changes,
			Audits: []model.AuditDraft{{Action: "goal.delete", BeforeData: resourceJSON(before), AfterData: resourceJSON(deleted), Entities: entities}},
		}, nil
	})
	return err
}

func (service *GoalService) CreateMilestone(ctx context.Context, mutation MutationContext, goalID uuid.UUID, input CreateMilestoneInput) (model.GoalMilestone, error) {
	if goalID == uuid.Nil {
		return model.GoalMilestone{}, fmt.Errorf("%w: goal ID is required", ErrValidation)
	}
	milestone := model.GoalMilestone{ID: service.newUUID(), GoalID: goalID, Title: strings.TrimSpace(input.Title), DueAt: utcTime(input.DueAt), SortOrder: input.SortOrder}
	if err := validateMilestone(milestone); err != nil {
		return model.GoalMilestone{}, err
	}
	payload, _ := json.Marshal(struct {
		GoalID uuid.UUID            `json:"goalId"`
		Input  CreateMilestoneInput `json:"input"`
	}{goalID, input})
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "milestone.create", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		if _, readErr := service.store.GetGoal(ctx, tx, mutation.UserID, goalID); readErr != nil {
			return CommandResult{}, readErr
		}
		created, createErr := service.store.CreateMilestone(ctx, tx, mutation.UserID, milestone)
		if createErr != nil {
			return CommandResult{}, createErr
		}
		return CommandResult{Status: 201, Body: resourceJSON(created),
			Changes: []model.SyncChangeDraft{{EntityType: "milestone", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}},
			Audits:  []model.AuditDraft{{Action: "milestone.create", AfterData: resourceJSON(created), Entities: []model.AuditEntity{{EntityType: "milestone", EntityID: created.ID}, {EntityType: "goal", EntityID: goalID}}}},
		}, nil
	})
	return decodeMilestoneResponse(response, err)
}

func (service *GoalService) ListMilestones(ctx context.Context, userID, goalID uuid.UUID) ([]model.GoalMilestone, error) {
	var milestones []model.GoalMilestone
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		if _, readErr := service.store.GetGoal(ctx, tx, userID, goalID); readErr != nil {
			return readErr
		}
		var readErr error
		milestones, readErr = service.store.ListMilestones(ctx, tx, userID, goalID)
		return readErr
	})
	return milestones, err
}

func (service *GoalService) GetMilestone(ctx context.Context, userID, milestoneID uuid.UUID) (model.GoalMilestone, error) {
	if userID == uuid.Nil || milestoneID == uuid.Nil {
		return model.GoalMilestone{}, fmt.Errorf("%w: user and milestone IDs are required", ErrValidation)
	}
	var milestone model.GoalMilestone
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		milestone, readErr = service.store.GetMilestone(ctx, tx, userID, milestoneID)
		return readErr
	})
	return milestone, err
}

func (service *GoalService) UpdateMilestone(ctx context.Context, mutation MutationContext, milestoneID uuid.UUID, expectedVersion int64, input UpdateMilestoneInput) (model.GoalMilestone, error) {
	candidate := model.GoalMilestone{ID: milestoneID, Title: strings.TrimSpace(input.Title), DueAt: utcTime(input.DueAt), CompletedAt: utcTime(input.CompletedAt), SortOrder: input.SortOrder}
	if expectedVersion < 1 {
		return model.GoalMilestone{}, fmt.Errorf("%w: expected version is required", ErrValidation)
	}
	if err := validateMilestone(candidate); err != nil {
		return model.GoalMilestone{}, err
	}
	payload, _ := json.Marshal(struct {
		ID       uuid.UUID            `json:"id"`
		Expected int64                `json:"expectedVersion"`
		Input    UpdateMilestoneInput `json:"input"`
	}{milestoneID, expectedVersion, input})
	response, err := service.commands.Execute(ctx, resourceCommand(mutation, "milestone.update", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetMilestone(ctx, tx, mutation.UserID, milestoneID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		candidate.GoalID = before.GoalID
		updated, updateErr := service.store.UpdateMilestone(ctx, tx, mutation.UserID, candidate, expectedVersion)
		if updateErr != nil {
			return CommandResult{}, updateErr
		}
		return CommandResult{Status: 200, Body: resourceJSON(updated),
			Changes: []model.SyncChangeDraft{{EntityType: "milestone", EntityID: updated.ID, Operation: "update", EntityVersion: updated.Version}},
			Audits:  []model.AuditDraft{{Action: "milestone.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "milestone", EntityID: updated.ID}}}},
		}, nil
	})
	return decodeMilestoneResponse(response, err)
}

func (service *GoalService) DeleteMilestone(ctx context.Context, mutation MutationContext, milestoneID uuid.UUID, expectedVersion int64) error {
	if milestoneID == uuid.Nil || expectedVersion < 1 {
		return fmt.Errorf("%w: milestone ID and expected version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"id": milestoneID, "expectedVersion": expectedVersion})
	_, err := service.commands.Execute(ctx, resourceCommand(mutation, "milestone.delete", payload), func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetMilestone(ctx, tx, mutation.UserID, milestoneID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		deleted, deleteErr := service.store.DeleteMilestone(ctx, tx, mutation.UserID, milestoneID, expectedVersion)
		if deleteErr != nil {
			return CommandResult{}, deleteErr
		}
		return CommandResult{Status: 200, Body: resourceJSON(map[string]any{"id": deleted.ID, "version": deleted.Version}),
			Changes: []model.SyncChangeDraft{{EntityType: "milestone", EntityID: deleted.ID, Operation: "delete", EntityVersion: deleted.Version}},
			Audits:  []model.AuditDraft{{Action: "milestone.delete", BeforeData: resourceJSON(before), AfterData: resourceJSON(deleted), Entities: []model.AuditEntity{{EntityType: "milestone", EntityID: deleted.ID}}}},
		}, nil
	})
	return err
}

func applyGoalInput(goal *model.Goal, input CreateGoalInput) {
	goal.Title = strings.TrimSpace(input.Title)
	goal.Why = strings.TrimSpace(input.Why)
	goal.Area = strings.TrimSpace(input.Area)
	goal.MetricType = input.MetricType
	goal.TargetValue = input.TargetValue
	goal.CurrentValue = input.CurrentValue
	goal.Unit = strings.TrimSpace(input.Unit)
	goal.StartDate = input.StartDate
	goal.DueDate = input.DueDate
	goal.Status = input.Status
	goal.Health = input.Health
	if goal.Status == "" {
		goal.Status = "active"
	}
	if goal.Health == "" {
		goal.Health = "normal"
	}
}

func validateGoal(goal model.Goal) error {
	if utf8.RuneCountInString(goal.Title) < 1 || utf8.RuneCountInString(goal.Title) > 240 || utf8.RuneCountInString(goal.Area) < 1 || utf8.RuneCountInString(goal.Area) > 40 {
		return fmt.Errorf("%w: invalid goal title or area", ErrValidation)
	}
	if _, ok := map[string]bool{"milestone": true, "numeric": true, "habit": true, "project": true}[goal.MetricType]; !ok {
		return fmt.Errorf("%w: invalid goal metric type", ErrValidation)
	}
	if goal.TargetValue <= 0 || goal.CurrentValue < 0 || math.IsNaN(goal.TargetValue) || math.IsInf(goal.TargetValue, 0) || math.IsNaN(goal.CurrentValue) || math.IsInf(goal.CurrentValue, 0) {
		return fmt.Errorf("%w: invalid goal metric values", ErrValidation)
	}
	if utf8.RuneCountInString(goal.Unit) > 32 || len(goal.Why) > 20000 {
		return fmt.Errorf("%w: goal text is too long", ErrValidation)
	}
	start, err := time.Parse(time.DateOnly, goal.StartDate)
	if err != nil {
		return fmt.Errorf("%w: invalid goal start date", ErrValidation)
	}
	if goal.DueDate != nil {
		due, dueErr := time.Parse(time.DateOnly, *goal.DueDate)
		if dueErr != nil || due.Before(start) {
			return fmt.Errorf("%w: invalid goal due date", ErrValidation)
		}
	}
	if !map[string]bool{"active": true, "paused": true, "completed": true, "abandoned": true}[goal.Status] || !map[string]bool{"normal": true, "attention": true, "stalled": true}[goal.Health] {
		return fmt.Errorf("%w: invalid goal status or health", ErrValidation)
	}
	return nil
}

func validateMilestone(milestone model.GoalMilestone) error {
	if milestone.ID == uuid.Nil || utf8.RuneCountInString(milestone.Title) < 1 || utf8.RuneCountInString(milestone.Title) > 240 || milestone.SortOrder < 0 {
		return fmt.Errorf("%w: invalid milestone fields", ErrValidation)
	}
	return nil
}

func resourceCommand(mutation MutationContext, name string, payload []byte) CommandRequest {
	return CommandRequest{UserID: mutation.UserID, DeviceID: mutation.DeviceID, MutationID: mutation.MutationID, RequestID: mutation.RequestID, CommandName: name, RequestBody: payload}
}

func resourceJSON(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }

func decodeGoalResponse(response CommandResponse, err error) (model.Goal, error) {
	if err != nil {
		return model.Goal{}, err
	}
	var goal model.Goal
	if decodeErr := json.Unmarshal(response.Body, &goal); decodeErr != nil {
		return model.Goal{}, fmt.Errorf("decode goal command response: %w", decodeErr)
	}
	return goal, nil
}

func decodeMilestoneResponse(response CommandResponse, err error) (model.GoalMilestone, error) {
	if err != nil {
		return model.GoalMilestone{}, err
	}
	var milestone model.GoalMilestone
	if decodeErr := json.Unmarshal(response.Body, &milestone); decodeErr != nil {
		return model.GoalMilestone{}, fmt.Errorf("decode milestone command response: %w", decodeErr)
	}
	return milestone, nil
}
