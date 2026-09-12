package agenttest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct {
	Fixture               *testdb.Postgres
	API, Worker, Migrator *pgxpool.Pool
	UserA, UserB          uuid.UUID
	DeviceA, DeviceB      uuid.UUID
}

func Open(t testing.TB) *Database {
	t.Helper()
	fixture := testdb.StartIsolatedForTest(t)
	database, err := NewDatabase(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Errorf("close isolated PostgreSQL pools: %v", closeErr)
		}
	})
	t.Logf("created isolated PostgreSQL database %s", fixture.DatabaseName())
	return database
}

func NewDatabase(ctx context.Context, fixture *testdb.Postgres) (*Database, error) {
	if fixture == nil {
		return nil, errors.New("isolated PostgreSQL fixture is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := migrations.Up(fixture.MigrationURL); err != nil {
		return nil, errors.New("apply migrations to isolated PostgreSQL fixture")
	}

	database := &Database{
		Fixture: fixture,
		UserA:   uuid.New(), UserB: uuid.New(), DeviceA: uuid.New(), DeviceB: uuid.New(),
	}
	var err error
	defer func() {
		if err != nil {
			_ = database.Close(context.Background())
		}
	}()
	if database.Migrator, err = openPool(ctx, fixture.MigrationURL, 1); err != nil {
		return nil, err
	}
	if database.API, err = openPool(ctx, fixture.APIURL, 4); err != nil {
		return nil, err
	}
	if database.Worker, err = openPool(ctx, fixture.WorkerURL, 4); err != nil {
		return nil, err
	}
	for index, account := range []struct {
		userID, deviceID uuid.UUID
	}{{database.UserA, database.DeviceA}, {database.UserB, database.DeviceB}} {
		email := "agent-fixture-" + account.userID.String() + "@example.com"
		if _, seedErr := database.Migrator.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status, email_verified_at)
VALUES ($1, $2, $2, $3, 'agent-fixture-password-hash', 'active', now())
`, account.userID, email, "Agent Fixture "+string(rune('A'+index))); seedErr != nil {
			err = errors.New("create isolated PostgreSQL fixture account")
			return nil, err
		}
		if _, seedErr := database.Migrator.Exec(ctx, `INSERT INTO dayorder.user_settings (user_id) VALUES ($1)`, account.userID); seedErr != nil {
			err = errors.New("create isolated PostgreSQL fixture settings")
			return nil, err
		}
		if _, seedErr := database.Migrator.Exec(ctx, `
INSERT INTO dayorder.user_devices (id, user_id, device_name, platform) VALUES ($2, $1, $3, 'web')
`, account.userID, account.deviceID, "Agent Fixture Device "+string(rune('A'+index))); seedErr != nil {
			err = errors.New("create isolated PostgreSQL fixture device")
			return nil, err
		}
	}
	return database, nil
}

func (database *Database) Close(context.Context) error {
	if database == nil {
		return nil
	}
	if database.API != nil {
		database.API.Close()
		database.API = nil
	}
	if database.Worker != nil {
		database.Worker.Close()
		database.Worker = nil
	}
	if database.Migrator != nil {
		database.Migrator.Close()
		database.Migrator = nil
	}
	return nil
}

func openPool(ctx context.Context, databaseURL string, maxConnections int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse isolated PostgreSQL pool configuration: %w", err)
	}
	config.MaxConns = maxConnections
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open isolated PostgreSQL pool: %w", err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect isolated PostgreSQL pool: %w", err)
	}
	return pool, nil
}
