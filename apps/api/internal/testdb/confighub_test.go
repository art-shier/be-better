package testdb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"dayorder.local/api/internal/config"
)

const fixedTemporaryDatabaseName = "dayorder_agent_it_0123456789abcdef0123456789abcdef"

func TestTemporaryDatabaseNamesAreRandomlyGeneratedInOwnedNamespace(t *testing.T) {
	first, err := newTemporaryDatabaseName()
	if err != nil {
		t.Fatalf("generate first temporary database name: %v", err)
	}
	second, err := newTemporaryDatabaseName()
	if err != nil {
		t.Fatalf("generate second temporary database name: %v", err)
	}
	wantFormat := regexp.MustCompile(`^dayorder_agent_it_[0-9a-f]{32}$`)
	if !wantFormat.MatchString(first) || !wantFormat.MatchString(second) {
		t.Fatalf("temporary database names do not match the owned format: %q, %q", first, second)
	}
	if first == second {
		t.Fatalf("two generated temporary database names unexpectedly matched: %q", first)
	}
}

func TestTemporaryDatabaseNameValidationRejectsLookalikes(t *testing.T) {
	invalid := []string{
		"dayorder",
		"dayorder-test",
		"dayorder_agent_it_0123456789abcdef0123456789abcde",
		"dayorder_agent_it_0123456789abcdef0123456789abcdef0",
		"dayorder_agent_it_0123456789abcdef0123456789abcdeg",
		"DAYORDER_AGENT_IT_0123456789abcdef0123456789abcdef",
		"dayorder_agent_it_0123456789abcdef0123456789abcdef;drop database dayorder",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			if validTemporaryDatabaseName(name) {
				t.Fatalf("unsafe temporary database name %q was accepted", name)
			}
		})
	}
	if !validTemporaryDatabaseName(fixedTemporaryDatabaseName) {
		t.Fatalf("valid temporary database name %q was rejected", fixedTemporaryDatabaseName)
	}
}

func TestConfigHubTemporaryDatabaseRefusesExistingNameWithoutTakingOwnership(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.databases[fixedTemporaryDatabaseName] = databaseIdentity{oid: 41, owner: "someone_else"}

	_, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("database name collision returned error %v", err)
	}
	if got := backend.databases[fixedTemporaryDatabaseName]; got.oid != 41 || got.owner != "someone_else" {
		t.Fatalf("existing database identity changed to %+v", got)
	}
	if len(backend.dropped) != 0 {
		t.Fatalf("collision cleanup dropped databases %v", backend.dropped)
	}
}

func TestConfigHubTemporaryDatabaseCloseUsesPrivateIdentityNotPublicURLs(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	database := startMemoryConfigHubDatabase(t, backend)
	database.AdminURL = "postgresql://attacker:secret@other.invalid/dayorder"
	database.MigrationURL = "postgresql://attacker:secret@other.invalid/dayorder-test"
	database.APIURL = "postgresql://attacker:secret@other.invalid/unrelated"
	database.WorkerURL = "postgresql://attacker:secret@other.invalid/postgres"

	if err := database.Close(context.Background()); err != nil {
		t.Fatalf("close owned temporary database: %v", err)
	}
	if _, exists := backend.databases[fixedTemporaryDatabaseName]; exists {
		t.Fatalf("owned temporary database %q still exists", fixedTemporaryDatabaseName)
	}
	if len(backend.dropped) != 1 || backend.dropped[0] != fixedTemporaryDatabaseName {
		t.Fatalf("dropped databases = %v, want only %q", backend.dropped, fixedTemporaryDatabaseName)
	}
	if err := database.Close(context.Background()); err != nil {
		t.Fatalf("repeat close owned temporary database: %v", err)
	}
	if len(backend.dropped) != 1 {
		t.Fatalf("repeat close dropped databases %v", backend.dropped)
	}
}

func TestConfigHubTemporaryDatabaseCloseRefusesOIDOrOwnerMismatchWithoutDrop(t *testing.T) {
	tests := []struct {
		name     string
		identity databaseIdentity
	}{
		{name: "OID changed", identity: databaseIdentity{oid: 999, owner: "fixture_admin"}},
		{name: "owner changed", identity: databaseIdentity{oid: 101, owner: "different_owner"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newMemoryConfigHubBackend()
			database := startMemoryConfigHubDatabase(t, backend)
			backend.databases[fixedTemporaryDatabaseName] = test.identity

			err := database.Close(context.Background())
			if err == nil || !strings.Contains(err.Error(), "identity mismatch") || !strings.Contains(err.Error(), fixedTemporaryDatabaseName) {
				t.Fatalf("identity mismatch returned error %v", err)
			}
			if len(backend.dropped) != 0 {
				t.Fatalf("identity mismatch dropped databases %v", backend.dropped)
			}
		})
	}
}

func TestConfigHubTemporaryDatabaseSanitizesBackendErrors(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	const secret = "admin-secret-that-must-not-appear"
	backend.openErr = errors.New("dial postgresql://fixture_admin:" + secret + "@db.invalid/postgres")
	source := validConfigHubSource()
	source.AdminPassword = secret

	_, err := startConfigHubTemporaryDatabase(context.Background(), source, backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL operation failed") {
		t.Fatalf("backend failure returned error %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "postgresql://") {
		t.Fatal("backend failure exposed a credential or DSN")
	}
}

func TestConfigHubTemporaryDatabaseReportsSafePreflightConstraint(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.preflightErr = newSafeDatabaseValidationError("runtime role dayorder_api is not restricted")

	_, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	if err == nil || !strings.Contains(err.Error(), "runtime role dayorder_api is not restricted") {
		t.Fatalf("safe preflight constraint returned error %v", err)
	}
}

func TestConfigHubTemporaryDatabaseReconcilesAcknowledgedCreateOnFreshConnection(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.lookupAfterCreateErrors = map[int]error{
		1: errors.New("first maintenance connection was canceled with admin-secret"),
	}

	database, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	if err != nil {
		t.Fatalf("reconcile acknowledged create: %v", err)
	}
	if err = database.Close(context.Background()); err != nil {
		t.Fatalf("close reconciled temporary database: %v", err)
	}
	if _, exists := backend.databases[fixedTemporaryDatabaseName]; exists {
		t.Fatalf("reconciled temporary database %q still exists", fixedTemporaryDatabaseName)
	}
}

func TestConfigHubTemporaryDatabaseDoesNotAdoptAmbiguousCreateOutcome(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.createErr = errors.New("lost CREATE response with admin-secret")
	backend.createCommitsOnError = true

	_, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	err = safeDatabaseError("start lifecycle test", fmt.Errorf("normal wrapper: %w", err))
	err = safeDatabaseError("test fixture", err)
	assertCleanupIncompleteDiagnostic(t, err)
	if strings.Contains(err.Error(), "admin-secret") {
		t.Fatal("ambiguous CREATE error exposed a password")
	}
	if _, exists := backend.databases[fixedTemporaryDatabaseName]; !exists {
		t.Fatalf("ambiguous CREATE database %q was adopted or deleted", fixedTemporaryDatabaseName)
	}
	if len(backend.dropped) != 0 {
		t.Fatalf("ambiguous CREATE outcome dropped databases %v", backend.dropped)
	}
}

func TestConfigHubTemporaryDatabaseReportsFailedFreshCreationReconciliation(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.lookupAfterCreateErrors = map[int]error{
		1: errors.New("original identity query failed with admin-secret"),
		2: errors.New("fresh identity query failed with admin-secret"),
	}

	_, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	err = safeDatabaseError("start lifecycle test", err)
	assertCleanupIncompleteDiagnostic(t, err)
	if strings.Contains(err.Error(), "admin-secret") {
		t.Fatal("failed reconciliation exposed a password")
	}
	if _, exists := backend.databases[fixedTemporaryDatabaseName]; !exists {
		t.Fatalf("unreconciled temporary database %q was deleted", fixedTemporaryDatabaseName)
	}
	if len(backend.dropped) != 0 {
		t.Fatalf("failed reconciliation dropped databases %v", backend.dropped)
	}
}

func TestSafeDatabaseErrorPreservesCuratedConstraintWhenSanitizedAgain(t *testing.T) {
	constraint := newSafeDatabaseValidationError("PostgreSQL 13 or newer is required")
	once := safeDatabaseError("preflight temporary database", constraint)
	twice := safeDatabaseError("start integration fixture", once)

	if !strings.Contains(twice.Error(), "PostgreSQL 13 or newer is required") {
		t.Fatalf("re-sanitized constraint = %q", twice.Error())
	}
}

func TestConfigHubTemporaryDatabaseCleansUpAfterConfigurationFailure(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.configureErr = errors.New("query failed with password worker-secret")

	_, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	if err == nil || !strings.Contains(err.Error(), "configure temporary database") {
		t.Fatalf("configuration failure returned error %v", err)
	}
	if strings.Contains(err.Error(), "worker-secret") {
		t.Fatal("configuration failure exposed a password")
	}
	if _, exists := backend.databases[fixedTemporaryDatabaseName]; exists {
		t.Fatalf("partially configured database %q still exists", fixedTemporaryDatabaseName)
	}
	if len(backend.dropped) != 1 || backend.dropped[0] != fixedTemporaryDatabaseName {
		t.Fatalf("partial failure dropped databases %v", backend.dropped)
	}
}

func TestConfigHubTemporaryDatabaseReportsIncompleteCleanupBySafeName(t *testing.T) {
	backend := newMemoryConfigHubBackend()
	backend.configureErr = errors.New("configure failed")
	backend.dropErr = errors.New("drop failed with admin-secret")

	_, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	err = safeDatabaseError("start lifecycle test", fmt.Errorf("normal wrapper: %w", err))
	err = safeDatabaseError("test fixture", err)
	assertCleanupIncompleteDiagnostic(t, err)
	if strings.Contains(err.Error(), "admin-secret") {
		t.Fatal("cleanup failure exposed a password")
	}
	if _, exists := backend.databases[fixedTemporaryDatabaseName]; !exists {
		t.Fatalf("failed cleanup did not retain %q for an exact retry", fixedTemporaryDatabaseName)
	}
}

func TestConfigHubTemporaryDatabaseCloseFailuresRemainDiagnosableAndRetryable(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*Postgres, *memoryConfigHubBackend)
		repair  func(*Postgres, *memoryConfigHubBackend, databaseIdentity)
	}{
		{
			name: "maintenance connection",
			arrange: func(_ *Postgres, backend *memoryConfigHubBackend) {
				backend.openErr = errors.New("dial failed with admin-secret")
			},
			repair: func(_ *Postgres, backend *memoryConfigHubBackend, _ databaseIdentity) {
				backend.openErr = nil
			},
		},
		{
			name: "identity query",
			arrange: func(_ *Postgres, backend *memoryConfigHubBackend) {
				backend.lookupErr = errors.New("query failed with admin-secret")
			},
			repair: func(_ *Postgres, backend *memoryConfigHubBackend, _ databaseIdentity) {
				backend.lookupErr = nil
			},
		},
		{
			name: "identity mismatch",
			arrange: func(_ *Postgres, backend *memoryConfigHubBackend) {
				backend.databases[fixedTemporaryDatabaseName] = databaseIdentity{oid: 999, owner: "different_owner"}
			},
			repair: func(_ *Postgres, backend *memoryConfigHubBackend, original databaseIdentity) {
				backend.databases[fixedTemporaryDatabaseName] = original
			},
		},
		{
			name: "drop",
			arrange: func(_ *Postgres, backend *memoryConfigHubBackend) {
				backend.dropErr = errors.New("drop failed with admin-secret")
			},
			repair: func(_ *Postgres, backend *memoryConfigHubBackend, _ databaseIdentity) {
				backend.dropErr = nil
			},
		},
		{
			name: "post-drop verification",
			arrange: func(_ *Postgres, backend *memoryConfigHubBackend) {
				backend.lookupAfterDropErr = errors.New("verification failed with admin-secret")
			},
			repair: func(_ *Postgres, backend *memoryConfigHubBackend, _ databaseIdentity) {
				backend.lookupAfterDropErr = nil
			},
		},
		{
			name: "private creation identity",
			arrange: func(database *Postgres, _ *memoryConfigHubBackend) {
				database.owned.identity.oid = 0
			},
			repair: func(database *Postgres, _ *memoryConfigHubBackend, original databaseIdentity) {
				database.owned.identity = original
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newMemoryConfigHubBackend()
			database := startMemoryConfigHubDatabase(t, backend)
			original := database.owned.identity
			test.arrange(database, backend)

			err := database.Close(context.Background())
			err = safeDatabaseError("normal fixture wrapper", fmt.Errorf("close wrapper: %w", err))
			err = safeDatabaseError("lifecycle test", err)
			assertCleanupIncompleteDiagnostic(t, err)
			if strings.Contains(err.Error(), "admin-secret") {
				t.Fatal("Close failure exposed a password")
			}

			test.repair(database, backend, original)
			if retryErr := database.Close(context.Background()); retryErr != nil {
				t.Fatalf("retry Close after repairing failure: %v", retryErr)
			}
		})
	}
}

type cleanupDiagnostic interface {
	error
	cleanupDatabaseName() string
}

func assertCleanupIncompleteDiagnostic(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "cleanup incomplete") || !strings.Contains(err.Error(), fixedTemporaryDatabaseName) {
		t.Fatalf("incomplete cleanup returned error %v", err)
	}
	var diagnostic cleanupDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic.cleanupDatabaseName() != fixedTemporaryDatabaseName {
		t.Fatalf("cleanup diagnostic did not preserve typed database identity: %v", err)
	}
}

func startMemoryConfigHubDatabase(t *testing.T, backend *memoryConfigHubBackend) *Postgres {
	t.Helper()
	database, err := startConfigHubTemporaryDatabase(context.Background(), validConfigHubSource(), backend.factory, func() (string, error) {
		return fixedTemporaryDatabaseName, nil
	})
	if err != nil {
		t.Fatalf("start memory ConfigHub database: %v", err)
	}
	if database.DatabaseName() != fixedTemporaryDatabaseName {
		t.Fatalf("database name = %q, want %q", database.DatabaseName(), fixedTemporaryDatabaseName)
	}
	return database
}

func validConfigHubSource() config.ConfigHubDatabaseSource {
	return config.ConfigHubDatabaseSource{
		Address:          "db.invalid",
		Port:             5432,
		AdminUsername:    "fixture_admin",
		AdminPassword:    "admin-secret",
		MigratorPassword: "migrator-secret",
		APIPassword:      "api-secret",
		WorkerPassword:   "worker-secret",
	}
}

type memoryConfigHubBackend struct {
	databases               map[string]databaseIdentity
	dropped                 []string
	nextOID                 uint32
	connections             int
	openErr                 error
	preflightErr            error
	lookupErr               error
	lookupAfterCreateErrors map[int]error
	lookupAfterDropErr      error
	createErr               error
	createCommitsOnError    bool
	configureErr            error
	dropErr                 error
}

func newMemoryConfigHubBackend() *memoryConfigHubBackend {
	return &memoryConfigHubBackend{databases: make(map[string]databaseIdentity), nextOID: 101}
}

func (backend *memoryConfigHubBackend) factory(context.Context, config.ConfigHubDatabaseSource) (configHubBackend, error) {
	if backend.openErr != nil {
		return nil, backend.openErr
	}
	backend.connections++
	return &memoryConfigHubConnection{backend: backend, number: backend.connections}, nil
}

type memoryConfigHubConnection struct {
	backend *memoryConfigHubBackend
	number  int
}

func (connection *memoryConfigHubConnection) Preflight(context.Context) (configHubPreflight, error) {
	if connection.backend.preflightErr != nil {
		return configHubPreflight{}, connection.backend.preflightErr
	}
	return configHubPreflight{administrator: "fixture_admin", serverVersion: "PostgreSQL fixture"}, nil
}

func (connection *memoryConfigHubConnection) LookupDatabase(_ context.Context, name string) (databaseIdentity, bool, error) {
	backend := connection.backend
	if backend.lookupErr != nil {
		return databaseIdentity{}, false, backend.lookupErr
	}
	identity, exists := backend.databases[name]
	if exists {
		if err := backend.lookupAfterCreateErrors[connection.number]; err != nil {
			return databaseIdentity{}, false, err
		}
	} else if backend.lookupAfterDropErr != nil && len(backend.dropped) > 0 {
		return databaseIdentity{}, false, backend.lookupAfterDropErr
	}
	return identity, exists, nil
}

func (connection *memoryConfigHubConnection) CreateDatabase(_ context.Context, name string) error {
	backend := connection.backend
	if _, exists := backend.databases[name]; exists {
		return errors.New("duplicate database")
	}
	if backend.createErr != nil && !backend.createCommitsOnError {
		return backend.createErr
	}
	backend.databases[name] = databaseIdentity{oid: backend.nextOID, owner: "fixture_admin"}
	backend.nextOID++
	if backend.createErr != nil {
		return backend.createErr
	}
	return nil
}

func (connection *memoryConfigHubConnection) ConfigureDatabase(context.Context, string, config.ConfigHubDatabaseSource) error {
	return connection.backend.configureErr
}

func (connection *memoryConfigHubConnection) DropDatabase(_ context.Context, name string) error {
	backend := connection.backend
	if backend.dropErr != nil {
		return backend.dropErr
	}
	delete(backend.databases, name)
	backend.dropped = append(backend.dropped, name)
	return nil
}

func (connection *memoryConfigHubConnection) Close(context.Context) error { return nil }
