package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeAgentProcessingStore struct {
	snapshot          model.AgentSnapshot
	transitioned      bool
	completedPlan     model.AgentPlan
	completedProvider string
	completedModel    string
	completed         model.AgentRun
	completeCalls     int
	failed            model.AgentRun
}

func (store *fakeAgentProcessingStore) PrepareAnalysis(context.Context, database.Tx, uuid.UUID, uuid.UUID, time.Time) (model.AgentSnapshot, bool, error) {
	return store.snapshot, store.transitioned, nil
}

func (store *fakeAgentProcessingStore) CompleteAnalysis(_ context.Context, _ database.Tx, _ uuid.UUID, _ uuid.UUID, _ int64, plan model.AgentPlan, provider, providerModel string, _ time.Time) (model.AgentRun, error) {
	store.completeCalls++
	store.completedPlan = plan
	store.completedProvider = provider
	store.completedModel = providerModel
	return store.completed, nil
}

func (store *fakeAgentProcessingStore) FailAnalysis(context.Context, database.Tx, uuid.UUID, uuid.UUID, string, string, time.Time) (model.AgentRun, error) {
	return store.failed, nil
}

type stubAgentProvider struct {
	plan  model.AgentPlan
	err   error
	calls int
}

func (*stubAgentProvider) Name() string  { return "stub" }
func (*stubAgentProvider) Model() string { return "stub-v1" }
func (provider *stubAgentProvider) Analyze(context.Context, model.AgentSnapshot) (model.AgentPlan, error) {
	provider.calls++
	return provider.plan, provider.err
}

func TestAgentProcessorPersistsValidatedPlanAndSyncsRunAndChanges(t *testing.T) {
	runID, taskID := uuid.New(), uuid.New()
	base := int64(3)
	store := &fakeAgentProcessingStore{
		transitioned: true,
		snapshot: model.AgentSnapshot{
			Run:   model.AgentRun{ID: runID, Status: "analyzing", ActionMode: "confirm", Version: 2},
			Tasks: []model.Task{{ID: taskID, Title: "Ship", Status: "todo", Version: base}},
		},
		completed: model.AgentRun{
			ID: runID, Status: "waiting", Version: 3,
			Changes: []model.AgentChange{{ID: uuid.New(), RunID: runID, TargetType: "task", Status: "pending", Version: 1}},
		},
	}
	provider := &stubAgentProvider{plan: model.AgentPlan{
		Summary: "生成 1 项待确认调整。",
		Steps:   []model.AgentStepDraft{{Title: "检查任务", Detail: "读取授权任务", Metadata: json.RawMessage(`{}`)}},
		Changes: []model.AgentChangeDraft{{
			ChangeType: "reschedule-task", TargetType: "task", TargetID: &taskID, BaseVersion: &base,
			Patch:  json.RawMessage(`[{"op":"replace","path":"/scheduledStart","value":"2026-08-29T09:00:00Z"}]`),
			Reason: "优先推进当前任务",
		}},
		SourceRefs: []model.AgentSourceRefDraft{{EntityType: "task", EntityID: taskID, EntityVersion: base, LabelSnapshot: "Ship"}},
	}}
	syncWriter := &recordingSyncWriter{}
	auditWriter := &recordingAuditWriter{}
	processor, err := NewAgentProcessor(store, immediateUserTransactor{tx: &testTransaction{}}, syncWriter, auditWriter, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err = processor.Process(context.Background(), uuid.New(), runID); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || store.completeCalls != 1 || store.completedProvider != "stub" || store.completedModel != "stub-v1" {
		t.Fatalf("provider calls=%d complete=%d provider=%q model=%q", provider.calls, store.completeCalls, store.completedProvider, store.completedModel)
	}
	if len(syncWriter.changes) != 3 || syncWriter.changes[0].EntityType != "agent_run" || syncWriter.changes[1].EntityType != "agent_run" || syncWriter.changes[2].EntityType != "agent_change" {
		t.Fatalf("sync changes = %#v", syncWriter.changes)
	}
	if len(auditWriter.audits) != 2 || auditWriter.audits[1].Action != "agent.run.analyze" {
		t.Fatalf("audits = %#v", auditWriter.audits)
	}
}

func TestAgentProcessorRejectsProviderPatchOutsideScopeAndWhitelist(t *testing.T) {
	runID, taskID, foreignTaskID := uuid.New(), uuid.New(), uuid.New()
	base := int64(2)
	store := &fakeAgentProcessingStore{snapshot: model.AgentSnapshot{
		Run:   model.AgentRun{ID: runID, Status: "analyzing", ActionMode: "confirm", Version: 2},
		Tasks: []model.Task{{ID: taskID, Title: "Allowed", Version: base}},
	}}
	provider := &stubAgentProvider{plan: model.AgentPlan{
		Summary: "bad", Steps: []model.AgentStepDraft{{Title: "bad", Metadata: json.RawMessage(`{}`)}},
		Changes: []model.AgentChangeDraft{{
			ChangeType: "reschedule-task", TargetType: "task", TargetID: &foreignTaskID, BaseVersion: &base,
			Patch: json.RawMessage(`[{"op":"replace","path":"/title","value":"越权"}]`), Reason: "bad",
		}},
	}}
	processor, _ := NewAgentProcessor(store, immediateUserTransactor{tx: &testTransaction{}}, &recordingSyncWriter{}, &recordingAuditWriter{}, provider)
	err := processor.Process(context.Background(), uuid.New(), runID)
	if !errors.Is(err, ErrValidation) || store.completeCalls != 0 {
		t.Fatalf("Process() error=%v completeCalls=%d", err, store.completeCalls)
	}
}

func TestDeterministicAgentProviderBuildsVersionBoundTaskChange(t *testing.T) {
	provider := NewDeterministicAgentProvider(func() time.Time {
		return time.Date(2026, 8, 28, 8, 15, 0, 0, time.UTC)
	})
	taskID := uuid.New()
	plan, err := provider.Analyze(context.Background(), model.AgentSnapshot{
		Run:   model.AgentRun{Intent: "安排今天任务", ActionMode: "confirm"},
		Tasks: []model.Task{{ID: taskID, Title: "关键任务", Status: "todo", Priority: "important", Version: 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].TargetID == nil || *plan.Changes[0].TargetID != taskID || plan.Changes[0].BaseVersion == nil || *plan.Changes[0].BaseVersion != 5 {
		t.Fatalf("deterministic plan = %#v", plan)
	}
}
