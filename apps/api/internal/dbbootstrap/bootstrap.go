package dbbootstrap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"dayorder.local/api/internal/config"
	dbmigrations "dayorder.local/api/internal/migrations"

	"github.com/jackc/pgx/v5"
)

type Result struct {
	Databases []DatabaseResult
}

type DatabaseResult struct {
	Name    string
	Created bool
	Version uint
}

type sqlStatement struct {
	query     string
	arguments []any
}

type rolePlan struct {
	name             config.DatabaseRole
	password         string
	connectionLimit  int
	statementTimeout string
	idleTimeout      string
}

type administratorCapabilities struct {
	name      string
	superuser bool
}

func Preflight(ctx context.Context, source config.ConfigHubDatabaseSource) error {
	maintenance, err := pgx.Connect(ctx, source.AdminURL("postgres"))
	if err != nil {
		return safeDatabaseError("connect to PostgreSQL maintenance database", err)
	}
	defer func() { _ = maintenance.Close(context.Background()) }()

	administrator, err := inspectAdministrator(ctx, maintenance)
	if err != nil {
		return err
	}
	if err = requireTLS(ctx, maintenance); err != nil {
		return err
	}
	if err = inspectExistingRuntimeRoleAdministration(ctx, maintenance, administrator); err != nil {
		return err
	}
	for _, database := range databaseTargets() {
		exists, owner, inspectErr := inspectDatabase(ctx, maintenance, database)
		if inspectErr != nil {
			return inspectErr
		}
		if !exists {
			continue
		}
		if owner != administrator.name {
			return fmt.Errorf("database %s has an ownership conflict", database)
		}
		if inspectErr = inspectExistingSchema(ctx, source, database); inspectErr != nil {
			return inspectErr
		}
	}
	return nil
}

func Run(ctx context.Context, source config.ConfigHubDatabaseSource) (Result, error) {
	if err := Preflight(ctx, source); err != nil {
		return Result{}, fmt.Errorf("database preflight: %w", err)
	}

	maintenance, err := pgx.Connect(ctx, source.AdminURL("postgres"))
	if err != nil {
		return Result{}, safeDatabaseError("connect to PostgreSQL maintenance database", err)
	}
	defer func() { _ = maintenance.Close(context.Background()) }()

	administrator, err := currentUser(ctx, maintenance)
	if err != nil {
		return Result{}, err
	}
	if err = reconcileRoles(ctx, maintenance, source); err != nil {
		return Result{}, err
	}

	result := Result{Databases: make([]DatabaseResult, 0, len(databaseTargets()))}
	for _, database := range databaseTargets() {
		created, ensureErr := ensureDatabase(ctx, maintenance, database, administrator)
		if ensureErr != nil {
			return Result{}, ensureErr
		}
		if ensureErr = configureDatabase(ctx, source, database); ensureErr != nil {
			return Result{}, ensureErr
		}

		environment, environmentErr := environmentForDatabase(database)
		if environmentErr != nil {
			return Result{}, environmentErr
		}
		migrationURL, roleErr := source.RoleURL(environment, config.DatabaseRoleMigrator)
		if roleErr != nil {
			return Result{}, roleErr
		}
		if migrationErr := dbmigrations.Up(migrationURL); migrationErr != nil {
			return Result{}, safeDatabaseError("apply migrations to database "+database, migrationErr)
		}
		if migrationErr := dbmigrations.RequireCurrent(migrationURL); migrationErr != nil {
			return Result{}, safeDatabaseError("verify migrations for database "+database, migrationErr)
		}
		version, dirty, exists, migrationErr := dbmigrations.CurrentVersion(migrationURL)
		if migrationErr != nil || !exists || dirty {
			return Result{}, safeDatabaseError("read migration version for database "+database, migrationErr)
		}
		if verifyErr := verifyRuntimeConnections(ctx, source, environment, database); verifyErr != nil {
			return Result{}, verifyErr
		}

		result.Databases = append(result.Databases, DatabaseResult{Name: database, Created: created, Version: version})
	}
	return result, nil
}

func databaseTargets() []string {
	return []string{"dayorder-test", "dayorder"}
}

func rolePlans(source config.ConfigHubDatabaseSource) []rolePlan {
	return []rolePlan{
		{name: config.DatabaseRoleMigrator, password: source.MigratorPassword, connectionLimit: 3, statementTimeout: "10min"},
		{name: config.DatabaseRoleAPI, password: source.APIPassword, connectionLimit: 25, statementTimeout: "5s", idleTimeout: "10s"},
		{name: config.DatabaseRoleWorker, password: source.WorkerPassword, connectionLimit: 10, statementTimeout: "30s", idleTimeout: "10s"},
	}
}

func roleStatements(plan rolePlan) []sqlStatement {
	role := pgx.Identifier{string(plan.name)}.Sanitize()
	attributes := fullRoleAttributes(plan)
	restrictedAttributes := restrictedAdministratorRoleAttributes(plan)
	reconcile := fmt.Sprintf(`DO $dayorder_role$
DECLARE
    administrator_is_superuser BOOLEAN;
    target RECORD;
BEGIN
    SELECT role.rolsuper
    INTO administrator_is_superuser
    FROM pg_catalog.pg_roles AS role
    WHERE role.rolname = current_user;

    SELECT role.rolsuper, role.rolreplication, role.rolbypassrls
    INTO target
    FROM pg_catalog.pg_roles AS role
    WHERE role.rolname = '%s';

    IF FOUND THEN
        IF administrator_is_superuser THEN
            EXECUTE format('ALTER ROLE %s WITH %s PASSWORD %%L', pg_catalog.current_setting('dayorder.bootstrap_password'));
        ELSE
            IF target.rolsuper OR target.rolreplication OR target.rolbypassrls THEN
                RAISE EXCEPTION USING ERRCODE = '42501', MESSAGE = 'runtime role requires superuser reconciliation';
            END IF;
            IF NOT EXISTS (
                SELECT 1
                FROM pg_catalog.pg_auth_members AS membership
                JOIN pg_catalog.pg_roles AS member_role ON member_role.oid = membership.member
                WHERE membership.roleid = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = '%s')
                  AND member_role.rolname = current_user
                  AND membership.admin_option
            ) THEN
                RAISE EXCEPTION USING ERRCODE = '42501', MESSAGE = 'administrator lacks ADMIN OPTION for runtime role';
            END IF;
            EXECUTE format('ALTER ROLE %s WITH %s PASSWORD %%L', pg_catalog.current_setting('dayorder.bootstrap_password'));
        END IF;
    ELSE
        EXECUTE format('CREATE ROLE %s WITH %s PASSWORD %%L', pg_catalog.current_setting('dayorder.bootstrap_password'));
    END IF;
END
$dayorder_role$`, string(plan.name), role, attributes, string(plan.name), role, restrictedAttributes, role, attributes)

	statements := []sqlStatement{
		{query: "SELECT pg_catalog.set_config('dayorder.bootstrap_password', $1, true)", arguments: []any{plan.password}},
		{query: reconcile},
		{query: fmt.Sprintf("ALTER ROLE %s SET timezone = 'UTC'", role)},
		{query: fmt.Sprintf("ALTER ROLE %s SET statement_timeout = '%s'", role, plan.statementTimeout)},
	}
	if plan.idleTimeout == "" {
		statements = append(statements, sqlStatement{query: fmt.Sprintf("ALTER ROLE %s RESET idle_in_transaction_session_timeout", role)})
	} else {
		statements = append(statements, sqlStatement{query: fmt.Sprintf("ALTER ROLE %s SET idle_in_transaction_session_timeout = '%s'", role, plan.idleTimeout)})
	}
	return append(statements, sqlStatement{query: "SELECT pg_catalog.set_config('dayorder.bootstrap_password', '', true)"})
}

func fullRoleAttributes(plan rolePlan) string {
	return "LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT " + strconv.Itoa(plan.connectionLimit)
}

func restrictedAdministratorRoleAttributes(plan rolePlan) string {
	return "LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT " + strconv.Itoa(plan.connectionLimit)
}

func createDatabaseStatement(database string) string {
	return "CREATE DATABASE " + pgx.Identifier{database}.Sanitize()
}

func databaseAccessStatements(database string) []sqlStatement {
	identifier := pgx.Identifier{database}.Sanitize()
	return []sqlStatement{
		{query: fmt.Sprintf("REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM PUBLIC", identifier)},
		{query: fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO dayorder_migrator, dayorder_api, dayorder_worker", identifier)},
		{query: fmt.Sprintf("GRANT CREATE ON DATABASE %s TO dayorder_migrator", identifier)},
		{query: fmt.Sprintf("REVOKE TEMPORARY ON DATABASE %s FROM dayorder_migrator", identifier)},
		{query: fmt.Sprintf("REVOKE CREATE, TEMPORARY ON DATABASE %s FROM dayorder_api, dayorder_worker", identifier)},
	}
}

func administratorSchemaStatements() []sqlStatement {
	return []sqlStatement{{query: "REVOKE CREATE ON SCHEMA public FROM PUBLIC"}}
}

func migratorSchemaStatements(schemaExists bool) []sqlStatement {
	statements := make([]sqlStatement, 0, 3)
	if !schemaExists {
		statements = append(statements, sqlStatement{query: "CREATE SCHEMA dayorder AUTHORIZATION dayorder_migrator"})
	}
	return append(statements,
		sqlStatement{query: "REVOKE ALL ON SCHEMA dayorder FROM PUBLIC"},
		sqlStatement{query: "GRANT USAGE, CREATE ON SCHEMA dayorder TO dayorder_migrator"},
	)
}

func inspectAdministrator(ctx context.Context, connection *pgx.Conn) (administratorCapabilities, error) {
	var name string
	var superuser, createDatabase, createRole bool
	err := connection.QueryRow(ctx, `
SELECT r.rolname, r.rolsuper, r.rolcreatedb, r.rolcreaterole
FROM pg_catalog.pg_roles AS r
	WHERE r.rolname = current_user`).Scan(&name, &superuser, &createDatabase, &createRole)
	if err != nil {
		return administratorCapabilities{}, safeDatabaseError("inspect PostgreSQL administrator capabilities", err)
	}
	if !superuser && (!createDatabase || !createRole) {
		return administratorCapabilities{}, errors.New("PostgreSQL administrator must be able to create databases and roles")
	}
	return administratorCapabilities{name: name, superuser: superuser}, nil
}

func inspectExistingRuntimeRoleAdministration(ctx context.Context, connection *pgx.Conn, administrator administratorCapabilities) error {
	if administrator.superuser {
		return nil
	}
	for _, role := range []config.DatabaseRole{config.DatabaseRoleMigrator, config.DatabaseRoleAPI, config.DatabaseRoleWorker} {
		var superuser, replication, bypassRLS, adminOption bool
		err := connection.QueryRow(ctx, `
SELECT
    target.rolsuper,
    target.rolreplication,
    target.rolbypassrls,
    coalesce(pg_catalog.bool_or(membership.admin_option), false)
FROM pg_catalog.pg_roles AS target
LEFT JOIN pg_catalog.pg_auth_members AS membership
    ON membership.roleid = target.oid
   AND membership.member = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = current_user)
WHERE target.rolname = $1
GROUP BY target.rolsuper, target.rolreplication, target.rolbypassrls`, string(role)).Scan(
			&superuser, &replication, &bypassRLS, &adminOption,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return safeDatabaseError("inspect existing runtime role "+string(role), err)
		}
		if err = validateExistingRuntimeRoleAdministration(role, superuser, replication, bypassRLS, adminOption); err != nil {
			return err
		}
	}
	return nil
}

func validateExistingRuntimeRoleAdministration(role config.DatabaseRole, superuser, replication, bypassRLS, adminOption bool) error {
	if superuser || replication || bypassRLS {
		return fmt.Errorf("existing runtime role %s requires a superuser to reconcile", role)
	}
	if !adminOption {
		return fmt.Errorf("PostgreSQL administrator requires ADMIN OPTION for existing runtime role %s", role)
	}
	return nil
}

func requireTLS(ctx context.Context, connection *pgx.Conn) error {
	var enabled bool
	err := connection.QueryRow(ctx, `
SELECT ssl
FROM pg_catalog.pg_stat_ssl
WHERE pid = pg_backend_pid()`).Scan(&enabled)
	if err != nil {
		return safeDatabaseError("verify PostgreSQL TLS", err)
	}
	if !enabled {
		return errors.New("PostgreSQL connection is not using TLS")
	}
	return nil
}

func inspectDatabase(ctx context.Context, connection *pgx.Conn, database string) (bool, string, error) {
	var owner string
	err := connection.QueryRow(ctx, `
SELECT owner.rolname
FROM pg_catalog.pg_database AS database
JOIN pg_catalog.pg_roles AS owner ON owner.oid = database.datdba
WHERE database.datname = $1`, database).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", safeDatabaseError("inspect database "+database, err)
	}
	return true, owner, nil
}

func inspectExistingSchema(ctx context.Context, source config.ConfigHubDatabaseSource, database string) error {
	connection, err := pgx.Connect(ctx, source.AdminURL(database))
	if err != nil {
		return safeDatabaseError("connect to existing database "+database, err)
	}
	defer func() { _ = connection.Close(context.Background()) }()

	owner, exists, err := inspectSchemaOwner(ctx, connection)
	if err != nil {
		return err
	}
	if exists && owner != string(config.DatabaseRoleMigrator) {
		return fmt.Errorf("database %s has a dayorder schema ownership conflict", database)
	}
	return nil
}

func inspectSchemaOwner(ctx context.Context, connection *pgx.Conn) (string, bool, error) {
	var owner string
	err := connection.QueryRow(ctx, `
SELECT owner.rolname
FROM pg_catalog.pg_namespace AS namespace
JOIN pg_catalog.pg_roles AS owner ON owner.oid = namespace.nspowner
WHERE namespace.nspname = 'dayorder'`).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, safeDatabaseError("inspect dayorder schema owner", err)
	}
	return owner, true, nil
}

func currentUser(ctx context.Context, connection *pgx.Conn) (string, error) {
	var name string
	if err := connection.QueryRow(ctx, "SELECT current_user").Scan(&name); err != nil {
		return "", safeDatabaseError("read PostgreSQL administrator role", err)
	}
	return name, nil
}

func reconcileRoles(ctx context.Context, connection *pgx.Conn, source config.ConfigHubDatabaseSource) error {
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return safeDatabaseError("begin database role reconciliation", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	for _, plan := range rolePlans(source) {
		for _, statement := range roleStatements(plan) {
			if _, err = transaction.Exec(ctx, statement.query, statement.arguments...); err != nil {
				return safeDatabaseError("reconcile database role "+string(plan.name), err)
			}
		}
	}
	if err = transaction.Commit(ctx); err != nil {
		return safeDatabaseError("commit database role reconciliation", err)
	}
	return nil
}

func ensureDatabase(ctx context.Context, connection *pgx.Conn, database, administrator string) (bool, error) {
	exists, owner, err := inspectDatabase(ctx, connection, database)
	if err != nil {
		return false, err
	}
	if exists {
		if owner != administrator {
			return false, fmt.Errorf("database %s has an ownership conflict", database)
		}
		return false, nil
	}
	if _, err = connection.Exec(ctx, createDatabaseStatement(database)); err != nil {
		return false, safeDatabaseError("create database "+database, err)
	}
	return true, nil
}

func configureDatabase(ctx context.Context, source config.ConfigHubDatabaseSource, database string) error {
	administrator, err := pgx.Connect(ctx, source.AdminURL(database))
	if err != nil {
		return safeDatabaseError("connect to database "+database, err)
	}
	defer func() { _ = administrator.Close(context.Background()) }()

	for _, statement := range databaseAccessStatements(database) {
		if _, err = administrator.Exec(ctx, statement.query, statement.arguments...); err != nil {
			return safeDatabaseError("configure database access for "+database, err)
		}
	}
	for _, statement := range administratorSchemaStatements() {
		if _, err = administrator.Exec(ctx, statement.query, statement.arguments...); err != nil {
			return safeDatabaseError("configure public schema in database "+database, err)
		}
	}

	owner, exists, err := inspectSchemaOwner(ctx, administrator)
	if err != nil {
		return err
	}
	if exists && owner != string(config.DatabaseRoleMigrator) {
		return fmt.Errorf("database %s has a dayorder schema ownership conflict", database)
	}
	environment, err := environmentForDatabase(database)
	if err != nil {
		return err
	}
	migratorURL, err := source.RoleURL(environment, config.DatabaseRoleMigrator)
	if err != nil {
		return err
	}
	migrator, err := pgx.Connect(ctx, migratorURL)
	if err != nil {
		return safeDatabaseError("connect to database "+database+" as "+string(config.DatabaseRoleMigrator), err)
	}
	defer func() { _ = migrator.Close(context.Background()) }()
	for _, statement := range migratorSchemaStatements(exists) {
		if _, err = migrator.Exec(ctx, statement.query, statement.arguments...); err != nil {
			return safeDatabaseError("configure dayorder schema in database "+database, err)
		}
	}
	return nil
}

func verifyRuntimeConnections(ctx context.Context, source config.ConfigHubDatabaseSource, environment config.Environment, database string) error {
	for _, role := range []config.DatabaseRole{config.DatabaseRoleAPI, config.DatabaseRoleWorker} {
		databaseURL, err := source.RoleURL(environment, role)
		if err != nil {
			return err
		}
		connection, err := pgx.Connect(ctx, databaseURL)
		if err != nil {
			return safeDatabaseError("connect to database "+database+" as "+string(role), err)
		}
		var one int
		queryErr := connection.QueryRow(ctx, "SELECT 1").Scan(&one)
		closeErr := connection.Close(context.Background())
		if queryErr != nil || one != 1 {
			return safeDatabaseError("verify database "+database+" access for "+string(role), queryErr)
		}
		if closeErr != nil {
			return safeDatabaseError("close database "+database+" connection for "+string(role), closeErr)
		}
	}
	return nil
}

func environmentForDatabase(database string) (config.Environment, error) {
	switch database {
	case "dayorder-test":
		return config.Development, nil
	case "dayorder":
		return config.Production, nil
	default:
		return "", errors.New("unsupported Bootstrap database target")
	}
}

func safeDatabaseError(operation string, _ error) error {
	return fmt.Errorf("%s: PostgreSQL operation failed", strings.TrimSpace(operation))
}
