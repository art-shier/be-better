package testdb

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"dayorder.local/api/internal/config"

	"github.com/jackc/pgx/v5"
)

func TestConfigHubTemporaryDatabaseLifecycle(t *testing.T) {
	if os.Getenv(agentTestDatabaseSourceEnvironment) != configHubDatabaseSource {
		t.Skipf("set %s=%q to run this integration test", agentTestDatabaseSourceEnvironment, configHubDatabaseSource)
	}

	ctx, cancel := context.WithTimeout(context.Background(), postgresStartupWindow)
	defer cancel()
	database, err := StartIsolated(ctx)
	if err != nil {
		t.Fatal(safeDatabaseError("start ConfigHub temporary database", err))
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if cleanupErr := database.Close(cleanupContext); cleanupErr != nil {
			t.Errorf("cleanup incomplete for %s: PostgreSQL operation failed", database.DatabaseName())
		}
	})
	t.Logf("created temporary PostgreSQL database %s on %s", database.DatabaseName(), database.serverVersion)

	roles := []struct {
		name string
		dsn  string
	}{
		{name: "dayorder_migrator", dsn: database.MigrationURL},
		{name: "dayorder_api", dsn: database.APIURL},
		{name: "dayorder_worker", dsn: database.WorkerURL},
	}
	for _, role := range roles {
		parsed, parseErr := url.Parse(role.dsn)
		if parseErr != nil {
			t.Fatalf("parse %s URL: PostgreSQL operation failed", role.name)
		}
		if parsed.Path != "/"+database.DatabaseName() || parsed.Query().Get("sslmode") != "require" {
			t.Fatalf("%s URL does not target the owned TLS database", role.name)
		}

		connection, connectErr := pgx.Connect(ctx, role.dsn)
		if connectErr != nil {
			t.Fatal(safeDatabaseError("connect as "+role.name, connectErr))
		}
		var currentUser string
		var tls, login, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
		queryErr := connection.QueryRow(ctx, `
SELECT current_user, ssl.ssl, role.rolcanlogin, role.rolinherit, role.rolsuper, role.rolcreatedb,
       role.rolcreaterole, role.rolreplication, role.rolbypassrls
FROM pg_catalog.pg_roles AS role
JOIN pg_catalog.pg_stat_ssl AS ssl ON ssl.pid = pg_backend_pid()
WHERE role.rolname = current_user`).Scan(
			&currentUser, &tls, &login, &inherit, &superuser, &createDatabase, &createRole, &replication, &bypassRLS,
		)
		closeErr := connection.Close(context.Background())
		if queryErr != nil {
			t.Fatal(safeDatabaseError("verify "+role.name+" connection", queryErr))
		}
		if closeErr != nil {
			t.Fatal(safeDatabaseError("close "+role.name+" connection", closeErr))
		}
		if currentUser != role.name || !tls || !login || inherit || superuser || createDatabase || createRole || replication || bypassRLS {
			t.Fatalf("%s connection did not use TLS as the expected restricted role", role.name)
		}
	}

	migrator, err := pgx.Connect(ctx, database.MigrationURL)
	if err != nil {
		t.Fatal(safeDatabaseError("connect as migrator for schema verification", err))
	}
	var schemaOwner string
	err = migrator.QueryRow(ctx, `
SELECT owner.rolname
FROM pg_catalog.pg_namespace AS namespace
JOIN pg_catalog.pg_roles AS owner ON owner.oid = namespace.nspowner
WHERE namespace.nspname = 'dayorder'`).Scan(&schemaOwner)
	closeErr := migrator.Close(context.Background())
	if err != nil {
		t.Fatal(safeDatabaseError("verify migrator schema ownership", err))
	}
	if closeErr != nil {
		t.Fatal(safeDatabaseError("close migrator schema verification connection", closeErr))
	}
	if schemaOwner != "dayorder_migrator" {
		t.Fatalf("dayorder schema owner = %q, want dayorder_migrator", schemaOwner)
	}

	if err = database.Close(ctx); err != nil {
		t.Fatal(safeDatabaseError("drop ConfigHub temporary database "+database.DatabaseName(), err))
	}
	source, loadErr := config.LoadConfigHubDatabaseSource(os.LookupEnv)
	if loadErr != nil {
		t.Fatal(safeDatabaseError("load ConfigHub source for cleanup verification", loadErr))
	}
	maintenance, connectErr := pgx.Connect(ctx, source.AdminURL("postgres"))
	if connectErr != nil {
		t.Fatal(safeDatabaseError("connect for cleanup catalog verification", connectErr))
	}
	var exists bool
	queryErr := maintenance.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_database
    WHERE datname = $1
)`, database.DatabaseName()).Scan(&exists)
	closeErr = maintenance.Close(context.Background())
	if queryErr != nil {
		t.Fatal(safeDatabaseError("verify temporary database catalog absence", queryErr))
	}
	if closeErr != nil {
		t.Fatal(safeDatabaseError("close cleanup catalog verification connection", closeErr))
	}
	if exists {
		t.Fatalf("cleanup incomplete for temporary database %s: database remains in pg_catalog.pg_database", database.DatabaseName())
	}
	closed = true
	t.Logf("deleted temporary PostgreSQL database %s", database.DatabaseName())
}
