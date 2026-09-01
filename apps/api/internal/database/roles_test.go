package database_test

import (
	"context"
	"testing"
	"time"

	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/testdb"

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupSecurityDatabase(t testing.TB) *testdb.Postgres {
	t.Helper()
	database := testdb.StartForTest(t)
	if err := dbmigrations.Up(database.MigrationURL); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return database
}

func openTestPool(t testing.TB, databaseURL string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestDatabaseRolesHaveMinimumPrivileges(t *testing.T) {
	database := setupSecurityDatabase(t)
	apiPool := openTestPool(t, database.APIURL)
	workerPool := openTestPool(t, database.WorkerURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := apiPool.Exec(ctx, "CREATE TABLE dayorder.api_must_not_create_tables (id integer)"); err == nil {
		t.Fatal("dayorder_api unexpectedly executed DDL")
	}
	if _, err := apiPool.Exec(ctx, "SELECT * FROM dayorder.login_throttles"); err == nil {
		t.Fatal("dayorder_api unexpectedly read authentication throttle storage directly")
	}
	var visibleGoals int
	if err := workerPool.QueryRow(ctx, "SELECT count(*) FROM dayorder.goals").Scan(&visibleGoals); err != nil {
		t.Fatalf("dayorder_worker cannot query a tenant business table through RLS: %v", err)
	}
	if visibleGoals != 0 {
		t.Fatalf("dayorder_worker visible goals without tenant context = %d, want 0", visibleGoals)
	}

	var claimed int
	const lockToken = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if err := workerPool.QueryRow(
		ctx,
		"SELECT count(*) FROM dayorder.claim_outbox_events(1, $1::uuid)",
		lockToken,
	).Scan(&claimed); err != nil {
		t.Fatalf("worker cannot call restricted outbox claim function: %v", err)
	}
	if claimed != 0 {
		t.Fatalf("claimed events from empty outbox = %d, want 0", claimed)
	}

	var backlog, dead int64
	var oldestAge float64
	if err := workerPool.QueryRow(ctx, "SELECT backlog, oldest_age_seconds, dead_total FROM dayorder.outbox_metrics()").Scan(&backlog, &oldestAge, &dead); err != nil {
		t.Fatalf("worker cannot read aggregate outbox metrics: %v", err)
	}
	if backlog != 0 || oldestAge != 0 || dead != 0 {
		t.Fatalf("empty outbox metrics = backlog %d age %v dead %d", backlog, oldestAge, dead)
	}
}
