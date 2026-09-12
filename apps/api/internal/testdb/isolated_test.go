package testdb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStartIsolatedWithSelectsOnlyConfiguredBackend(t *testing.T) {
	sentinel := &Postgres{}
	tests := []struct {
		name          string
		source        string
		wantDocker    int
		wantConfigHub int
	}{
		{name: "empty defaults to Docker", source: "", wantDocker: 1},
		{name: "explicit Docker", source: "docker", wantDocker: 1},
		{name: "explicit ConfigHub", source: "confighub", wantConfigHub: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dockerCalls := 0
			configHubCalls := 0
			got, err := startIsolatedWith(context.Background(), test.source,
				func(context.Context) (*Postgres, error) {
					dockerCalls++
					return sentinel, nil
				},
				func(context.Context) (*Postgres, error) {
					configHubCalls++
					return sentinel, nil
				},
			)
			if err != nil {
				t.Fatalf("select isolated database backend: %v", err)
			}
			if got != sentinel || dockerCalls != test.wantDocker || configHubCalls != test.wantConfigHub {
				t.Fatalf("backend result/calls = %p/%d/%d, want %p/%d/%d", got, dockerCalls, configHubCalls, sentinel, test.wantDocker, test.wantConfigHub)
			}
		})
	}
}

func TestStartIsolatedWithRejectsUnknownSourceWithoutFallback(t *testing.T) {
	calls := 0
	start := func(context.Context) (*Postgres, error) {
		calls++
		return &Postgres{}, nil
	}

	_, err := startIsolatedWith(context.Background(), "remote", start, start)
	if err == nil || !strings.Contains(err.Error(), "DAYORDER_AGENT_TEST_DB_SOURCE") {
		t.Fatalf("unknown source returned error %v", err)
	}
	if calls != 0 {
		t.Fatalf("unknown source invoked %d backends, want none", calls)
	}
}

func TestStartIsolatedWithDoesNotFallbackAfterExplicitFailure(t *testing.T) {
	dockerCalls := 0
	_, err := startIsolatedWith(context.Background(), "confighub",
		func(context.Context) (*Postgres, error) {
			dockerCalls++
			return &Postgres{}, nil
		},
		func(context.Context) (*Postgres, error) {
			return nil, errors.New("ConfigHub unavailable")
		},
	)
	if err == nil {
		t.Fatal("explicit ConfigHub failure was ignored")
	}
	if dockerCalls != 0 {
		t.Fatalf("explicit ConfigHub failure fell back to Docker %d times", dockerCalls)
	}
}

func TestStartIsolatedConfigHubRejectsIncompleteSourceBeforeConnecting(t *testing.T) {
	t.Setenv(agentTestDatabaseSourceEnvironment, "confighub")
	setConfigHubEnvironment(t)
	t.Setenv("db_password", "")

	_, err := StartIsolated(context.Background())
	if err == nil || !strings.Contains(err.Error(), "db_password") {
		t.Fatalf("incomplete ConfigHub source returned error %v", err)
	}
}

func TestPostgresCloseWithoutOwnedResourceIsIdempotent(t *testing.T) {
	database := &Postgres{}
	if err := database.Close(context.Background()); err != nil {
		t.Fatalf("close empty database fixture: %v", err)
	}
	if err := database.Close(context.Background()); err != nil {
		t.Fatalf("close empty database fixture again: %v", err)
	}
}

func setConfigHubEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"db_address":           "db.invalid",
		"db_port":              "5432",
		"db_username":          "fixture_admin",
		"db_password":          "admin-secret",
		"db_migrator_password": "migrator-secret",
		"db_api_password":      "api-secret",
		"db_worker_password":   "worker-secret",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
