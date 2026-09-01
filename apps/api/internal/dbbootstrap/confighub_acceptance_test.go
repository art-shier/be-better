package dbbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/httpapi"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfigHubAcceptance(t *testing.T) {
	if os.Getenv("DAYORDER_CONFIGHUB_ACCEPTANCE") != "1" {
		t.Skip("ConfigHub PostgreSQL acceptance is opt-in")
	}

	source, err := config.LoadConfigHubDatabaseSource(os.LookupEnv)
	if err != nil {
		t.Fatalf("load ConfigHub database source: %v", err)
	}
	config.ScrubConfigHubDatabaseEnvironment()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	migrationURL, err := source.RoleURL(config.Development, config.DatabaseRoleMigrator)
	if err != nil {
		t.Fatal(err)
	}
	if err = dbmigrations.RequireCurrent(migrationURL); err != nil {
		t.Fatalf("dayorder-test migration state is not current: %v", err)
	}

	apiURL, err := source.RoleURL(config.Development, config.DatabaseRoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	handler, apiPool, err := newAcceptanceAPI(ctx, apiURL)
	if err != nil {
		t.Fatalf("start acceptance API: %v", err)
	}
	defer apiPool.Close()

	assertAcceptanceAPICannotCreateTables(t, ctx, apiPool)
	assertAcceptanceWorkerCanReadOutboxMetrics(t, ctx, source)

	admin, err := pgx.Connect(ctx, source.AdminURL("dayorder-test"))
	if err != nil {
		t.Fatal("connect to dayorder-test for acceptance cleanup")
	}
	defer func() { _ = admin.Close(context.Background()) }()

	server := httptest.NewServer(handler)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar

	for _, healthPath := range []string{"/health/live", "/health/ready"} {
		doAcceptanceRequest(t, client, http.MethodGet, server.URL+healthPath, nil, nil, http.StatusOK)
	}
	doAcceptanceRequest(t, client, http.MethodGet, server.URL+"/api/v1/goals", nil, nil, http.StatusUnauthorized)

	unique := uuid.NewString()
	email := "confighub-acceptance-" + unique + "@example.invalid"
	password := "acceptance-password-" + uuid.NewString()
	accountID := uuid.Nil
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := admin.Exec(cleanupCtx, `
DELETE FROM dayorder.users
WHERE id = $1 OR normalized_email = lower(btrim($2))`, accountID, email); cleanupErr != nil {
			t.Errorf("clean unique dayorder-test acceptance account: %v", cleanupErr)
		}
	}()

	registerBody := doAcceptanceRequest(t, client, http.MethodPost, server.URL+"/api/v1/auth/register", map[string]any{
		"email": email, "displayName": "ConfigHub Acceptance", "password": password,
	}, nil, http.StatusCreated)
	var registered struct {
		User model.Account `json:"user"`
	}
	decodeAcceptanceResponse(t, registerBody, &registered)
	if registered.User.ID == uuid.Nil || registered.User.Email != email {
		t.Fatal("registration response did not contain the unique acceptance account")
	}
	accountID = registered.User.ID

	var verificationToken string
	if err = admin.QueryRow(ctx, `
SELECT payload ->> 'token'
FROM dayorder.outbox_events
WHERE user_id = $1 AND event_type = 'email.verification.requested'
ORDER BY created_at DESC
LIMIT 1`, accountID).Scan(&verificationToken); err != nil {
		t.Fatal("read unique acceptance verification token")
	}
	if verificationToken == "" {
		t.Fatal("unique acceptance verification token was empty")
	}
	doAcceptanceRequest(t, client, http.MethodPost, server.URL+"/api/v1/auth/verify-email", map[string]string{
		"token": verificationToken,
	}, nil, http.StatusOK)
	verificationToken = ""

	deviceID := uuid.NewString()
	mutationHeaders := func() map[string]string {
		return map[string]string{
			"X-Device-ID":     deviceID,
			"Idempotency-Key": uuid.NewString(),
		}
	}
	goalBody := doAcceptanceRequest(t, client, http.MethodPost, server.URL+"/api/v1/goals", map[string]any{
		"title": "ConfigHub acceptance goal", "area": "Work", "metricType": "project",
		"targetValue": 1, "currentValue": 0, "unit": "", "startDate": time.Now().UTC().Format(time.DateOnly),
		"status": "active", "health": "normal",
	}, mutationHeaders(), http.StatusCreated)
	var goal model.Goal
	decodeAcceptanceResponse(t, goalBody, &goal)
	if goal.ID == uuid.Nil || goal.Version != 1 {
		t.Fatal("goal creation did not return a versioned resource")
	}

	readGoalBody := doAcceptanceRequest(t, client, http.MethodGet, server.URL+"/api/v1/goals/"+goal.ID.String(), nil, nil, http.StatusOK)
	var readGoal model.Goal
	decodeAcceptanceResponse(t, readGoalBody, &readGoal)
	if readGoal.ID != goal.ID {
		t.Fatal("goal read returned a different resource")
	}
	goalHeaders := mutationHeaders()
	goalHeaders["If-Match"] = fmt.Sprintf(`"%d"`, goal.Version)
	goalHeaders["Content-Type"] = "application/merge-patch+json"
	updatedGoalBody := doAcceptanceRequest(t, client, http.MethodPatch, server.URL+"/api/v1/goals/"+goal.ID.String(), map[string]string{
		"title": "ConfigHub acceptance goal updated",
	}, goalHeaders, http.StatusOK)
	var updatedGoal model.Goal
	decodeAcceptanceResponse(t, updatedGoalBody, &updatedGoal)
	if updatedGoal.Version != 2 || updatedGoal.Title != "ConfigHub acceptance goal updated" {
		t.Fatal("goal update did not persist the expected version and title")
	}

	taskBody := doAcceptanceRequest(t, client, http.MethodPost, server.URL+"/api/v1/tasks", map[string]any{
		"title": "ConfigHub acceptance task", "status": "todo", "priority": "normal",
		"estimateMinutes": 15, "goalId": goal.ID,
	}, mutationHeaders(), http.StatusCreated)
	var task model.Task
	decodeAcceptanceResponse(t, taskBody, &task)
	if task.ID == uuid.Nil || task.Version != 1 || task.GoalID == nil || *task.GoalID != goal.ID {
		t.Fatal("task creation did not return the expected versioned relation")
	}

	readTaskBody := doAcceptanceRequest(t, client, http.MethodGet, server.URL+"/api/v1/tasks/"+task.ID.String(), nil, nil, http.StatusOK)
	var readTask model.Task
	decodeAcceptanceResponse(t, readTaskBody, &readTask)
	if readTask.ID != task.ID {
		t.Fatal("task read returned a different resource")
	}
	taskHeaders := mutationHeaders()
	taskHeaders["If-Match"] = fmt.Sprintf(`"%d"`, task.Version)
	taskHeaders["Content-Type"] = "application/merge-patch+json"
	updatedTaskBody := doAcceptanceRequest(t, client, http.MethodPatch, server.URL+"/api/v1/tasks/"+task.ID.String(), map[string]string{
		"status": "doing",
	}, taskHeaders, http.StatusOK)
	var updatedTask model.Task
	decodeAcceptanceResponse(t, updatedTaskBody, &updatedTask)
	if updatedTask.Version != 2 || updatedTask.Status != "doing" {
		t.Fatal("task update did not persist the expected version and status")
	}
}

func newAcceptanceAPI(ctx context.Context, databaseURL string) (http.Handler, *pgxpool.Pool, error) {
	databaseConfig := config.DatabaseConfig{
		URL: databaseURL, MaxConns: 4, MinConns: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute,
		StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second,
		IdleTransactionTimeout: 10 * time.Second, HealthTimeout: 10 * time.Second,
	}
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (http.Handler, *pgxpool.Pool, error) {
		pool.Close()
		return nil, nil, err
	}

	repository, err := postgresstore.NewAccountRepository(pool)
	if err != nil {
		return fail(err)
	}
	accounts, err := service.NewAccountService(repository)
	if err != nil {
		return fail(err)
	}
	sessions, err := service.NewSessionService(repository, repository, []byte("confighub-acceptance-hmac-key-32-bytes"))
	if err != nil {
		return fail(err)
	}
	transactor, err := database.NewPoolTransactor(pool)
	if err != nil {
		return fail(err)
	}
	idempotency, err := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	if err != nil {
		return fail(err)
	}
	syncService, err := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, []byte("confighub-acceptance-hmac-key-32-bytes"))
	if err != nil {
		return fail(err)
	}
	auditService, err := service.NewAuditService(postgresstore.NewAuditRepository())
	if err != nil {
		return fail(err)
	}
	commands, err := service.NewCommandService(transactor, idempotency, syncService, auditService, postgresstore.NewOutboxWriter())
	if err != nil {
		return fail(err)
	}
	cursors, err := service.NewResourceCursorCodec([]byte("confighub-acceptance-hmac-key-32-bytes"))
	if err != nil {
		return fail(err)
	}
	goals, err := service.NewGoalService(postgresstore.NewGoalRepository(), transactor, commands, cursors)
	if err != nil {
		return fail(err)
	}
	tasks, err := service.NewTaskService(postgresstore.NewTaskRepository(), transactor, commands, cursors)
	if err != nil {
		return fail(err)
	}
	handler, err := httpapi.NewRouter(httpapi.RouterOptions{
		Accounts: accounts,
		Sessions: sessions,
		Goals:    goals,
		Tasks:    tasks,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready: func(readyCtx context.Context) error {
			if pingErr := database.Ping(readyCtx, pool, databaseConfig.HealthTimeout); pingErr != nil {
				return pingErr
			}
			var version uint
			var dirty bool
			if queryErr := pool.QueryRow(readyCtx, "SELECT version, dirty FROM dayorder.schema_migrations LIMIT 1").Scan(&version, &dirty); queryErr != nil {
				return queryErr
			}
			return dbmigrations.RequireCompatibleVersion(version, dirty)
		},
	})
	if err != nil {
		return fail(err)
	}
	return handler, pool, nil
}

func assertAcceptanceAPICannotCreateTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, createErr := transaction.Exec(ctx, "CREATE TABLE dayorder.confighub_acceptance_forbidden (id integer)")
	_ = transaction.Rollback(context.Background())
	if createErr == nil {
		t.Fatal("dayorder_api unexpectedly created a table")
	}
}

func assertAcceptanceWorkerCanReadOutboxMetrics(t *testing.T, ctx context.Context, source config.ConfigHubDatabaseSource) {
	t.Helper()
	workerURL, err := source.RoleURL(config.Development, config.DatabaseRoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(ctx, workerURL)
	if err != nil {
		t.Fatal("connect to dayorder-test as dayorder_worker")
	}
	defer func() { _ = connection.Close(context.Background()) }()
	var backlog, dead int64
	var oldestAge float64
	if err = connection.QueryRow(ctx, "SELECT backlog, oldest_age_seconds, dead_total FROM dayorder.outbox_metrics()").Scan(&backlog, &oldestAge, &dead); err != nil {
		t.Fatal("dayorder_worker cannot read permitted Outbox metrics")
	}
}

func doAcceptanceRequest(t *testing.T, client *http.Client, method, endpoint string, payload any, headers map[string]string, wantStatus int) []byte {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if payload != nil && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s request failed: %v", method, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s request returned status %d, want %d", method, response.StatusCode, wantStatus)
	}
	return responseBody
}

func decodeAcceptanceResponse(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal("decode acceptance API response")
	}
}
