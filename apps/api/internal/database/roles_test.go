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
	if _, err := workerPool.Exec(ctx, "SELECT * FROM dayorder.goals"); err == nil {
		t.Fatal("dayorder_worker unexpectedly scanned a tenant business table")
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
}
