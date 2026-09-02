package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type stubAgentApplication struct {
	created       model.AgentRun
	startInput    service.StartAgentInput
	mutation      service.MutationContext
	accepted      model.AgentApplyResult
	acceptID      uuid.UUID
	acceptVersion int64
}

func (application *stubAgentApplication) Create(_ context.Context, mutation service.MutationContext, input service.StartAgentInput) (model.AgentRun, error) {
	application.mutation = mutation
	application.startInput = input
	return application.created, nil
}

func (application *stubAgentApplication) Get(context.Context, uuid.UUID, uuid.UUID) (model.AgentRun, error) {
	return application.created, nil
}

func (application *stubAgentApplication) List(context.Context, uuid.UUID, string, int) (service.AgentRunPage, error) {
	return service.AgentRunPage{Runs: []model.AgentRun{application.created}}, nil
}

func (application *stubAgentApplication) Accept(_ context.Context, mutation service.MutationContext, changeID uuid.UUID, expected int64) (model.AgentApplyResult, error) {
	application.mutation = mutation
	application.acceptID = changeID
	application.acceptVersion = expected
	return application.accepted, nil
}

func (*stubAgentApplication) Reject(context.Context, service.MutationContext, uuid.UUID, int64) (model.AgentApplyResult, error) {
	return model.AgentApplyResult{}, nil
}

func (*stubAgentApplication) Stop(context.Context, service.MutationContext, uuid.UUID, int64) (model.AgentRun, error) {
	return model.AgentRun{}, nil
}

func TestDisabledAgentRoutesReturnNotAvailable(t *testing.T) {
	handler, err := NewRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: &stubSessionApplication{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/agent-runs"},
		{http.MethodPost, "/api/v1/agent-runs"},
		{http.MethodGet, "/api/v1/agent-runs/11111111-1111-4111-8111-111111111111"},
		{http.MethodPost, "/api/v1/agent-runs/11111111-1111-4111-8111-111111111111/stop"},
		{http.MethodPost, "/api/v1/agent-changes/11111111-1111-4111-8111-111111111111/accept"},
		{http.MethodPost, "/api/v1/agent-changes/11111111-1111-4111-8111-111111111111/reject"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, "http://dayorder.example"+route.path, nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			var envelope apiErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != "AGENT_NOT_AVAILABLE" || envelope.Error.Message != "Agent 功能暂未接入" || envelope.Error.Retryable {
				t.Fatalf("error = %#v", envelope.Error)
			}
		})
	}
}

func TestAgentRoutesCreateRunAndAcceptVersionedChange(t *testing.T) {
	userID, deviceID, runID, changeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	agents := &stubAgentApplication{
		created: model.AgentRun{ID: runID, Status: "ready", Version: 1},
		accepted: model.AgentApplyResult{
			Run:    model.AgentRun{ID: runID, Status: "completed", Version: 3},
			Change: model.AgentChange{ID: changeID, RunID: runID, Status: "applied", Version: 2},
		},
	}
	handler, err := NewRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: sessions, Agents: agents, AgentAvailable: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/agent-runs", bytes.NewBufferString(`{"intent":"安排本周任务","actionMode":"confirm","scope":{"domains":["goals","tasks"]}}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Device-ID", deviceID.String())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` || agents.startInput.Intent != "安排本周任务" || agents.mutation.UserID != userID {
		t.Fatalf("create status=%d etag=%q input=%#v mutation=%#v body=%s", response.Code, response.Header().Get("ETag"), agents.startInput, agents.mutation, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/agent-changes/"+changeID.String()+"/accept", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", deviceID.String())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` || agents.acceptID != changeID || agents.acceptVersion != 1 {
		t.Fatalf("accept status=%d etag=%q id=%s version=%d body=%s", response.Code, response.Header().Get("ETag"), agents.acceptID, agents.acceptVersion, response.Body.String())
	}
	var result model.AgentApplyResult
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Change.Status != "applied" {
		t.Fatalf("accept response=%#v error=%v", result, err)
	}
}
