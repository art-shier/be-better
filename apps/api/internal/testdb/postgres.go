package testdb

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	testDatabase          = "dayorder_test"
	testAdminUser         = "postgres"
	testAdminPassword     = "dayorder-test-admin-password"
	testMigratorPassword  = "dayorder-test-migrator-password"
	testAPIPassword       = "dayorder-test-api-password"
	testWorkerPassword    = "dayorder-test-worker-password"
	postgresStartupWindow = 2 * time.Minute
)

type Postgres struct {
	Container    *postgres.PostgresContainer
	AdminURL     string
	MigrationURL string
	APIURL       string
	WorkerURL    string
}

var (
	dockerOnce      sync.Once
	dockerAvailable bool
)

func DockerAvailable() bool {
	dockerOnce.Do(func() {
		path, err := exec.LookPath("docker")
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, path, "version", "--format", "{{.Server.Version}}")
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		dockerAvailable = command.Run() == nil
	})
	return dockerAvailable
}

func StartForTest(t testing.TB) *Postgres {
	t.Helper()
	if !DockerAvailable() {
		t.Skip("Docker is unavailable; real PostgreSQL integration test skipped")
	}

	ctx, cancel := context.WithTimeout(context.Background(), postgresStartupWindow)
	database, err := Start(ctx)
	cancel()
	if err != nil {
		t.Fatalf("start PostgreSQL test container: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if terminateErr := database.Container.Terminate(cleanupContext); terminateErr != nil {
			t.Errorf("terminate PostgreSQL test container: %v", terminateErr)
		}
	})
	return database
}

func Start(ctx context.Context) (*Postgres, error) {
	container, err := postgres.Run(
		ctx,
		"postgres:17-alpine",
		postgres.WithDatabase(testDatabase),
		postgres.WithUsername(testAdminUser),
		postgres.WithPassword(testAdminPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("run PostgreSQL container: %w", err)
	}

	adminURL, err := container.ConnectionString(ctx, "sslmode=disable", "timezone=UTC")
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, fmt.Errorf("build PostgreSQL test connection string: %w", err)
	}

	database := &Postgres{
		Container:    container,
		AdminURL:     adminURL,
		MigrationURL: connectionURL(adminURL, "dayorder_migrator", testMigratorPassword, "dayorder"),
		APIURL:       connectionURL(adminURL, "dayorder_api", testAPIPassword),
		WorkerURL:    connectionURL(adminURL, "dayorder_worker", testWorkerPassword),
	}
	if err = bootstrapRoles(ctx, adminURL); err != nil {
		_ = container.Terminate(context.Background())
		return nil, err
	}
	return database, nil
}

func connectionURL(adminURL, username, password string, searchPath ...string) string {
	parsed, err := url.Parse(adminURL)
	if err != nil {
		panic(fmt.Sprintf("testcontainers returned an invalid PostgreSQL URL: %v", err))
	}
	parsed.User = url.UserPassword(username, password)
	if len(searchPath) > 0 {
		query := parsed.Query()
		query.Set("search_path", searchPath[0])
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func bootstrapRoles(ctx context.Context, adminURL string) error {
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL role bootstrap connection: %w", err)
	}
	defer pool.Close()

	const statements = `
CREATE ROLE dayorder_migrator LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'dayorder-test-migrator-password';
CREATE ROLE dayorder_api LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'dayorder-test-api-password';
CREATE ROLE dayorder_worker LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'dayorder-test-worker-password';
GRANT CONNECT ON DATABASE dayorder_test TO dayorder_migrator, dayorder_api, dayorder_worker;
GRANT CREATE ON DATABASE dayorder_test TO dayorder_migrator;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA dayorder AUTHORIZATION dayorder_migrator;
REVOKE ALL ON SCHEMA dayorder FROM PUBLIC;
`
	if _, err = pool.Exec(ctx, statements); err != nil {
		return fmt.Errorf("bootstrap PostgreSQL test roles: %w", err)
	}
	return nil
}
