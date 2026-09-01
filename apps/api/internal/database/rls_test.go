package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	userA = "11111111-1111-4111-8111-111111111111"
	userB = "22222222-2222-4222-8222-222222222222"
	goalA = "aaaaaaaa-1111-4111-8111-111111111111"
	goalB = "bbbbbbbb-2222-4222-8222-222222222222"
)

func TestRowLevelSecurityAndCompositeForeignKeysIsolateUsers(t *testing.T) {
	database := setupSecurityDatabase(t)
	migrationPool := openTestPool(t, database.MigrationURL)
	apiPool := openTestPool(t, database.APIURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	seedTenantRows(t, ctx, migrationPool)

	var count int
	if err := apiPool.QueryRow(ctx, "SELECT count(*) FROM dayorder.goals").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows visible without tenant context = %d, want 0", count)
	}

	tx, err := apiPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT dayorder.set_user_context($1::uuid)", userA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM dayorder.goals").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user A visible goals = %d, want 1", count)
	}

	var title string
	if err = tx.QueryRow(ctx, "SELECT title FROM dayorder.goals WHERE id = $1::uuid", goalB).Scan(&title); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reading user B goal error = %v, want pgx.ErrNoRows", err)
	}
	result, err := tx.Exec(ctx, "UPDATE dayorder.goals SET title = 'forbidden' WHERE id = $1::uuid", goalB)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected() != 0 {
		t.Fatalf("updated %d rows belonging to user B, want 0", result.RowsAffected())
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	assertCrossTenantGoalReferenceRejected(t, ctx, apiPool)
	assertTenantWriteCheckRejected(t, ctx, apiPool)
}

func seedTenantRows(t testing.TB, ctx context.Context, pool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO dayorder.users (
    id, email, normalized_email, display_name, password_hash, status, email_verified_at
) VALUES
    ($1::uuid, 'a@example.test', 'a@example.test', 'User A', 'hash-a', 'active', now()),
    ($2::uuid, 'b@example.test', 'b@example.test', 'User B', 'hash-b', 'active', now())
`, userA, userB)
	if err != nil {
		t.Fatalf("seed tenant users: %v", err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO dayorder.goals (
    id, user_id, title, why, area, metric_type, target_value, current_value,
    unit, start_date, status, health
) VALUES
    ($1::uuid, $2::uuid, 'Goal A', '', 'work', 'project', 100, 0, '%', current_date, 'active', 'normal'),
    ($3::uuid, $4::uuid, 'Goal B', '', 'life', 'project', 100, 0, '%', current_date, 'active', 'normal')
`, goalA, userA, goalB, userB)
	if err != nil {
		t.Fatalf("seed tenant goals: %v", err)
	}
}

func assertCrossTenantGoalReferenceRejected(t testing.TB, ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT dayorder.set_user_context($1::uuid)", userA); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO dayorder.tasks (
    id, user_id, title, status, priority, estimate_minutes, goal_id
) VALUES (
    'cccccccc-3333-4333-8333-333333333333'::uuid,
    $1::uuid,
    'Cross tenant task',
    'todo',
    'normal',
    10,
    $2::uuid
)
`, userA, goalB)
	assertPostgresCode(t, err, "23503")
}

func assertTenantWriteCheckRejected(t testing.TB, ctx context.Context, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT dayorder.set_user_context($1::uuid)", userA); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO dayorder.goals (
    id, user_id, title, why, area, metric_type, target_value, current_value,
    unit, start_date, status, health
) VALUES (
    'dddddddd-4444-4444-8444-444444444444'::uuid,
    $1::uuid,
    'Wrong tenant',
    '',
    'work',
    'project',
    100,
    0,
    '%',
    current_date,
    'active',
    'normal'
)
`, userB)
	assertPostgresCode(t, err, "42501")
}

func assertPostgresCode(t testing.TB, err error, want string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("error = %v, want PostgreSQL code %s", err, want)
	}
	if postgresError.Code != want {
		t.Fatalf("PostgreSQL code = %s, want %s: %v", postgresError.Code, want, err)
	}
}
