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
	"time"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSyncMutationsUseIndependentPostgresTransactionsAndEnforceDeviceAndRLS(t *testing.T) {
	fixture := testdb.StartForTest(t)
	if err := dbmigrations.Up(fixture.MigrationURL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	apiPool, err := database.Open(ctx, syncIntegrationDatabaseConfig(fixture.APIURL))
	if err != nil {
		t.Fatal(err)
	}
	defer apiPool.Close()
	migrationPool, err := pgxpool.New(ctx, fixture.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()

	userA, userB := uuid.New(), uuid.New()
	deviceA, deviceB := uuid.New(), uuid.New()
	for index, identity := range []struct {
		userID   uuid.UUID
		deviceID uuid.UUID
	}{{userA, deviceA}, {userB, deviceB}} {
		email := "sync-http-" + identity.userID.String() + "@example.com"
		if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status, email_verified_at)
VALUES ($1, $2, $2, $3, 'unused', 'active', now());
INSERT INTO dayorder.user_settings (user_id) VALUES ($1);
INSERT INTO dayorder.user_devices (id, user_id, device_name, platform)
VALUES ($4, $1, 'integration-test', 'web');
`, identity.userID, email, "Sync User "+string(rune('A'+index)), identity.deviceID); err != nil {
			t.Fatal(err)
		}
	}

	transactor, err := database.NewPoolTransactor(apiPool)
	if err != nil {
		t.Fatal(err)
	}
	idempotency, _ := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	syncService, _ := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, []byte("0123456789abcdef0123456789abcdef"))
	auditService, _ := service.NewAuditService(postgresstore.NewAuditRepository())
	commands, _ := service.NewCommandService(transactor, idempotency, syncService, auditService, postgresstore.NewOutboxWriter())
	cursors, _ := service.NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	goals, _ := service.NewGoalService(postgresstore.NewGoalRepository(), transactor, commands, cursors)
	sessions := &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userA, Status: model.AccountActive}}}
	handler, err := NewRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: sessions, Goals: goals, Sync: syncService,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	firstGoalID, rejectedGoalID, thirdGoalID := uuid.New(), uuid.New(), uuid.New()
	firstMutationID, rejectedMutationID, thirdMutationID := uuid.New(), uuid.New(), uuid.New()
	initial := []map[string]any{
		syncGoalMutation(firstMutationID, 1, firstGoalID, "create", 0, validGoalPayload("First")),
		syncGoalMutation(rejectedMutationID, 2, rejectedGoalID, "create", 0, validGoalPayload("")),
		syncGoalMutation(thirdMutationID, 3, thirdGoalID, "create", 0, validGoalPayload("Third")),
	}
	result := postIntegrationMutations(t, handler, deviceA, initial)
	if len(result.Results) != 3 || result.Results[0].Status != "applied" || result.Results[1].Status != "rejected" || result.Results[2].Status != "applied" {
		t.Fatalf("initial results=%#v", result.Results)
	}
	assertStoredGoalIDs(t, ctx, migrationPool, userA, []uuid.UUID{firstGoalID, thirdGoalID})
	var rejectedCount int
	if err = migrationPool.QueryRow(ctx, "SELECT count(*) FROM dayorder.goals WHERE id = $1", rejectedGoalID).Scan(&rejectedCount); err != nil || rejectedCount != 0 {
		t.Fatalf("rejected goal count=%d error=%v", rejectedCount, err)
	}

	replay := postIntegrationMutations(t, handler, deviceA, initial)
	if replay.Results[0].Status != "duplicate" || replay.Results[1].Status != "rejected" || replay.Results[2].Status != "duplicate" {
		t.Fatalf("replay results=%#v", replay.Results)
	}

	updatedPayload := validGoalPayload("Updated")
	conflicts := postIntegrationMutations(t, handler, deviceA, []map[string]any{
		syncGoalMutation(uuid.New(), 1, firstGoalID, "update", 1, updatedPayload),
		syncGoalMutation(uuid.New(), 2, firstGoalID, "update", 1, validGoalPayload("Stale")),
	})
	if conflicts.Results[0].Status != "applied" || conflicts.Results[1].Status != "conflict" || conflicts.Results[1].Error == nil || conflicts.Results[1].Error.Code != "ENTITY_VERSION_CONFLICT" {
		t.Fatalf("conflict results=%#v", conflicts.Results)
	}
	var current model.Goal
	if err = json.Unmarshal(conflicts.Results[1].Data, &current); err != nil || current.ID != firstGoalID || current.Title != "Updated" || current.Version != 2 {
		t.Fatalf("current conflict data=%s decoded=%#v error=%v", conflicts.Results[1].Data, current, err)
	}

	sessions.authenticated.Account.ID = userB
	crossUser := postIntegrationMutations(t, handler, deviceB, []map[string]any{
		syncGoalMutation(uuid.New(), 1, firstGoalID, "update", 2, validGoalPayload("Cross user")),
	})
	if crossUser.Results[0].Status != "conflict" || crossUser.Results[0].Error == nil || crossUser.Results[0].Error.Code != "ENTITY_DELETED" || len(crossUser.Results[0].Data) != 0 {
		t.Fatalf("cross-user result=%#v", crossUser.Results[0])
	}

	sessions.authenticated.Account.ID = userA
	if _, err = migrationPool.Exec(ctx, "UPDATE dayorder.user_devices SET revoked_at = now() WHERE user_id = $1 AND id = $2", userA, deviceA); err != nil {
		t.Fatal(err)
	}
	revoked := postIntegrationMutations(t, handler, deviceA, []map[string]any{
		syncGoalMutation(uuid.New(), 1, uuid.New(), "create", 0, validGoalPayload("Revoked device")),
	})
	if revoked.Results[0].Status != "rejected" || revoked.Results[0].Error == nil || revoked.Results[0].Error.Code != "DEVICE_REGISTRATION_REQUIRED" {
		t.Fatalf("revoked-device result=%#v", revoked.Results[0])
	}
}

type integrationMutationResponse struct {
	Results []struct {
		MutationID uuid.UUID       `json:"mutationId"`
		Status     string          `json:"status"`
		Data       json.RawMessage `json:"data"`
		Error      *apiErrorBody   `json:"error"`
	} `json:"results"`
}

func postIntegrationMutations(t testing.TB, handler http.Handler, deviceID uuid.UUID, mutations []map[string]any) integrationMutationResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"mutations": mutations})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://dayorder.example/api/v1/sync/mutations", bytes.NewReader(body))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "integration-session"})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Device-ID", deviceID.String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("sync mutations status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded integrationMutationResponse
	if err = json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func syncGoalMutation(mutationID uuid.UUID, sequence int64, entityID uuid.UUID, operation string, baseVersion int64, payload map[string]any) map[string]any {
	return map[string]any{
		"mutationId": mutationID, "sequence": sequence, "entityType": "goal", "entityId": entityID,
		"operation": operation, "baseVersion": baseVersion, "payload": payload,
	}
}

func validGoalPayload(title string) map[string]any {
	return map[string]any{
		"title": title, "area": "Work", "metricType": "project", "targetValue": 1,
		"currentValue": 0, "unit": "", "startDate": "2026-08-28", "status": "active", "health": "normal",
	}
}

func assertStoredGoalIDs(t testing.TB, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, want []uuid.UUID) {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT id FROM dayorder.goals WHERE user_id = $1 AND deleted_at IS NULL ORDER BY id", userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[uuid.UUID]bool)
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got[id] = true
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("stored goals=%v want=%v", got, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("stored goals=%v missing=%s", got, id)
		}
	}
}

func syncIntegrationDatabaseConfig(databaseURL string) config.DatabaseConfig {
	return config.DatabaseConfig{
		URL: databaseURL, MaxConns: 4, MinConns: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute,
		StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second,
		IdleTransactionTimeout: 10 * time.Second, HealthTimeout: 10 * time.Second,
	}
}
