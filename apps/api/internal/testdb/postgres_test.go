package testdb_test

import (
	"context"
	"testing"
	"time"

	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/testdb"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresFixtureAppliesMigrationsFromEmptyDatabase(t *testing.T) {
	database := testdb.StartForTest(t)

	if err := dbmigrations.Up(database.MigrationURL); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if err := dbmigrations.Up(database.MigrationURL); err != nil {
		t.Fatalf("idempotent migration run: %v", err)
	}
	if err := dbmigrations.RequireCurrent(database.MigrationURL); err != nil {
		t.Fatalf("schema version check: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	migrationPool, err := pgxpool.New(ctx, database.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()

	var tableCount int
	if err = migrationPool.QueryRow(ctx, `
SELECT count(*)
FROM pg_catalog.pg_tables
WHERE schemaname = 'dayorder'
`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 29 {
		t.Fatalf("tables in dayorder schema = %d, want 29 including schema_migrations", tableCount)
	}

	var rlsTableCount int
	if err = migrationPool.QueryRow(ctx, `
SELECT count(*)
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'dayorder'
  AND relation.relkind = 'r'
  AND relation.relrowsecurity
`).Scan(&rlsTableCount); err != nil {
		t.Fatal(err)
	}
	if rlsTableCount != 27 {
		t.Fatalf("RLS-enabled tenant tables = %d, want 27", rlsTableCount)
	}

	pool, err := pgxpool.New(ctx, database.APIURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var role string
	if err = pool.QueryRow(ctx, "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "dayorder_api" {
		t.Fatalf("API connection role = %q, want dayorder_api", role)
	}
	if _, err = pool.Exec(ctx, "CREATE TABLE dayorder.forbidden_ddl (id integer)"); err == nil {
		t.Fatal("dayorder_api unexpectedly executed DDL")
	}
}
