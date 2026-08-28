package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type stubAuditApplication struct{ event model.AuditEvent }

func (application *stubAuditApplication) Get(context.Context, uuid.UUID, uuid.UUID) (model.AuditEvent, error) {
	return application.event, nil
}

func (application *stubAuditApplication) List(context.Context, uuid.UUID, string, int) (service.AuditPage, error) {
	return service.AuditPage{Events: []model.AuditEvent{application.event}}, nil
}

type stubUndoApplication struct {
	auditID  uuid.UUID
	expected int64
	result   model.UndoResult
}

func (application *stubUndoApplication) Undo(_ context.Context, _ service.MutationContext, auditID uuid.UUID, expected int64) (model.UndoResult, error) {
	application.auditID, application.expected = auditID, expected
	return application.result, nil
}

func TestAuditRoutesListOwnEventsAndRunVersionedUndo(t *testing.T) {
	userID, deviceID, auditID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
	audits := &stubAuditApplication{event: model.AuditEvent{ID: auditID, Action: "agent.change.apply", Undoable: true, Entities: []model.AuditEntity{{EntityType: "task", EntityID: taskID}}}}
	undos := &stubUndoApplication{result: model.UndoResult{OriginalAuditID: auditID, EntityType: "task", EntityID: taskID, EntityOperation: "update", EntityVersion: 3}}
	handler, err := NewRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: sessions, Audits: audits, Undos: undos,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://dayorder.example/api/v1/audit-events", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"entityType":"task"`) || strings.Contains(response.Body.String(), `"EntityType"`) {
		t.Fatalf("audit entities must use camelCase JSON fields: body=%s", response.Body.String())
	}
	var page struct {
		Events []struct {
			Entities []struct {
				EntityType string    `json:"entityType"`
				EntityID   uuid.UUID `json:"entityId"`
			} `json:"entities"`
		} `json:"events"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Events) != 1 || len(page.Events[0].Entities) != 1 || page.Events[0].Entities[0].EntityType != "task" || page.Events[0].Entities[0].EntityID != taskID {
		t.Fatalf("audit entity JSON contract mismatch: err=%v body=%s", err, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/audit-events/"+auditID.String()+"/undo", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
	request.Header.Set("X-Device-ID", deviceID.String())
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` || undos.auditID != auditID || undos.expected != 2 {
		t.Fatalf("undo status=%d etag=%q audit=%s expected=%d body=%s", response.Code, response.Header().Get("ETag"), undos.auditID, undos.expected, response.Body.String())
	}
}
