package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type TaskStore interface {
	CreateTask(context.Context, database.Tx, uuid.UUID, model.Task) (model.Task, error)
	GetTask(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.Task, error)
	ListTasks(context.Context, database.Tx, uuid.UUID, *string, *model.ResourcePosition, int) ([]model.Task, error)
	UpdateTask(context.Context, database.Tx, uuid.UUID, model.Task, int64) (model.Task, error)
	DeleteTask(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) (model.Task, error)
}

type TaskService struct {
	store      TaskStore
	transactor UserTransactor
	commands   *CommandService
	cursors    *ResourceCursorCodec
	newUUID    func() uuid.UUID
	now        func() time.Time
}

type TaskPage struct {
	Tasks      []model.Task `json:"tasks"`
	NextCursor string       `json:"nextCursor,omitempty"`
	HasMore    bool         `json:"hasMore"`
}

type TaskInput struct {
	ID              *uuid.UUID `json:"id,omitempty"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	Priority        string     `json:"priority"`
	EstimateMinutes int        `json:"estimateMinutes"`
	DueAt           *time.Time `json:"dueAt"`
	ScheduledStart  *time.Time `json:"scheduledStart"`
	ScheduledEnd    *time.Time `json:"scheduledEnd"`
	GoalID          *uuid.UUID `json:"goalId"`
	SourceRecordID  *uuid.UUID `json:"sourceRecordId"`
}

func NewTaskService(store TaskStore, transactor UserTransactor, commands *CommandService, cursors *ResourceCursorCodec) (*TaskService, error) {
	if store == nil || transactor == nil || commands == nil || cursors == nil {
		return nil, errors.New("task store, transactor, commands, and cursors are required")
	}
	return &TaskService{store: store, transactor: transactor, commands: commands, cursors: cursors, newUUID: uuid.New, now: time.Now}, nil
}

func (service *TaskService) Create(ctx context.Context, mutation MutationContext, input TaskInput) (model.Task, error) {
	taskID := service.newUUID()
	if input.ID != nil {
		taskID = *input.ID
	}
	task := model.Task{ID: taskID}
	service.applyTaskInput(&task, input)
	if err := validateTask(task); err != nil {
		return model.Task{}, err
	}
	payload, _ := json.Marshal(input)
	response, err := executeResourceCommand(ctx, service.commands, mutation, "task.create", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, createErr := service.store.CreateTask(ctx, tx, mutation.UserID, task)
		if createErr != nil {
			return CommandResult{}, createErr
		}
		return CommandResult{Status: 201, Body: resourceJSON(created),
			Changes: []model.SyncChangeDraft{{EntityType: "task", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}},
			Audits:  []model.AuditDraft{{Action: "task.create", AfterData: resourceJSON(created), Entities: []model.AuditEntity{{EntityType: "task", EntityID: created.ID}}}},
		}, nil
	})
	return decodeTaskResponse(response, err)
}

func (service *TaskService) Get(ctx context.Context, userID, taskID uuid.UUID) (model.Task, error) {
	var task model.Task
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		task, readErr = service.store.GetTask(ctx, tx, userID, taskID)
		return readErr
	})
	return task, err
}

func (service *TaskService) List(ctx context.Context, userID uuid.UUID, status, cursor string, pageSize int) (TaskPage, error) {
	if pageSize < 1 || pageSize > maxResourcePageSize {
		return TaskPage{}, fmt.Errorf("%w: invalid page size", ErrValidation)
	}
	var statusFilter *string
	if status != "" {
		if !validTaskStatus(status) {
			return TaskPage{}, fmt.Errorf("%w: invalid task status", ErrValidation)
		}
		statusFilter = &status
	}
	scope := "tasks:" + status
	var after *model.ResourcePosition
	if cursor != "" {
		decoded, err := service.cursors.Decode(userID, scope, cursor)
		if err != nil {
			return TaskPage{}, err
		}
		after = &decoded
	}
	var tasks []model.Task
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		tasks, readErr = service.store.ListTasks(ctx, tx, userID, statusFilter, after, pageSize+1)
		return readErr
	})
	if err != nil {
		return TaskPage{}, err
	}
	hasMore := len(tasks) > pageSize
	if hasMore {
		tasks = tasks[:pageSize]
	}
	next := ""
	if hasMore && len(tasks) > 0 {
		last := tasks[len(tasks)-1]
		next, err = service.cursors.Encode(userID, scope, model.ResourcePosition{UpdatedAt: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return TaskPage{}, err
		}
	}
	return TaskPage{Tasks: tasks, NextCursor: next, HasMore: hasMore}, nil
}

func (service *TaskService) Update(ctx context.Context, mutation MutationContext, taskID uuid.UUID, expectedVersion int64, input TaskInput) (model.Task, error) {
	if taskID == uuid.Nil || expectedVersion < 1 {
		return model.Task{}, fmt.Errorf("%w: task ID and expected version are required", ErrValidation)
	}
	candidate := model.Task{ID: taskID}
	service.applyTaskInput(&candidate, input)
	if err := validateTask(candidate); err != nil {
		return model.Task{}, err
	}
	payload, _ := json.Marshal(struct {
		ID       uuid.UUID `json:"id"`
		Expected int64     `json:"expectedVersion"`
		Input    TaskInput `json:"input"`
	}{taskID, expectedVersion, input})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "task.update", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetTask(ctx, tx, mutation.UserID, taskID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		if candidate.Status == "done" && before.Status == "done" {
			candidate.CompletedAt = before.CompletedAt
		}
		updated, updateErr := service.store.UpdateTask(ctx, tx, mutation.UserID, candidate, expectedVersion)
		if updateErr != nil {
			return CommandResult{}, updateErr
		}
		return CommandResult{Status: 200, Body: resourceJSON(updated),
			Changes: []model.SyncChangeDraft{{EntityType: "task", EntityID: updated.ID, Operation: "update", EntityVersion: updated.Version}},
			Audits:  []model.AuditDraft{{Action: "task.update", BeforeData: resourceJSON(before), AfterData: resourceJSON(updated), Entities: []model.AuditEntity{{EntityType: "task", EntityID: updated.ID}}}},
		}, nil
	})
	return decodeTaskResponse(response, err)
}

func (service *TaskService) Delete(ctx context.Context, mutation MutationContext, taskID uuid.UUID, expectedVersion int64) error {
	if taskID == uuid.Nil || expectedVersion < 1 {
		return fmt.Errorf("%w: task ID and expected version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"id": taskID, "expectedVersion": expectedVersion})
	_, err := executeResourceCommand(ctx, service.commands, mutation, "task.delete", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		before, readErr := service.store.GetTask(ctx, tx, mutation.UserID, taskID)
		if readErr != nil {
			return CommandResult{}, readErr
		}
		deleted, deleteErr := service.store.DeleteTask(ctx, tx, mutation.UserID, taskID, expectedVersion)
		if deleteErr != nil {
			return CommandResult{}, deleteErr
		}
		return CommandResult{Status: 200, Body: resourceJSON(map[string]any{"id": deleted.ID, "version": deleted.Version}),
			Changes: []model.SyncChangeDraft{{EntityType: "task", EntityID: deleted.ID, Operation: "delete", EntityVersion: deleted.Version}},
			Audits:  []model.AuditDraft{{Action: "task.delete", BeforeData: resourceJSON(before), AfterData: resourceJSON(deleted), Entities: []model.AuditEntity{{EntityType: "task", EntityID: deleted.ID}}}},
		}, nil
	})
	return err
}

func (service *TaskService) applyTaskInput(task *model.Task, input TaskInput) {
	task.Title = strings.TrimSpace(input.Title)
	task.Status = input.Status
	task.Priority = input.Priority
	task.EstimateMinutes = input.EstimateMinutes
	task.DueAt = utcTime(input.DueAt)
	task.ScheduledStart = utcTime(input.ScheduledStart)
	task.ScheduledEnd = utcTime(input.ScheduledEnd)
	task.GoalID = input.GoalID
	task.SourceRecordID = input.SourceRecordID
	if task.Status == "" {
		task.Status = "todo"
	}
	if task.Priority == "" {
		task.Priority = "normal"
	}
	if task.Status == "done" {
		now := service.now().UTC()
		task.CompletedAt = &now
	} else {
		task.CompletedAt = nil
	}
}

func validateTask(task model.Task) error {
	if utf8.RuneCountInString(task.Title) < 1 || utf8.RuneCountInString(task.Title) > 240 || !validTaskStatus(task.Status) || (task.Priority != "normal" && task.Priority != "important") || task.EstimateMinutes < 0 {
		return fmt.Errorf("%w: invalid task fields", ErrValidation)
	}
	if task.ScheduledEnd != nil && (task.ScheduledStart == nil || task.ScheduledEnd.Before(*task.ScheduledStart)) {
		return fmt.Errorf("%w: invalid task schedule", ErrValidation)
	}
	if task.Status == "done" && task.CompletedAt == nil {
		return fmt.Errorf("%w: completed task requires completion time", ErrValidation)
	}
	if (task.GoalID != nil && *task.GoalID == uuid.Nil) || (task.SourceRecordID != nil && *task.SourceRecordID == uuid.Nil) {
		return fmt.Errorf("%w: related resource IDs cannot be empty", ErrValidation)
	}
	return nil
}

func validTaskStatus(status string) bool {
	return map[string]bool{"todo": true, "doing": true, "done": true, "archived": true}[status]
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func decodeTaskResponse(response CommandResponse, err error) (model.Task, error) {
	if err != nil {
		return model.Task{}, err
	}
	var task model.Task
	if decodeErr := json.Unmarshal(response.Body, &task); decodeErr != nil {
		return model.Task{}, fmt.Errorf("decode task command response: %w", decodeErr)
	}
	return task, nil
}
