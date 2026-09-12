package testdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"dayorder.local/api/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	temporaryDatabasePrefix = "dayorder_agent_it_"
	partialCleanupWindow    = 30 * time.Second
)

type databaseIdentity struct {
	oid   uint32
	owner string
}

type configHubPreflight struct {
	administrator string
	serverVersion string
}

type configHubBackend interface {
	Preflight(context.Context) (configHubPreflight, error)
	LookupDatabase(context.Context, string) (databaseIdentity, bool, error)
	CreateDatabase(context.Context, string) error
	ConfigureDatabase(context.Context, string, config.ConfigHubDatabaseSource) error
	DropDatabase(context.Context, string) error
	Close(context.Context) error
}

type configHubBackendFactory func(context.Context, config.ConfigHubDatabaseSource) (configHubBackend, error)

type ownedConfigHubDatabase struct {
	name     string
	identity databaseIdentity
	source   config.ConfigHubDatabaseSource
	factory  configHubBackendFactory
}

type safeDatabaseValidationError struct {
	message string
}

func (err safeDatabaseValidationError) Error() string {
	return err.message
}

func (safeDatabaseValidationError) safeDatabaseDiagnostic() {}

type safeDatabaseDiagnostic interface {
	error
	safeDatabaseDiagnostic()
}

type safeDatabaseContextError struct {
	operation  string
	diagnostic safeDatabaseDiagnostic
}

func (err safeDatabaseContextError) Error() string {
	return err.operation + ": " + err.diagnostic.Error()
}

func (err safeDatabaseContextError) Unwrap() error {
	return err.diagnostic
}

func (safeDatabaseContextError) safeDatabaseDiagnostic() {}

type cleanupIncompleteError struct {
	name   string
	detail string
}

func (err cleanupIncompleteError) Error() string {
	return fmt.Sprintf("cleanup incomplete for temporary database %s: %s", err.name, err.detail)
}

func (err cleanupIncompleteError) cleanupDatabaseName() string {
	return err.name
}

func (cleanupIncompleteError) safeDatabaseDiagnostic() {}

func newSafeDatabaseValidationError(message string) error {
	return safeDatabaseValidationError{message: strings.TrimSpace(message)}
}

func newCleanupIncompleteError(name, detail string) error {
	if !validTemporaryDatabaseName(name) {
		name = "<invalid-owned-name>"
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "PostgreSQL operation failed"
	}
	return cleanupIncompleteError{name: name, detail: detail}
}

func startConfigHubFromEnvironment(ctx context.Context) (*Postgres, error) {
	source, err := config.LoadConfigHubDatabaseSource(os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("load ConfigHub database source: %w", err)
	}
	return startConfigHubTemporaryDatabase(ctx, source, openPGXConfigHubBackend, newTemporaryDatabaseName)
}

func startConfigHubTemporaryDatabase(
	ctx context.Context,
	source config.ConfigHubDatabaseSource,
	factory configHubBackendFactory,
	generateName func() (string, error),
) (*Postgres, error) {
	name, err := generateName()
	if err != nil || !validTemporaryDatabaseName(name) {
		return nil, errors.New("generate owned temporary database name: operation failed")
	}

	backend, err := factory(ctx, source)
	if err != nil {
		return nil, safeDatabaseError("connect to PostgreSQL maintenance database", err)
	}
	defer func() { _ = backend.Close(context.Background()) }()

	preflight, err := backend.Preflight(ctx)
	if err != nil {
		return nil, safeDatabaseError("preflight PostgreSQL temporary database", err)
	}
	if strings.TrimSpace(preflight.administrator) == "" {
		return nil, errors.New("preflight PostgreSQL temporary database: administrator identity is unavailable")
	}

	_, exists, err := backend.LookupDatabase(ctx, name)
	if err != nil {
		return nil, safeDatabaseError("inspect temporary database "+name, err)
	}
	if exists {
		return nil, fmt.Errorf("temporary database %s already exists; refusing ownership", name)
	}
	if err = backend.CreateDatabase(ctx, name); err != nil {
		return nil, newCleanupIncompleteError(
			name,
			"CREATE outcome was not acknowledged; ownership was not adopted and no database was dropped",
		)
	}

	identity, exists, err := backend.LookupDatabase(ctx, name)
	if err != nil || !exists {
		identity, err = reconcileAcknowledgedCreation(source, factory, name, preflight.administrator)
		if err != nil {
			return nil, err
		}
		exists = true
	}
	if !exists || identity.oid == 0 || identity.owner != preflight.administrator {
		return nil, newCleanupIncompleteError(
			name,
			"acknowledged CREATE identity did not match the administrator; ownership was not adopted and no database was dropped",
		)
	}

	owned := &ownedConfigHubDatabase{name: name, identity: identity, source: source, factory: factory}
	database := &Postgres{
		AdminURL:      source.AdminURL(name),
		MigrationURL:  configHubRoleURL(source, name, config.DatabaseRoleMigrator),
		APIURL:        configHubRoleURL(source, name, config.DatabaseRoleAPI),
		WorkerURL:     configHubRoleURL(source, name, config.DatabaseRoleWorker),
		databaseName:  name,
		serverVersion: preflight.serverVersion,
		owned:         owned,
	}

	if err = backend.ConfigureDatabase(ctx, name, source); err != nil {
		startErr := safeDatabaseError("configure temporary database "+name, err)
		cleanupContext, cancel := context.WithTimeout(context.Background(), partialCleanupWindow)
		cleanupErr := database.Close(cleanupContext)
		cancel()
		if cleanupErr != nil {
			return nil, newCleanupIncompleteError(name, startErr.Error()+"; "+safeCleanupDetail(cleanupErr))
		}
		return nil, startErr
	}
	return database, nil
}

func reconcileAcknowledgedCreation(
	source config.ConfigHubDatabaseSource,
	factory configHubBackendFactory,
	name string,
	administrator string,
) (databaseIdentity, error) {
	reconcileContext, cancel := context.WithTimeout(context.Background(), partialCleanupWindow)
	defer cancel()

	backend, err := factory(reconcileContext, source)
	if err != nil {
		return databaseIdentity{}, cleanupIncompleteDatabaseError(name, "reconnect for acknowledged CREATE reconciliation", err)
	}
	defer func() { _ = backend.Close(reconcileContext) }()

	identity, exists, err := backend.LookupDatabase(reconcileContext, name)
	if err != nil {
		return databaseIdentity{}, cleanupIncompleteDatabaseError(name, "reconcile acknowledged CREATE identity", err)
	}
	if !exists {
		return databaseIdentity{}, newSafeDatabaseValidationError(
			fmt.Sprintf("temporary database %s was absent after acknowledged CREATE reconciliation", name),
		)
	}
	if identity.oid == 0 || identity.owner != administrator {
		return databaseIdentity{}, newCleanupIncompleteError(
			name,
			"acknowledged CREATE identity did not match the administrator; ownership was not adopted and no database was dropped",
		)
	}
	return identity, nil
}

func newTemporaryDatabaseName() (string, error) {
	identifier, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return temporaryDatabasePrefix + strings.ReplaceAll(identifier.String(), "-", ""), nil
}

func validTemporaryDatabaseName(name string) bool {
	if len(name) != len(temporaryDatabasePrefix)+32 || !strings.HasPrefix(name, temporaryDatabasePrefix) {
		return false
	}
	for _, character := range name[len(temporaryDatabasePrefix):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func configHubRoleURL(source config.ConfigHubDatabaseSource, database string, role config.DatabaseRole) string {
	switch role {
	case config.DatabaseRoleMigrator:
		return connectionURL(source.AdminURL(database), string(role), source.MigratorPassword, "dayorder")
	case config.DatabaseRoleAPI:
		return connectionURL(source.AdminURL(database), string(role), source.APIPassword)
	case config.DatabaseRoleWorker:
		return connectionURL(source.AdminURL(database), string(role), source.WorkerPassword)
	default:
		panic("unsupported ConfigHub database role")
	}
}

func (owned *ownedConfigHubDatabase) close(ctx context.Context) error {
	if owned == nil {
		return nil
	}
	if !validTemporaryDatabaseName(owned.name) || owned.identity.oid == 0 || strings.TrimSpace(owned.identity.owner) == "" {
		return newCleanupIncompleteError(
			owned.name,
			"private creation identity is invalid; no database was dropped",
		)
	}

	backend, err := owned.factory(ctx, owned.source)
	if err != nil {
		return cleanupIncompleteDatabaseError(owned.name, "connect for temporary database cleanup", err)
	}
	defer func() { _ = backend.Close(context.Background()) }()

	identity, exists, err := backend.LookupDatabase(ctx, owned.name)
	if err != nil {
		return cleanupIncompleteDatabaseError(owned.name, "validate temporary database cleanup identity", err)
	}
	if !exists {
		return nil
	}
	if identity != owned.identity {
		return newCleanupIncompleteError(owned.name, "identity mismatch; no database was dropped")
	}
	if err = backend.DropDatabase(ctx, owned.name); err != nil {
		return cleanupIncompleteDatabaseError(owned.name, "drop temporary database", err)
	}
	_, exists, err = backend.LookupDatabase(ctx, owned.name)
	if err != nil {
		return cleanupIncompleteDatabaseError(owned.name, "verify temporary database cleanup", err)
	}
	if exists {
		return newCleanupIncompleteError(owned.name, "database still exists after DROP")
	}
	return nil
}

func safeDatabaseError(operation string, err error) error {
	operation = strings.TrimSpace(operation)
	var diagnostic safeDatabaseDiagnostic
	if errors.As(err, &diagnostic) {
		return safeDatabaseContextError{operation: operation, diagnostic: diagnostic}
	}
	return newSafeDatabaseValidationError(fmt.Sprintf("%s: PostgreSQL operation failed", operation))
}

func cleanupIncompleteDatabaseError(name, operation string, err error) error {
	return newCleanupIncompleteError(name, safeDatabaseError(operation, err).Error())
}

func safeCleanupDetail(err error) string {
	var diagnostic safeDatabaseDiagnostic
	if errors.As(err, &diagnostic) {
		return diagnostic.Error()
	}
	return "cleanup attempt failed: PostgreSQL operation failed"
}

type pgxConfigHubBackend struct {
	maintenance *pgx.Conn
}

func openPGXConfigHubBackend(ctx context.Context, source config.ConfigHubDatabaseSource) (configHubBackend, error) {
	connection, err := pgx.Connect(ctx, source.AdminURL("postgres"))
	if err != nil {
		return nil, err
	}
	return &pgxConfigHubBackend{maintenance: connection}, nil
}

func (backend *pgxConfigHubBackend) Preflight(ctx context.Context) (configHubPreflight, error) {
	var result configHubPreflight
	var createDatabase, superuser, tls bool
	var serverVersionNumber int
	err := backend.maintenance.QueryRow(ctx, `
SELECT role.rolname, pg_catalog.version(), pg_catalog.current_setting('server_version_num')::integer,
       role.rolcreatedb, role.rolsuper, ssl.ssl
FROM pg_catalog.pg_roles AS role
JOIN pg_catalog.pg_stat_ssl AS ssl ON ssl.pid = pg_backend_pid()
WHERE role.rolname = current_user`).Scan(
		&result.administrator, &result.serverVersion, &serverVersionNumber, &createDatabase, &superuser, &tls,
	)
	if err != nil {
		return configHubPreflight{}, err
	}
	if !tls {
		return configHubPreflight{}, newSafeDatabaseValidationError("maintenance connection is not using TLS")
	}
	if !createDatabase && !superuser {
		return configHubPreflight{}, newSafeDatabaseValidationError("administrator lacks CREATEDB capability")
	}
	if serverVersionNumber < 130000 {
		return configHubPreflight{}, newSafeDatabaseValidationError("PostgreSQL 13 or newer is required for bounded forced cleanup")
	}

	for _, role := range []config.DatabaseRole{config.DatabaseRoleMigrator, config.DatabaseRoleAPI, config.DatabaseRoleWorker} {
		var login, inherit, superuser, roleCreateDatabase, createRole, replication, bypassRLS bool
		err = backend.maintenance.QueryRow(ctx, `
SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
FROM pg_catalog.pg_roles
WHERE rolname = $1`, string(role)).Scan(&login, &inherit, &superuser, &roleCreateDatabase, &createRole, &replication, &bypassRLS)
		if errors.Is(err, pgx.ErrNoRows) {
			return configHubPreflight{}, newSafeDatabaseValidationError(fmt.Sprintf("required runtime role %s does not exist", role))
		}
		if err != nil {
			return configHubPreflight{}, err
		}
		if !login || inherit || superuser || roleCreateDatabase || createRole || replication || bypassRLS {
			return configHubPreflight{}, newSafeDatabaseValidationError(fmt.Sprintf("runtime role %s is not restricted", role))
		}
	}
	return result, nil
}

func (backend *pgxConfigHubBackend) LookupDatabase(ctx context.Context, name string) (databaseIdentity, bool, error) {
	var identity databaseIdentity
	err := backend.maintenance.QueryRow(ctx, `
SELECT database.oid, owner.rolname
FROM pg_catalog.pg_database AS database
JOIN pg_catalog.pg_roles AS owner ON owner.oid = database.datdba
WHERE database.datname = $1`, name).Scan(&identity.oid, &identity.owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return databaseIdentity{}, false, nil
	}
	if err != nil {
		return databaseIdentity{}, false, err
	}
	return identity, true, nil
}

func (backend *pgxConfigHubBackend) CreateDatabase(ctx context.Context, name string) error {
	if !validTemporaryDatabaseName(name) {
		return errors.New("invalid temporary database name")
	}
	_, err := backend.maintenance.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	return err
}

func (backend *pgxConfigHubBackend) ConfigureDatabase(ctx context.Context, name string, source config.ConfigHubDatabaseSource) error {
	if !validTemporaryDatabaseName(name) {
		return errors.New("invalid temporary database name")
	}
	identifier := pgx.Identifier{name}.Sanitize()
	administrator, err := pgx.Connect(ctx, source.AdminURL(name))
	if err != nil {
		return err
	}
	defer func() { _ = administrator.Close(context.Background()) }()

	statements := []string{
		"REVOKE CONNECT, TEMPORARY ON DATABASE " + identifier + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + identifier + " TO dayorder_migrator, dayorder_api, dayorder_worker",
		"GRANT CREATE ON DATABASE " + identifier + " TO dayorder_migrator",
		"REVOKE TEMPORARY ON DATABASE " + identifier + " FROM dayorder_migrator",
		"REVOKE CREATE, TEMPORARY ON DATABASE " + identifier + " FROM dayorder_api, dayorder_worker",
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
	}
	for _, statement := range statements {
		if _, err = administrator.Exec(ctx, statement); err != nil {
			return err
		}
	}

	migrator, err := pgx.Connect(ctx, configHubRoleURL(source, name, config.DatabaseRoleMigrator))
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close(context.Background()) }()
	for _, statement := range []string{
		"CREATE SCHEMA dayorder AUTHORIZATION dayorder_migrator",
		"REVOKE ALL ON SCHEMA dayorder FROM PUBLIC",
		"GRANT USAGE, CREATE ON SCHEMA dayorder TO dayorder_migrator",
	} {
		if _, err = migrator.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (backend *pgxConfigHubBackend) DropDatabase(ctx context.Context, name string) error {
	if !validTemporaryDatabaseName(name) {
		return errors.New("invalid temporary database name")
	}
	_, err := backend.maintenance.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	return err
}

func (backend *pgxConfigHubBackend) Close(ctx context.Context) error {
	return backend.maintenance.Close(ctx)
}
