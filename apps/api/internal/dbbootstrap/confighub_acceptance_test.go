package dbbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	"github.com/jackc/pgx/v5/pgconn"
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
	errorRecorder := &acceptanceErrorRecorder{}
	handler, apiPool, err := newAcceptanceAPI(ctx, apiURL, errorRecorder)
	if err != nil {
		t.Fatalf("start acceptance API: %v", err)
	}
	defer apiPool.Close()

	assertAcceptanceAPICannotCreateTables(t, ctx, apiPool)
	assertAcceptanceAPIRegistrationGraph(t, ctx, apiPool)
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
	}, nil, http.StatusCreated, errorRecorder)
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
	deviceBody := doAcceptanceRequest(t, client, http.MethodPut, server.URL+"/api/v1/users/me/devices/"+deviceID, map[string]string{
		"deviceName": "ConfigHub Acceptance", "platform": "web",
	}, nil, http.StatusCreated)
	var deviceRegistration service.DeviceRegistration
	decodeAcceptanceResponse(t, deviceBody, &deviceRegistration)
	if deviceRegistration.Device.ID.String() != deviceID {
		t.Fatal("device registration returned a different device")
	}
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

func newAcceptanceAPI(ctx context.Context, databaseURL string, errorRecorder *acceptanceErrorRecorder) (http.Handler, *pgxpool.Pool, error) {
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
	devices, err := service.NewDeviceService(postgresstore.NewDeviceRepository(), transactor, auditService)
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
		Devices:  devices,
		Goals:    goals,
		Tasks:    tasks,
		Logger:   slog.New(acceptanceLogHandler{recorder: errorRecorder}),
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

func assertAcceptanceAPIRegistrationGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var canSelectUsers bool
	if err := pool.QueryRow(ctx, "SELECT pg_catalog.has_table_privilege(current_user, 'dayorder.users', 'SELECT')").Scan(&canSelectUsers); err != nil || !canSelectUsers {
		t.Fatalf("dayorder_api users SELECT privilege = %t", canSelectUsers)
	}
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal("begin API registration permission probe")
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	userID := uuid.New()
	email := "permission-probe-" + userID.String() + "@example.invalid"
	steps := []struct {
		name      string
		query     string
		arguments []any
	}{
		{name: "set user context", query: "SELECT dayorder.set_user_context($1)", arguments: []any{userID}},
		{name: "create user", query: `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status)
VALUES ($1, $2, $2, 'Permission Probe', 'diagnostic-hash', 'pending_verification')
RETURNING *`, arguments: []any{userID, email}},
		{name: "create user settings", query: `
INSERT INTO dayorder.user_settings (user_id, schema_version, version, settings)
VALUES ($1, 1, 1, '{}'::jsonb)`, arguments: []any{userID}},
		{name: "create account token", query: `
INSERT INTO dayorder.account_tokens (id, user_id, purpose, token_hash, expires_at)
VALUES ($1, $2, 'verify_email', $3, $4)`, arguments: []any{uuid.New(), userID, []byte("diagnostic-token-hash"), time.Now().UTC().Add(time.Hour)}},
		{name: "create Outbox event", query: `
INSERT INTO dayorder.outbox_events (id, user_id, event_type, aggregate_type, aggregate_id, payload, available_at)
VALUES ($1, $2, 'email.verification.requested', 'user', $2, '{}'::jsonb, now())`, arguments: []any{uuid.New(), userID}},
	}
	for _, step := range steps {
		if _, err = transaction.Exec(ctx, step.query, step.arguments...); err != nil {
			t.Fatalf("API registration permission probe failed at %s with SQLSTATE %s", step.name, acceptanceSQLState(err))
		}
	}
}

func acceptanceSQLState(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return "non-postgresql-error"
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

func doAcceptanceRequest(t *testing.T, client *http.Client, method, endpoint string, payload any, headers map[string]string, wantStatus int, diagnostics ...*acceptanceErrorRecorder) []byte {
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
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(responseBody, &envelope)
		diagnostic := ""
		if len(diagnostics) == 1 {
			diagnostic = diagnostics[0].safeSummary()
		}
		t.Fatalf("%s request returned status %d with code %q, want %d%s", method, response.StatusCode, envelope.Error.Code, wantStatus, diagnostic)
	}
	return responseBody
}

type acceptanceErrorRecorder struct {
	mutex sync.Mutex
	err   error
}

func TestAcceptanceErrorRecorderClassifiesPostgresPermissionErrors(t *testing.T) {
	tests := []struct {
		name    string
		message string
		table   string
		want    string
	}{
		{
			name:    "row-level security",
			message: `new row violates row-level security policy for table "users"`,
			table:   "users",
			want:    `; operation="create pending account", sqlstate="42501", permission_class="row_level_security", table="users"`,
		},
		{
			name:    "table permission recovers allowlisted table from message",
			message: `permission denied for table users`,
			want:    `; operation="create pending account", sqlstate="42501", permission_class="table_permission", table="users"`,
		},
		{
			name:    "column permission",
			message: `permission denied for column email of relation users`,
			table:   "users",
			want:    `; operation="create pending account", sqlstate="42501", permission_class="column_permission", table="users"`,
		},
		{
			name:    "other does not expose message or unknown table",
			message: `secret diagnostic content`,
			table:   "secret_table",
			want:    `; operation="create pending account", sqlstate="42501", permission_class="other"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &acceptanceErrorRecorder{err: fmt.Errorf("create pending account: %w", &pgconn.PgError{
				Code:      "42501",
				Message:   test.message,
				TableName: test.table,
			})}
			if got := recorder.safeSummary(); got != test.want {
				t.Fatalf("safeSummary() = %q, want %q", got, test.want)
			}
		})
	}
}

func (recorder *acceptanceErrorRecorder) safeSummary() string {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	if recorder.err == nil {
		return "; operation=unavailable"
	}
	operation := recorder.err.Error()
	if separator := strings.Index(operation, ":"); separator >= 0 {
		operation = operation[:separator]
	}
	permissionClass := "other"
	tableName := ""
	var postgresError *pgconn.PgError
	if errors.As(recorder.err, &postgresError) {
		switch {
		case strings.Contains(postgresError.Message, "row-level security"):
			permissionClass = "row_level_security"
		case strings.Contains(postgresError.Message, "permission denied for column"):
			permissionClass = "column_permission"
		case strings.Contains(postgresError.Message, "permission denied for table"):
			permissionClass = "table_permission"
		}
		for _, allowedTable := range []string{"users", "user_settings", "account_tokens", "outbox_events"} {
			if postgresError.TableName == allowedTable ||
				strings.Contains(postgresError.Message, "table "+allowedTable) ||
				strings.Contains(postgresError.Message, `table "`+allowedTable+`"`) {
				tableName = allowedTable
				break
			}
		}
	}
	summary := fmt.Sprintf("; operation=%q, sqlstate=%q, permission_class=%q", operation, acceptanceSQLState(recorder.err), permissionClass)
	if tableName != "" {
		summary += fmt.Sprintf(", table=%q", tableName)
	}
	return summary
}

type acceptanceLogHandler struct {
	recorder *acceptanceErrorRecorder
}

func (handler acceptanceLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (handler acceptanceLogHandler) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attribute slog.Attr) bool {
		if attribute.Key != "error" {
			return true
		}
		captured, ok := attribute.Value.Any().(error)
		if ok {
			handler.recorder.mutex.Lock()
			handler.recorder.err = captured
			handler.recorder.mutex.Unlock()
		}
		return true
	})
	return nil
}

func (handler acceptanceLogHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler acceptanceLogHandler) WithGroup(string) slog.Handler      { return handler }

func decodeAcceptanceResponse(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal("decode acceptance API response")
	}
}
