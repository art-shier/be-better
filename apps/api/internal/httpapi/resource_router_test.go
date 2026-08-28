package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type stubGoalApplication struct {
	goal           model.Goal
	milestone      model.GoalMilestone
	createCalls    int
	updateInput    service.UpdateGoalInput
	updateVersion  int64
	milestoneInput service.UpdateMilestoneInput
	getErr         error
	updateErr      error
	deleteErr      error
	mutations      []service.MutationContext
}

type stubDeviceApplication struct {
	registration service.DeviceRegistration
	input        service.RegisterDeviceInput
	deviceID     uuid.UUID
}

type stubSyncApplication struct {
	deviceID uuid.UUID
	cursor   string
}

func (app *stubSyncApplication) Bootstrap(_ context.Context, _ uuid.UUID, deviceID uuid.UUID) (service.SyncBootstrap, error) {
	app.deviceID = deviceID
	return service.SyncBootstrap{Cursor: "bootstrap-cursor"}, nil
}

func (app *stubSyncApplication) DeviceChanges(_ context.Context, _ uuid.UUID, deviceID uuid.UUID, cursor string, _ int) (service.SyncPage, error) {
	app.deviceID = deviceID
	app.cursor = cursor
	return service.SyncPage{NextCursor: "next-cursor"}, nil
}

func (app *stubDeviceApplication) Register(_ context.Context, _ uuid.UUID, deviceID uuid.UUID, input service.RegisterDeviceInput) (service.DeviceRegistration, error) {
	app.deviceID = deviceID
	app.input = input
	app.registration.Device.ID = deviceID
	return app.registration, nil
}

func (app *stubDeviceApplication) List(context.Context, uuid.UUID) ([]model.UserDevice, error) {
	return []model.UserDevice{app.registration.Device}, nil
}

func (app *stubGoalApplication) Create(_ context.Context, mutation service.MutationContext, input service.CreateGoalInput) (model.Goal, error) {
	app.createCalls++
	app.mutations = append(app.mutations, mutation)
	if mutation.Duplicate != nil && input.Title == "Replay" {
		*mutation.Duplicate = true
		return model.Goal{ID: uuid.New(), Title: input.Title, Version: 1}, nil
	}
	app.goal = model.Goal{ID: uuid.New(), Title: input.Title, Area: input.Area, Version: 1}
	return app.goal, nil
}
func (app *stubGoalApplication) Get(context.Context, uuid.UUID, uuid.UUID) (model.Goal, error) {
	return app.goal, app.getErr
}
func (*stubGoalApplication) List(context.Context, uuid.UUID, string, int) (service.GoalPage, error) {
	return service.GoalPage{}, nil
}
func (app *stubGoalApplication) Update(_ context.Context, mutation service.MutationContext, _ uuid.UUID, version int64, input service.UpdateGoalInput) (model.Goal, error) {
	app.mutations = append(app.mutations, mutation)
	if app.updateErr != nil {
		return model.Goal{}, app.updateErr
	}
	app.updateInput = input
	app.updateVersion = version
	app.goal.Title = input.Title
	app.goal.Version = version + 1
	return app.goal, nil
}
func (app *stubGoalApplication) Delete(context.Context, service.MutationContext, uuid.UUID, int64) error {
	return app.deleteErr
}
func (*stubGoalApplication) CreateMilestone(context.Context, service.MutationContext, uuid.UUID, service.CreateMilestoneInput) (model.GoalMilestone, error) {
	return model.GoalMilestone{}, nil
}
func (*stubGoalApplication) ListMilestones(context.Context, uuid.UUID, uuid.UUID) ([]model.GoalMilestone, error) {
	return nil, nil
}
func (app *stubGoalApplication) GetMilestone(context.Context, uuid.UUID, uuid.UUID) (model.GoalMilestone, error) {
	return app.milestone, nil
}
func (app *stubGoalApplication) UpdateMilestone(_ context.Context, _ service.MutationContext, _ uuid.UUID, version int64, input service.UpdateMilestoneInput) (model.GoalMilestone, error) {
	app.milestoneInput = input
	app.milestone.Title = input.Title
	app.milestone.Version = version + 1
	return app.milestone, nil
}

func TestResourceRouterRequiresMergePatchMediaType(t *testing.T) {
	userID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	goals := &stubGoalApplication{goal: model.Goal{ID: uuid.New(), Title: "Old", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28", Status: "active", Health: "normal", Version: 3}}
	handler, err := NewRouter(RouterOptions{Accounts: &stubAccountApplication{}, Sessions: sessions, Goals: goals, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "http://dayorder.example/api/v1/goals/"+goals.goal.ID.String(), bytes.NewBufferString(`{"title":"New"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", uuid.NewString())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", `"3"`)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType || goals.updateVersion != 0 {
		t.Fatalf("wrong media type status=%d updateVersion=%d body=%s", response.Code, goals.updateVersion, response.Body.String())
	}
}

func TestMilestonePatchPreservesOmittedFields(t *testing.T) {
	userID := uuid.New()
	dueAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	completedAt := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	milestoneID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	goals := &stubGoalApplication{milestone: model.GoalMilestone{ID: milestoneID, GoalID: uuid.New(), Title: "Old", DueAt: &dueAt, CompletedAt: &completedAt, SortOrder: 7, Version: 2}}
	handler, err := NewRouter(RouterOptions{Accounts: &stubAccountApplication{}, Sessions: sessions, Goals: goals, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "http://dayorder.example/api/v1/milestones/"+milestoneID.String(), bytes.NewBufferString(`{"title":"New"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", uuid.NewString())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", `"2"`)
	request.Header.Set("Content-Type", "application/merge-patch+json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("milestone patch status=%d body=%s", response.Code, response.Body.String())
	}
	if goals.milestoneInput.Title != "New" || goals.milestoneInput.SortOrder != 7 ||
		goals.milestoneInput.DueAt == nil || !goals.milestoneInput.DueAt.Equal(dueAt) ||
		goals.milestoneInput.CompletedAt == nil || !goals.milestoneInput.CompletedAt.Equal(completedAt) {
		t.Fatalf("milestone patch input=%#v", goals.milestoneInput)
	}
}

func TestDeviceRegistrationBootstrapsMutationIdentityWithoutExistingDeviceHeaders(t *testing.T) {
	userID := uuid.New()
	deviceID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	devices := &stubDeviceApplication{registration: service.DeviceRegistration{Device: model.UserDevice{DeviceName: "Browser", Platform: "web"}, Created: true}}
	handler, err := NewRouter(RouterOptions{Accounts: &stubAccountApplication{}, Sessions: sessions, Devices: devices, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "http://dayorder.example/api/v1/users/me/devices/"+deviceID.String(), bytes.NewBufferString(`{"deviceName":"Chrome","platform":"web"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || devices.deviceID != deviceID || devices.input.DeviceName != "Chrome" {
		t.Fatalf("register status=%d device=%s input=%#v body=%s", response.Code, devices.deviceID, devices.input, response.Body.String())
	}
}

func TestSyncRoutesRequireRegisteredDeviceIdentity(t *testing.T) {
	userID := uuid.New()
	deviceID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	syncApp := &stubSyncApplication{}
	handler, err := NewRouter(RouterOptions{Accounts: &stubAccountApplication{}, Sessions: sessions, Sync: syncApp, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://dayorder.example/api/v1/sync/bootstrap", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing device status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://dayorder.example/api/v1/sync/bootstrap", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", deviceID.String())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || syncApp.deviceID != deviceID || !strings.Contains(response.Body.String(), "bootstrap-cursor") {
		t.Fatalf("bootstrap status=%d device=%s body=%s", response.Code, syncApp.deviceID, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://dayorder.example/api/v1/sync/changes?cursor=bootstrap-cursor&limit=100", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", deviceID.String())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || syncApp.cursor != "bootstrap-cursor" || !strings.Contains(response.Body.String(), "next-cursor") {
		t.Fatalf("changes status=%d cursor=%q body=%s", response.Code, syncApp.cursor, response.Body.String())
	}
}

func TestSyncMutationsProcessesEachItemAndReportsDuplicateConflictAndRejection(t *testing.T) {
	userID := uuid.New()
	deviceID := uuid.New()
	createID := uuid.New()
	updateID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	goals := &stubGoalApplication{goal: model.Goal{ID: updateID, Title: "Current", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28", Status: "active", Health: "normal", Version: 3}}
	handler, err := NewRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: sessions, Goals: goals, Sync: &stubSyncApplication{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstMutationID := uuid.New()
	secondMutationID := uuid.New()
	thirdMutationID := uuid.New()
	payload := map[string]any{"mutations": []map[string]any{
		{"mutationId": firstMutationID, "sequence": 1, "entityType": "goal", "entityId": createID, "operation": "create", "baseVersion": 0, "payload": map[string]any{"title": "Replay"}},
		{"mutationId": secondMutationID, "sequence": 2, "entityType": "goal", "entityId": updateID, "operation": "update", "baseVersion": 2, "payload": map[string]any{"title": "Stale"}},
		{"mutationId": thirdMutationID, "sequence": 3, "entityType": "unknown", "entityId": uuid.New(), "operation": "create", "baseVersion": 0, "payload": map[string]any{}},
	}}
	body, _ := json.Marshal(payload)
	goals.updateErr = model.ErrConflict
	request := httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/sync/mutations", bytes.NewReader(body))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Device-ID", deviceID.String())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Results []struct {
			MutationID uuid.UUID       `json:"mutationId"`
			Status     string          `json:"status"`
			Data       json.RawMessage `json:"data"`
			Error      *apiErrorBody   `json:"error"`
		} `json:"results"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 3 || result.Results[0].Status != "duplicate" || result.Results[1].Status != "conflict" || result.Results[2].Status != "rejected" {
		t.Fatalf("mutation results=%#v", result.Results)
	}
	if result.Results[1].Error == nil || result.Results[1].Error.Code != "ENTITY_VERSION_CONFLICT" {
		t.Fatalf("conflict result=%#v", result.Results[1])
	}
	var current model.Goal
	if err = json.Unmarshal(result.Results[1].Data, &current); err != nil || current.ID != updateID || current.Title != "Current" || current.Version != 3 {
		t.Fatalf("conflict data=%s decoded=%#v error=%v", result.Results[1].Data, current, err)
	}
	if len(goals.mutations) != 2 || goals.mutations[0].DeviceID != deviceID || goals.mutations[0].MutationID != firstMutationID {
		t.Fatalf("mutation contexts=%#v", goals.mutations)
	}
}
func (*stubGoalApplication) DeleteMilestone(context.Context, service.MutationContext, uuid.UUID, int64) error {
	return nil
}

func TestResourceRouterRequiresMutationHeadersAndSupportsMergePatch(t *testing.T) {
	userID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	goals := &stubGoalApplication{goal: model.Goal{ID: uuid.New(), Title: "Old", Why: "why", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28", Status: "active", Health: "normal", Version: 3}}
	handler, err := NewRouter(RouterOptions{Accounts: &stubAccountApplication{}, Sessions: sessions, Goals: goals, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/goals", bytes.NewBufferString(`{"title":"Goal"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired || goals.createCalls != 0 {
		t.Fatalf("missing headers status=%d calls=%d", response.Code, goals.createCalls)
	}

	request = httptest.NewRequest(http.MethodPatch, "http://dayorder.example/api/v1/goals/"+goals.goal.ID.String(), bytes.NewBufferString(`{"title":"New"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", uuid.NewString())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", `"3"`)
	request.Header.Set("Content-Type", "application/merge-patch+json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` {
		t.Fatalf("patch status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if goals.updateInput.Title != "New" || goals.updateInput.Area != "Work" || goals.updateVersion != 3 {
		t.Fatalf("merge input=%#v version=%d", goals.updateInput, goals.updateVersion)
	}
}

func TestResourceRouterMapsCreateDeleteNotFoundAndVersionConflict(t *testing.T) {
	userID := uuid.New()
	deviceID := uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	goals := &stubGoalApplication{goal: model.Goal{ID: uuid.New(), Title: "Goal", Area: "Work", MetricType: "project", TargetValue: 1, StartDate: "2026-08-28", Status: "active", Health: "normal", Version: 1}}
	handler, err := NewRouter(RouterOptions{Accounts: &stubAccountApplication{}, Sessions: sessions, Goals: goals, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	resourceRequest := func(method, path, body string) *http.Request {
		request := httptest.NewRequest(method, "http://dayorder.example"+path, bytes.NewBufferString(body))
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
		request.Header.Set("X-Device-ID", deviceID.String())
		request.Header.Set("Idempotency-Key", uuid.NewString())
		return request
	}

	request := resourceRequest(http.MethodPost, "/api/v1/goals", `{"title":"Created"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	goals.getErr = model.ErrNotFound
	request = httptest.NewRequest(http.MethodGet, "http://dayorder.example/api/v1/goals/"+goals.goal.ID.String(), nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", response.Code, response.Body.String())
	}
	goals.getErr = nil

	goals.updateErr = model.ErrConflict
	request = resourceRequest(http.MethodPatch, "/api/v1/goals/"+goals.goal.ID.String(), `{"title":"Stale"}`)
	request.Header.Set("If-Match", `"1"`)
	request.Header.Set("Content-Type", "application/merge-patch+json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
	goals.updateErr = nil

	request = resourceRequest(http.MethodDelete, "/api/v1/goals/"+goals.goal.ID.String(), "")
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
}
