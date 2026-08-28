package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type AgentProcessingStore interface {
	PrepareAnalysis(context.Context, database.Tx, uuid.UUID, uuid.UUID, time.Time) (model.AgentSnapshot, bool, error)
	CompleteAnalysis(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64, model.AgentPlan, string, string, time.Time) (model.AgentRun, error)
	FailAnalysis(context.Context, database.Tx, uuid.UUID, uuid.UUID, string, string, time.Time) (model.AgentRun, error)
}

type AgentProvider interface {
	Name() string
	Model() string
	Analyze(context.Context, model.AgentSnapshot) (model.AgentPlan, error)
}

type AgentProcessor struct {
	store       AgentProcessingStore
	transactor  UserTransactor
	syncWriter  CommandSyncWriter
	auditWriter CommandAuditWriter
	provider    AgentProvider
	now         func() time.Time
}

func NewAgentProcessor(store AgentProcessingStore, transactor UserTransactor, syncWriter CommandSyncWriter, auditWriter CommandAuditWriter, provider AgentProvider) (*AgentProcessor, error) {
	if store == nil || transactor == nil || syncWriter == nil || auditWriter == nil || provider == nil {
		return nil, errors.New("agent processor store, transactor, writers, and provider are required")
	}
	if strings.TrimSpace(provider.Name()) == "" || strings.TrimSpace(provider.Model()) == "" {
		return nil, errors.New("agent provider name and model are required")
	}
	return &AgentProcessor{
		store: store, transactor: transactor, syncWriter: syncWriter, auditWriter: auditWriter,
		provider: provider, now: time.Now,
	}, nil
}

func (processor *AgentProcessor) Process(ctx context.Context, userID, runID uuid.UUID) error {
	if processor == nil || processor.store == nil || userID == uuid.Nil || runID == uuid.Nil {
		return fmt.Errorf("%w: agent processor user and run IDs are required", ErrValidation)
	}
	var snapshot model.AgentSnapshot
	var transitioned bool
	err := processor.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var prepareErr error
		snapshot, transitioned, prepareErr = processor.store.PrepareAnalysis(ctx, tx, userID, runID, processor.now().UTC())
		if prepareErr != nil {
			return prepareErr
		}
		if !transitioned {
			return nil
		}
		if err := processor.syncWriter.Record(ctx, tx, userID, []model.SyncChangeDraft{{
			EntityType: "agent_run", EntityID: snapshot.Run.ID, Operation: "update", EntityVersion: snapshot.Run.Version,
		}}); err != nil {
			return err
		}
		return processor.auditWriter.Record(ctx, tx, userID, []model.AuditDraft{{
			ActorType: "system", Action: "agent.run.start", AfterData: resourceJSON(snapshot.Run),
			Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: snapshot.Run.ID}},
		}})
	})
	if err != nil {
		return fmt.Errorf("prepare agent analysis: %w", err)
	}
	if snapshot.Run.Status != "analyzing" {
		return nil
	}

	plan, err := processor.provider.Analyze(ctx, snapshot)
	if err != nil {
		return fmt.Errorf("agent provider analysis failed: %w", err)
	}
	if err = validateAgentPlan(snapshot, plan); err != nil {
		return err
	}
	err = processor.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		completed, completeErr := processor.store.CompleteAnalysis(
			ctx, tx, userID, runID, snapshot.Run.Version, plan,
			processor.provider.Name(), processor.provider.Model(), processor.now().UTC(),
		)
		if completeErr != nil {
			return completeErr
		}
		changes := []model.SyncChangeDraft{{
			EntityType: "agent_run", EntityID: completed.ID, Operation: "update", EntityVersion: completed.Version,
		}}
		for _, change := range completed.Changes {
			changes = append(changes, model.SyncChangeDraft{
				EntityType: "agent_change", EntityID: change.ID, Operation: "create", EntityVersion: change.Version,
			})
		}
		if syncErr := processor.syncWriter.Record(ctx, tx, userID, changes); syncErr != nil {
			return syncErr
		}
		actorID := completed.ID
		return processor.auditWriter.Record(ctx, tx, userID, []model.AuditDraft{{
			ActorType: "agent", ActorID: &actorID, Action: "agent.run.analyze",
			AfterData: resourceJSON(map[string]any{
				"id": completed.ID, "status": completed.Status, "summary": completed.Summary,
				"changeCount": len(completed.Changes), "sourceCount": len(completed.SourceRefs),
			}),
			Metadata: resourceJSON(map[string]any{"provider": processor.provider.Name(), "model": processor.provider.Model()}),
			Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: completed.ID}},
		}})
	})
	if err != nil {
		return fmt.Errorf("complete agent analysis: %w", err)
	}
	return nil
}

func (processor *AgentProcessor) Fail(ctx context.Context, userID, runID uuid.UUID, code, message string) error {
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	if processor == nil || userID == uuid.Nil || runID == uuid.Nil || code == "" || message == "" {
		return fmt.Errorf("%w: complete agent failure data is required", ErrValidation)
	}
	if utf8.RuneCountInString(code) > 80 {
		code = string([]rune(code)[:80])
	}
	if utf8.RuneCountInString(message) > 1000 {
		message = string([]rune(message)[:1000])
	}
	return processor.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		run, err := processor.store.FailAnalysis(ctx, tx, userID, runID, code, message, processor.now().UTC())
		if err != nil {
			return err
		}
		if err = processor.syncWriter.Record(ctx, tx, userID, []model.SyncChangeDraft{{
			EntityType: "agent_run", EntityID: run.ID, Operation: "update", EntityVersion: run.Version,
		}}); err != nil {
			return err
		}
		return processor.auditWriter.Record(ctx, tx, userID, []model.AuditDraft{{
			ActorType: "system", Action: "agent.run.fail", AfterData: resourceJSON(run),
			Entities: []model.AuditEntity{{EntityType: "agent_run", EntityID: run.ID}},
		}})
	})
}

type agentSnapshotEntity struct {
	entityType string
	version    int64
}

func validateAgentPlan(snapshot model.AgentSnapshot, plan model.AgentPlan) error {
	plan.Summary = strings.TrimSpace(plan.Summary)
	if utf8.RuneCountInString(plan.Summary) < 1 || utf8.RuneCountInString(plan.Summary) > 4000 {
		return fmt.Errorf("%w: invalid agent plan summary", ErrValidation)
	}
	if len(plan.Steps) < 1 || len(plan.Steps) > 20 || len(plan.Changes) > 50 || len(plan.SourceRefs) > 200 {
		return fmt.Errorf("%w: invalid agent plan size", ErrValidation)
	}
	if snapshot.Run.ActionMode == "read" && len(plan.Changes) != 0 {
		return fmt.Errorf("%w: read-only agent run cannot propose changes", ErrValidation)
	}
	for _, step := range plan.Steps {
		if utf8.RuneCountInString(strings.TrimSpace(step.Title)) < 1 || utf8.RuneCountInString(strings.TrimSpace(step.Title)) > 240 || utf8.RuneCountInString(step.Detail) > 4000 {
			return fmt.Errorf("%w: invalid agent step", ErrValidation)
		}
		if len(step.Metadata) == 0 {
			step.Metadata = json.RawMessage(`{}`)
		}
		if err := validateOptionalJSONObject(step.Metadata, maxAuditMetadataBytes); err != nil {
			return err
		}
	}
	entities := agentSnapshotEntities(snapshot)
	for _, change := range plan.Changes {
		if err := validateAgentChange(change); err != nil {
			return err
		}
		if change.TargetID != nil {
			entity, exists := entities[*change.TargetID]
			if !exists || entity.entityType != change.TargetType || change.BaseVersion == nil || entity.version != *change.BaseVersion {
				return fmt.Errorf("%w: agent change target is outside the authorized snapshot", ErrValidation)
			}
		}
		if err := validatePatchReferences(change.Patch, entities); err != nil {
			return err
		}
	}
	seenRefs := map[string]bool{}
	for _, ref := range plan.SourceRefs {
		entity, exists := entities[ref.EntityID]
		key := ref.EntityType + ":" + ref.EntityID.String() + ":" + fmt.Sprint(ref.EntityVersion)
		if !exists || entity.entityType != ref.EntityType || entity.version != ref.EntityVersion || seenRefs[key] || utf8.RuneCountInString(strings.TrimSpace(ref.LabelSnapshot)) < 1 || utf8.RuneCountInString(strings.TrimSpace(ref.LabelSnapshot)) > 240 {
			return fmt.Errorf("%w: invalid or unauthorized agent source reference", ErrValidation)
		}
		seenRefs[key] = true
	}
	return nil
}

func validatePatchReferences(raw json.RawMessage, entities map[uuid.UUID]agentSnapshotEntity) error {
	var operations []agentPatchOperation
	if err := json.Unmarshal(raw, &operations); err != nil {
		return fmt.Errorf("%w: invalid agent patch", ErrValidation)
	}
	for _, operation := range operations {
		if operation.Path != "/goalId" && operation.Path != "/sourceRecordId" && operation.Path != "/linkedEntityIds/-" || string(operation.Value) == "null" {
			continue
		}
		var identifier uuid.UUID
		if err := json.Unmarshal(operation.Value, &identifier); err != nil {
			return fmt.Errorf("%w: invalid agent patch reference", ErrValidation)
		}
		entity, exists := entities[identifier]
		allowedType := map[string]string{"/goalId": "goal", "/sourceRecordId": "record"}[operation.Path]
		if !exists || allowedType != "" && entity.entityType != allowedType {
			return fmt.Errorf("%w: agent patch reference is outside the authorized snapshot", ErrValidation)
		}
	}
	return nil
}

func agentSnapshotEntities(snapshot model.AgentSnapshot) map[uuid.UUID]agentSnapshotEntity {
	entities := make(map[uuid.UUID]agentSnapshotEntity, len(snapshot.Goals)+len(snapshot.Tasks)+len(snapshot.Events)+len(snapshot.Records)+len(snapshot.Notes))
	for _, value := range snapshot.Goals {
		entities[value.ID] = agentSnapshotEntity{entityType: "goal", version: value.Version}
	}
	for _, value := range snapshot.Tasks {
		entities[value.ID] = agentSnapshotEntity{entityType: "task", version: value.Version}
	}
	for _, value := range snapshot.Events {
		entities[value.ID] = agentSnapshotEntity{entityType: "calendar_event", version: value.Version}
	}
	for _, value := range snapshot.Records {
		entities[value.ID] = agentSnapshotEntity{entityType: "record", version: value.Version}
	}
	for _, value := range snapshot.Notes {
		entities[value.ID] = agentSnapshotEntity{entityType: "note", version: value.Version}
	}
	return entities
}

type DeterministicAgentProvider struct{ now func() time.Time }

func NewDeterministicAgentProvider(now func() time.Time) *DeterministicAgentProvider {
	if now == nil {
		now = time.Now
	}
	return &DeterministicAgentProvider{now: now}
}

func (*DeterministicAgentProvider) Name() string  { return "deterministic" }
func (*DeterministicAgentProvider) Model() string { return "rules-v1" }

func (provider *DeterministicAgentProvider) Analyze(_ context.Context, snapshot model.AgentSnapshot) (model.AgentPlan, error) {
	plan := model.AgentPlan{
		Summary: "已完成授权范围分析，没有需要执行的写入。",
		Steps: []model.AgentStepDraft{
			{Title: "读取授权范围", Detail: "仅载入本次委托明确授权的数据。", Metadata: json.RawMessage(`{"phase":"read"}`)},
			{Title: "分析优先级与时间", Detail: "检查未完成任务、目标关联和现有安排。", Metadata: json.RawMessage(`{"phase":"analyze"}`)},
			{Title: "生成受控建议", Detail: "只生成字段白名单内、带基础版本的变更。", Metadata: json.RawMessage(`{"phase":"propose"}`)},
		},
		Changes: []model.AgentChangeDraft{}, SourceRefs: []model.AgentSourceRefDraft{},
	}
	if snapshot.Run.ActionMode == "read" {
		plan.SourceRefs = deterministicSourceRefs(snapshot, 8)
		return plan, nil
	}
	openTasks := make([]model.Task, 0, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		if task.Status == "todo" || task.Status == "doing" {
			openTasks = append(openTasks, task)
		}
	}
	sort.SliceStable(openTasks, func(i, j int) bool {
		if openTasks[i].Priority != openTasks[j].Priority {
			return openTasks[i].Priority == "important"
		}
		return openTasks[i].UpdatedAt.After(openTasks[j].UpdatedAt)
	})
	if len(openTasks) > 0 {
		task := openTasks[0]
		start := provider.now().UTC().Truncate(time.Hour).Add(time.Hour)
		duration := task.EstimateMinutes
		if duration < 15 {
			duration = 45
		}
		end := start.Add(time.Duration(duration) * time.Minute)
		base := task.Version
		targetID := task.ID
		patch, _ := json.Marshal([]map[string]any{
			{"op": "replace", "path": "/scheduledStart", "value": start.Format(time.RFC3339)},
			{"op": "replace", "path": "/scheduledEnd", "value": end.Format(time.RFC3339)},
		})
		plan.Changes = []model.AgentChangeDraft{{
			ChangeType: "reschedule-task", TargetType: "task", TargetID: &targetID, BaseVersion: &base,
			Patch:         patch,
			PreviewBefore: resourceJSON(map[string]any{"scheduledStart": task.ScheduledStart, "scheduledEnd": task.ScheduledEnd}),
			PreviewAfter:  resourceJSON(map[string]any{"scheduledStart": start, "scheduledEnd": end}),
			Reason:        "该任务是当前授权范围内优先级最高的未完成事项。",
		}}
		plan.SourceRefs = []model.AgentSourceRefDraft{{EntityType: "task", EntityID: task.ID, EntityVersion: task.Version, LabelSnapshot: task.Title}}
		plan.Summary = "已生成 1 项带版本保护的任务时间建议，等待确认。"
		return plan, nil
	}
	if len(snapshot.Goals) > 0 {
		goal := snapshot.Goals[0]
		patch, _ := json.Marshal([]map[string]any{
			{"op": "add", "path": "/title", "value": "推进：" + goal.Title},
			{"op": "add", "path": "/status", "value": "todo"},
			{"op": "add", "path": "/priority", "value": "normal"},
			{"op": "add", "path": "/estimateMinutes", "value": 45},
			{"op": "add", "path": "/goalId", "value": goal.ID},
		})
		plan.Changes = []model.AgentChangeDraft{{
			ChangeType: "create-task", TargetType: "task", Patch: patch,
			PreviewAfter: resourceJSON(map[string]any{"title": "推进：" + goal.Title, "estimateMinutes": 45, "goalId": goal.ID}),
			Reason:       "当前目标没有可执行的未完成任务，先生成一个可确认的起步动作。",
		}}
		plan.SourceRefs = []model.AgentSourceRefDraft{{EntityType: "goal", EntityID: goal.ID, EntityVersion: goal.Version, LabelSnapshot: goal.Title}}
		plan.Summary = "已生成 1 项任务创建建议，等待确认。"
	}
	return plan, nil
}

func deterministicSourceRefs(snapshot model.AgentSnapshot, limit int) []model.AgentSourceRefDraft {
	refs := make([]model.AgentSourceRefDraft, 0, limit)
	appendRef := func(entityType string, id uuid.UUID, version int64, label string) {
		if len(refs) < limit {
			refs = append(refs, model.AgentSourceRefDraft{EntityType: entityType, EntityID: id, EntityVersion: version, LabelSnapshot: label})
		}
	}
	for _, goal := range snapshot.Goals {
		appendRef("goal", goal.ID, goal.Version, goal.Title)
	}
	for _, task := range snapshot.Tasks {
		appendRef("task", task.ID, task.Version, task.Title)
	}
	for _, event := range snapshot.Events {
		appendRef("calendar_event", event.ID, event.Version, event.Title)
	}
	for _, record := range snapshot.Records {
		appendRef("record", record.ID, record.Version, truncateAgentLabel(record.RawText))
	}
	for _, note := range snapshot.Notes {
		appendRef("note", note.ID, note.Version, note.Title)
	}
	return refs
}

func truncateAgentLabel(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:240])
	}
	if value == "" {
		return "未命名来源"
	}
	return value
}
