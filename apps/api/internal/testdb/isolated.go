package testdb

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	agentTestDatabaseSourceEnvironment = "DAYORDER_AGENT_TEST_DB_SOURCE"
	dockerDatabaseSource               = "docker"
	configHubDatabaseSource            = "confighub"
)

type isolatedStarter func(context.Context) (*Postgres, error)

func StartIsolated(ctx context.Context) (*Postgres, error) {
	return startIsolatedWith(
		ctx,
		os.Getenv(agentTestDatabaseSourceEnvironment),
		Start,
		startConfigHubFromEnvironment,
	)
}

func StartIsolatedForTest(t testing.TB) *Postgres {
	t.Helper()

	source := strings.TrimSpace(os.Getenv(agentTestDatabaseSourceEnvironment))
	if source == "" && !DockerAvailable() {
		t.Skip("Docker is unavailable; real PostgreSQL integration test skipped")
	}

	ctx, cancel := context.WithTimeout(context.Background(), postgresStartupWindow)
	database, err := StartIsolated(ctx)
	cancel()
	if err != nil {
		t.Fatalf("start isolated PostgreSQL test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if cleanupErr := database.Close(cleanupContext); cleanupErr != nil {
			t.Errorf("close isolated PostgreSQL test database %s: %v", database.DatabaseName(), cleanupErr)
		} else {
			t.Logf("deleted isolated PostgreSQL database %s after guarded cleanup", database.DatabaseName())
		}
	})
	return database
}

func startIsolatedWith(ctx context.Context, source string, docker, configHub isolatedStarter) (*Postgres, error) {
	switch strings.TrimSpace(source) {
	case "", dockerDatabaseSource:
		return docker(ctx)
	case configHubDatabaseSource:
		return configHub(ctx)
	default:
		return nil, errors.New(agentTestDatabaseSourceEnvironment + " must be empty, docker, or confighub")
	}
}
