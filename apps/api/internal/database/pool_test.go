package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/config"
)

func TestBuildPoolConfigAppliesLimitsAndPostgresTimeouts(t *testing.T) {
	databaseConfig := config.DatabaseConfig{
		URL:                    "postgres://dayorder:secret@127.0.0.1:5432/dayorder?sslmode=disable",
		MaxConns:               12,
		MinConns:               3,
		MaxConnLifetime:        45 * time.Minute,
		MaxConnIdleTime:        7 * time.Minute,
		StatementTimeout:       6 * time.Second,
		LockTimeout:            1500 * time.Millisecond,
		IdleTransactionTimeout: 11 * time.Second,
		HealthTimeout:          time.Second,
	}

	poolConfig, err := BuildPoolConfig(databaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if poolConfig.MaxConns != 12 || poolConfig.MinConns != 3 {
		t.Fatalf("pool limits = %d/%d", poolConfig.MinConns, poolConfig.MaxConns)
	}
	if poolConfig.MaxConnLifetime != 45*time.Minute || poolConfig.MaxConnIdleTime != 7*time.Minute {
		t.Fatalf("pool lifetime = %s/%s", poolConfig.MaxConnLifetime, poolConfig.MaxConnIdleTime)
	}
	runtime := poolConfig.ConnConfig.RuntimeParams
	if runtime["timezone"] != "UTC" || runtime["statement_timeout"] != "6000" || runtime["lock_timeout"] != "1500" || runtime["idle_in_transaction_session_timeout"] != "11000" {
		t.Fatalf("runtime params = %#v", runtime)
	}
}

type blockingPinger struct{}

func (blockingPinger) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type errorPinger struct{ err error }

func (p errorPinger) Ping(context.Context) error { return p.err }

func TestPingUsesBoundedContextAndPreservesDependencyError(t *testing.T) {
	started := time.Now()
	err := Ping(context.Background(), blockingPinger{}, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("health check took %s", elapsed)
	}

	want := errors.New("database unavailable")
	if err = Ping(context.Background(), errorPinger{err: want}, time.Second); !errors.Is(err, want) {
		t.Fatalf("expected dependency error, got %v", err)
	}
}
