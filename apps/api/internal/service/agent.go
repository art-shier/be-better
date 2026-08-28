package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	maxAgentIntentRunes = 2000
	maxAgentScopeBytes  = 8 * 1024
	maxAgentPatchBytes  = 16 * 1024
	maxAgentReasonRunes = 2000
)

var agentScopeDomains = map[string]struct{}{
	"goals": {}, "tasks": {}, "calendar": {}, "records": {}, "notes": {},
}

type AgentStore interface {
	CreateRun(context.Context, database.Tx, uuid.UUID, model.AgentRun) (model.AgentRun, error)
	GetRun(context.Context, database.Tx, uuid.UUID, uuid.UUID) (model.AgentRun, error)
	ListRuns(context.Context, database.Tx, uuid.UUID, *model.ResourcePosition, int) ([]model.AgentRun, error)
	ApplyChange(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.AgentApplyResult, error)
	RejectChange(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.AgentApplyResult, error)
	StopRun(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, time.Time) (model.AgentRun, error)
}

type AgentService struct {
	store      AgentStore
	transactor UserTransactor
	commands   *CommandService
	cursors    *ResourceCursorCodec
	newUUID    func() uuid.UUID
	now        func() time.Time
}

type StartAgentInput struct {
	Intent     string           `json:"intent"`
	ActionMode string           `json:"actionMode"`
	Scope      model.AgentScope `json:"scope"`
}

type AgentRunPage struct {
	Runs       []model.AgentRun `json:"runs"`
	NextCursor string           `json:"nextCursor,omitempty"`
	HasMore    bool             `json:"hasMore"`
}

func NewAgentService(store AgentStore, transactor UserTransactor, commands *CommandService, cursors *ResourceCursorCodec) (*AgentService, error) {
	if store == nil || transactor == nil || commands == nil || cursors == nil {
		return nil, errors.New("agent store, transactor, commands, and cursors are required")
	}
	return &AgentService{
		store: store, transactor: transactor, commands: commands, cursors: cursors,
		newUUID: uuid.New, now: time.Now,
	}, nil
}

func (service *AgentService) Create(ctx context.Context, mutation MutationContext, input StartAgentInput) (model.AgentRun, error) {
	input.Intent = strings.TrimSpace(input.Intent)
	if err := validateAgentStart(input); err != nil {
		return model.AgentRun{}, err
	}
	scope, err := json.Marshal(input.Scope)
	if err != nil {
		return model.AgentRun{}, fmt.Errorf("encode agent scope: %w", err)
	}
	run := model.AgentRun{
		ID: service.newUUID(), Intent: input.Intent, Status: "ready", ActionMode: input.ActionMode,
		Scope: scope, Steps: []model.AgentStep{}, Changes: []model.AgentChange{}, SourceRefs: []model.AgentSourceRef{},
	}
	payload, _ := json.Marshal(input)
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.run.create", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		created, createErr := service.store.CreateRun(ctx, tx, mutation.UserID, run)
		if createErr != nil {
			return CommandResult{}, createErr
		}
		outboxPayload := resourceJSON(map[string]any{"runId": created.ID})
		return CommandResult{
			Status: 201, Body: resourceJSON(created),
			Changes: []model.SyncChangeDraft{{EntityType: "agent_run", EntityID: created.ID, Operation: "create", EntityVersion: created.Version}},
			Audits: []model.AuditDraft{{
				Action: "agent.run.create", AfterData: resourceJSON(created),
				Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: created.ID}},
			}},
			Outbox: []model.OutboxDraft{{
				EventType: "agent.run.requested", AggregateType: "agent_run", AggregateID: created.ID,
				Payload: outboxPayload,
			}},
		}, nil
	})
	if err != nil {
		return model.AgentRun{}, err
	}
	var created model.AgentRun
	if err = json.Unmarshal(response.Body, &created); err != nil {
		return model.AgentRun{}, fmt.Errorf("decode created agent run: %w", err)
	}
	return created, nil
}

func (service *AgentService) Get(ctx context.Context, userID, runID uuid.UUID) (model.AgentRun, error) {
	if userID == uuid.Nil || runID == uuid.Nil {
		return model.AgentRun{}, fmt.Errorf("%w: agent user and run IDs are required", ErrValidation)
	}
	var run model.AgentRun
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		run, readErr = service.store.GetRun(ctx, tx, userID, runID)
		return readErr
	})
	return run, err
}

func (service *AgentService) List(ctx context.Context, userID uuid.UUID, cursor string, limit int) (AgentRunPage, error) {
	if userID == uuid.Nil || limit < 1 || limit > maxResourcePageSize {
		return AgentRunPage{}, fmt.Errorf("%w: invalid agent list request", ErrValidation)
	}
	var after *model.ResourcePosition
	if cursor != "" {
		decoded, err := service.cursors.Decode(userID, "agent-runs", cursor)
		if err != nil {
			return AgentRunPage{}, err
		}
		after = &decoded
	}
	var runs []model.AgentRun
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		runs, readErr = service.store.ListRuns(ctx, tx, userID, after, limit+1)
		return readErr
	})
	if err != nil {
		return AgentRunPage{}, err
	}
	hasMore := len(runs) > limit
	if hasMore {
		runs = runs[:limit]
	}
	next := ""
	if hasMore {
		last := runs[len(runs)-1]
		next, err = service.cursors.Encode(userID, "agent-runs", model.ResourcePosition{UpdatedAt: last.CreatedAt, ID: last.ID})
	}
	return AgentRunPage{Runs: runs, NextCursor: next, HasMore: hasMore}, err
}

func (service *AgentService) Accept(ctx context.Context, mutation MutationContext, changeID uuid.UUID, expectedVersion int64) (model.AgentApplyResult, error) {
	if changeID == uuid.Nil || expectedVersion < 1 {
		return model.AgentApplyResult{}, fmt.Errorf("%w: agent change ID and version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"changeId": changeID, "expectedVersion": expectedVersion})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.change.accept", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		applied, applyErr := service.store.ApplyChange(ctx, tx, mutation.UserID, changeID, expectedVersion, service.now().UTC())
		if applyErr != nil {
			return CommandResult{}, applyErr
		}
		changes := []model.SyncChangeDraft{
			{EntityType: applied.TargetType, EntityID: applied.TargetID, Operation: applied.TargetOperation, EntityVersion: applied.TargetVersion},
			{EntityType: "agent_change", EntityID: applied.Change.ID, Operation: "update", EntityVersion: applied.Change.Version},
		}
		if applied.RunUpdated {
			changes = append(changes, model.SyncChangeDraft{EntityType: "agent_run", EntityID: applied.Run.ID, Operation: "update", EntityVersion: applied.Run.Version})
		}
		actorID := applied.Run.ID
		metadata := resourceJSON(map[string]any{
			"runId": applied.Run.ID, "changeId": applied.Change.ID,
			"confirmedBy": mutation.UserID, "targetVersion": applied.TargetVersion,
		})
		return CommandResult{
			Status: 200, Body: resourceJSON(applied), Changes: changes,
			Audits: []model.AuditDraft{{
				ActorType: "agent", ActorID: &actorID, Action: "agent.change.apply",
				BeforeData: applied.BeforeData, AfterData: applied.AfterData, Metadata: metadata,
				Entities: []model.AuditEntity{
					{EntityType: applied.TargetType, EntityID: applied.TargetID},
					{EntityType: "agent_run", EntityID: applied.Run.ID},
				},
			}},
		}, nil
	})
	if err != nil {
		return model.AgentApplyResult{}, err
	}
	var applied model.AgentApplyResult
	if err = json.Unmarshal(response.Body, &applied); err != nil {
		return model.AgentApplyResult{}, fmt.Errorf("decode accepted agent change: %w", err)
	}
	return applied, nil
}

func (service *AgentService) Reject(ctx context.Context, mutation MutationContext, changeID uuid.UUID, expectedVersion int64) (model.AgentApplyResult, error) {
	if changeID == uuid.Nil || expectedVersion < 1 {
		return model.AgentApplyResult{}, fmt.Errorf("%w: agent change ID and version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"changeId": changeID, "expectedVersion": expectedVersion})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.change.reject", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		result, rejectErr := service.store.RejectChange(ctx, tx, mutation.UserID, changeID, expectedVersion, service.now().UTC())
		if rejectErr != nil {
			return CommandResult{}, rejectErr
		}
		changes := []model.SyncChangeDraft{{EntityType: "agent_change", EntityID: result.Change.ID, Operation: "update", EntityVersion: result.Change.Version}}
		if result.RunUpdated {
			changes = append(changes, model.SyncChangeDraft{EntityType: "agent_run", EntityID: result.Run.ID, Operation: "update", EntityVersion: result.Run.Version})
		}
		return CommandResult{
			Status: 200, Body: resourceJSON(result), Changes: changes,
			Audits: []model.AuditDraft{{
				Action: "agent.change.reject", Metadata: resourceJSON(map[string]any{"runId": result.Run.ID, "changeId": result.Change.ID}),
				Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: result.Run.ID}},
			}},
		}, nil
	})
	if err != nil {
		return model.AgentApplyResult{}, err
	}
	var result model.AgentApplyResult
	if err = json.Unmarshal(response.Body, &result); err != nil {
		return model.AgentApplyResult{}, fmt.Errorf("decode rejected agent change: %w", err)
	}
	return result, nil
}

func (service *AgentService) Stop(ctx context.Context, mutation MutationContext, runID uuid.UUID, expectedVersion int64) (model.AgentRun, error) {
	if runID == uuid.Nil || expectedVersion < 1 {
		return model.AgentRun{}, fmt.Errorf("%w: agent run ID and version are required", ErrValidation)
	}
	payload, _ := json.Marshal(map[string]any{"runId": runID, "expectedVersion": expectedVersion})
	response, err := executeResourceCommand(ctx, service.commands, mutation, "agent.run.stop", payload, func(ctx context.Context, tx database.Tx) (CommandResult, error) {
		run, stopErr := service.store.StopRun(ctx, tx, mutation.UserID, runID, expectedVersion, service.now().UTC())
		if stopErr != nil {
			return CommandResult{}, stopErr
		}
		return CommandResult{
			Status: 200, Body: resourceJSON(run),
			Changes: []model.SyncChangeDraft{{EntityType: "agent_run", EntityID: run.ID, Operation: "update", EntityVersion: run.Version}},
			Audits:  []model.AuditDraft{{Action: "agent.run.stop", AfterData: resourceJSON(run), Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: run.ID}}}},
		}, nil
	})
	if err != nil {
		return model.AgentRun{}, err
	}
	var run model.AgentRun
	if err = json.Unmarshal(response.Body, &run); err != nil {
		return model.AgentRun{}, fmt.Errorf("decode stopped agent run: %w", err)
	}
	return run, nil
}

func validateAgentStart(input StartAgentInput) error {
	if utf8.RuneCountInString(input.Intent) < 1 || utf8.RuneCountInString(input.Intent) > maxAgentIntentRunes {
		return fmt.Errorf("%w: agent intent must contain 1 to %d characters", ErrValidation, maxAgentIntentRunes)
	}
	if input.ActionMode != "read" && input.ActionMode != "confirm" {
		return fmt.Errorf("%w: invalid agent action mode", ErrValidation)
	}
	if len(input.Scope.Domains) < 1 || len(input.Scope.Domains) > len(agentScopeDomains) || len(input.Scope.EntityIDs) > 100 {
		return fmt.Errorf("%w: invalid agent scope size", ErrValidation)
	}
	seenDomains := map[string]bool{}
	for _, domain := range input.Scope.Domains {
		if _, ok := agentScopeDomains[domain]; !ok || seenDomains[domain] {
			return fmt.Errorf("%w: invalid or duplicate agent scope domain", ErrValidation)
		}
		seenDomains[domain] = true
	}
	seenIDs := map[uuid.UUID]bool{}
	for _, id := range input.Scope.EntityIDs {
		if id == uuid.Nil || seenIDs[id] {
			return fmt.Errorf("%w: invalid or duplicate agent scope entity", ErrValidation)
		}
		seenIDs[id] = true
	}
	if input.Scope.From != nil && input.Scope.To != nil && input.Scope.To.Before(*input.Scope.From) {
		return fmt.Errorf("%w: invalid agent scope time range", ErrValidation)
	}
	encoded, err := json.Marshal(input.Scope)
	if err != nil || len(encoded) > maxAgentScopeBytes {
		return fmt.Errorf("%w: agent scope is too large", ErrValidation)
	}
	return nil
}

type agentPatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

func validateAgentChange(change model.AgentChangeDraft) error {
	change.Reason = strings.TrimSpace(change.Reason)
	if utf8.RuneCountInString(change.Reason) < 1 || utf8.RuneCountInString(change.Reason) > maxAgentReasonRunes {
		return fmt.Errorf("%w: agent change reason must contain 1 to %d characters", ErrValidation, maxAgentReasonRunes)
	}
	configuration, ok := agentChangePolicies[change.ChangeType]
	if !ok || configuration.targetType != change.TargetType {
		return fmt.Errorf("%w: unsupported agent change type or target", ErrValidation)
	}
	if configuration.create {
		if change.TargetID != nil || change.BaseVersion != nil {
			return fmt.Errorf("%w: create change cannot identify an existing target", ErrValidation)
		}
	} else if change.TargetID == nil || *change.TargetID == uuid.Nil || change.BaseVersion == nil || *change.BaseVersion < 1 {
		return fmt.Errorf("%w: existing target and base version are required", ErrValidation)
	}
	if len(change.Patch) < 2 || len(change.Patch) > maxAgentPatchBytes {
		return fmt.Errorf("%w: invalid agent patch size", ErrValidation)
	}
	var operations []agentPatchOperation
	if err := decodeStrictJSON(change.Patch, &operations); err != nil || len(operations) < 1 || len(operations) > 32 {
		return fmt.Errorf("%w: agent patch must be a bounded JSON Patch array", ErrValidation)
	}
	seenPaths := map[string]bool{}
	for _, operation := range operations {
		allowedOperations, allowed := configuration.paths[operation.Path]
		if !allowed || !allowedOperations[operation.Op] || seenPaths[operation.Path] {
			return fmt.Errorf("%w: agent patch operation or path is not allowed", ErrValidation)
		}
		seenPaths[operation.Path] = true
		if operation.Op != "remove" {
			if len(operation.Value) == 0 || string(operation.Value) == "null" && !configuration.nullable[operation.Path] {
				return fmt.Errorf("%w: agent patch value is required", ErrValidation)
			}
			if err := validateAgentPatchValue(operation.Path, operation.Value); err != nil {
				return err
			}
		}
	}
	if err := validateOptionalJSONObject(change.PreviewBefore, maxAuditSnapshotBytes); err != nil {
		return err
	}
	if err := validateOptionalJSONObject(change.PreviewAfter, maxAuditSnapshotBytes); err != nil {
		return err
	}
	return nil
}

type agentChangePolicy struct {
	targetType string
	create     bool
	paths      map[string]map[string]bool
	nullable   map[string]bool
}

func patchOps(operations ...string) map[string]bool {
	result := make(map[string]bool, len(operations))
	for _, operation := range operations {
		result[operation] = true
	}
	return result
}

var agentChangePolicies = map[string]agentChangePolicy{
	"reschedule-task": {
		targetType: "task",
		paths: map[string]map[string]bool{
			"/scheduledStart": patchOps("add", "replace", "remove"),
			"/scheduledEnd":   patchOps("add", "replace", "remove"),
		},
		nullable: map[string]bool{"/scheduledStart": true, "/scheduledEnd": true},
	},
	"create-task": {
		targetType: "task", create: true,
		paths: map[string]map[string]bool{
			"/title": patchOps("add"), "/status": patchOps("add"), "/priority": patchOps("add"),
			"/estimateMinutes": patchOps("add"), "/dueAt": patchOps("add"),
			"/scheduledStart": patchOps("add"), "/scheduledEnd": patchOps("add"),
			"/goalId": patchOps("add"), "/sourceRecordId": patchOps("add"),
		},
		nullable: map[string]bool{"/dueAt": true, "/scheduledStart": true, "/scheduledEnd": true, "/goalId": true, "/sourceRecordId": true},
	},
	"create-event": {
		targetType: "calendar_event", create: true,
		paths: map[string]map[string]bool{
			"/title": patchOps("add"), "/startAt": patchOps("add"), "/endAt": patchOps("add"),
			"/timezone": patchOps("add"), "/location": patchOps("add"), "/kind": patchOps("add"),
			"/sourceCalendar": patchOps("add"), "/goalId": patchOps("add"),
		},
		nullable: map[string]bool{"/location": true, "/sourceCalendar": true, "/goalId": true},
	},
	"archive-record": {
		targetType: "record",
		paths:      map[string]map[string]bool{"/archivedAt": patchOps("add", "replace")},
		nullable:   map[string]bool{},
	},
	"link-note": {
		targetType: "note",
		paths:      map[string]map[string]bool{"/linkedEntityIds/-": patchOps("add")},
		nullable:   map[string]bool{},
	},
}

func validateAgentPatchValue(path string, raw json.RawMessage) error {
	switch path {
	case "/scheduledStart", "/scheduledEnd", "/dueAt", "/startAt", "/endAt", "/archivedAt":
		if string(raw) == "null" {
			return nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%w: agent patch time must be a string", ErrValidation)
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("%w: agent patch time must use RFC3339", ErrValidation)
		}
	case "/goalId", "/sourceRecordId", "/linkedEntityIds/-":
		if string(raw) == "null" {
			return nil
		}
		var value uuid.UUID
		if err := json.Unmarshal(raw, &value); err != nil || value == uuid.Nil {
			return fmt.Errorf("%w: agent patch ID must be a UUID", ErrValidation)
		}
	case "/estimateMinutes":
		var value int
		if err := json.Unmarshal(raw, &value); err != nil || value < 0 || value > 525600 {
			return fmt.Errorf("%w: invalid agent task estimate", ErrValidation)
		}
	case "/title", "/status", "/priority", "/timezone", "/location", "/kind", "/sourceCalendar":
		if string(raw) == "null" {
			return nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || utf8.RuneCountInString(strings.TrimSpace(value)) > 240 {
			return fmt.Errorf("%w: invalid agent patch text", ErrValidation)
		}
		if path == "/title" && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: agent patch title is required", ErrValidation)
		}
		if path == "/status" && !validTaskStatus(value) {
			return fmt.Errorf("%w: invalid agent task status", ErrValidation)
		}
		if path == "/priority" && value != "normal" && value != "important" {
			return fmt.Errorf("%w: invalid agent task priority", ErrValidation)
		}
		if path == "/kind" && !map[string]bool{"fixed": true, "focus": true, "health": true, "personal": true}[value] {
			return fmt.Errorf("%w: invalid agent event kind", ErrValidation)
		}
		if path == "/timezone" {
			if _, err := time.LoadLocation(value); err != nil {
				return fmt.Errorf("%w: invalid agent event timezone", ErrValidation)
			}
		}
	}
	return nil
}

func validateOptionalJSONObject(raw json.RawMessage, limit int) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if len(raw) > limit {
		return fmt.Errorf("%w: agent preview is too large", ErrValidation)
	}
	var object map[string]any
	if err := decodeStrictJSON(raw, &object); err != nil || object == nil {
		return fmt.Errorf("%w: agent preview must be a JSON object", ErrValidation)
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}
