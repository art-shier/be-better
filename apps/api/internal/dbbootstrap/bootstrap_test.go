package dbbootstrap

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"dayorder.local/api/internal/config"
)

func TestTargetsKeepAcceptanceBeforeProduction(t *testing.T) {
	got := databaseTargets()
	if len(got) != 2 || got[0] != "dayorder-test" || got[1] != "dayorder" {
		t.Fatalf("database targets = %v", got)
	}
}

func TestRolePlansContainOnlyRestrictedRuntimeRoles(t *testing.T) {
	source := bootstrapTestSource()
	plans := rolePlans(source)
	want := []struct {
		name             config.DatabaseRole
		password         string
		connectionLimit  int
		statementTimeout string
		idleTimeout      string
	}{
		{config.DatabaseRoleMigrator, source.MigratorPassword, 3, "10min", ""},
		{config.DatabaseRoleAPI, source.APIPassword, 25, "5s", "10s"},
		{config.DatabaseRoleWorker, source.WorkerPassword, 10, "30s", "10s"},
	}
	if len(plans) != len(want) {
		t.Fatalf("role plans = %d, want %d", len(plans), len(want))
	}
	for index, expected := range want {
		plan := plans[index]
		if plan.name != expected.name || plan.password != expected.password || plan.connectionLimit != expected.connectionLimit || plan.statementTimeout != expected.statementTimeout || plan.idleTimeout != expected.idleTimeout {
			t.Fatalf("role plan %d has unexpected non-secret settings: name=%q limit=%d statement=%q idle=%q", index, plan.name, plan.connectionLimit, plan.statementTimeout, plan.idleTimeout)
		}
	}
}

func TestRoleStatementsBindPasswordsAndReconcileSecuritySettings(t *testing.T) {
	for _, plan := range rolePlans(bootstrapTestSource()) {
		t.Run(string(plan.name), func(t *testing.T) {
			statements := roleStatements(plan)
			if len(statements) < 4 {
				t.Fatalf("role statements = %d, want at least 4", len(statements))
			}

			joined := joinQueries(statements)
			for _, expected := range []string{
				string(plan.name),
				"LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
				"CONNECTION LIMIT " + strconv.Itoa(plan.connectionLimit),
				"format(",
				"%L",
				"current_setting('dayorder.bootstrap_password')",
				"SET timezone = 'UTC'",
				"SET statement_timeout = '" + plan.statementTimeout + "'",
			} {
				if !strings.Contains(joined, expected) {
					t.Fatalf("role SQL for %s is missing %q", plan.name, expected)
				}
			}
			if plan.idleTimeout != "" && !strings.Contains(joined, "SET idle_in_transaction_session_timeout = '"+plan.idleTimeout+"'") {
				t.Fatalf("role SQL for %s is missing its idle transaction timeout", plan.name)
			}
			if plan.idleTimeout == "" && !strings.Contains(joined, "RESET idle_in_transaction_session_timeout") {
				t.Fatalf("role SQL for %s does not clear an unexpected idle transaction timeout", plan.name)
			}

			boundPassword := false
			for _, statement := range statements {
				if strings.Contains(statement.query, plan.password) {
					t.Fatal("role password appeared in generated SQL")
				}
				for _, argument := range statement.arguments {
					if value, ok := argument.(string); ok && value == plan.password {
						boundPassword = true
					}
				}
			}
			if !boundPassword {
				t.Fatal("role password was not supplied as a protocol argument")
			}
		})
	}
}

func TestBootstrapGeneratedSQLContainsNoDestructiveOperations(t *testing.T) {
	statements := make([]sqlStatement, 0)
	for _, plan := range rolePlans(bootstrapTestSource()) {
		statements = append(statements, roleStatements(plan)...)
	}
	for _, database := range databaseTargets() {
		statements = append(statements, sqlStatement{query: createDatabaseStatement(database)})
		statements = append(statements, databaseAccessStatements(database)...)
	}
	statements = append(statements, administratorSchemaStatements()...)
	statements = append(statements, migratorSchemaStatements(false)...)

	forbidden := regexp.MustCompile(`(?i)\b(DROP\s+(DATABASE|SCHEMA|ROLE)|TRUNCATE)\b`)
	for _, statement := range statements {
		if forbidden.MatchString(statement.query) {
			t.Fatalf("Bootstrap generated a destructive SQL operation: %s", forbidden.FindString(statement.query))
		}
	}
}

func TestDatabaseStatementsUseOnlyFixedQuotedTargetsAndRoles(t *testing.T) {
	if got := createDatabaseStatement("dayorder-test"); got != `CREATE DATABASE "dayorder-test"` {
		t.Fatalf("acceptance database statement = %q", got)
	}
	if got := createDatabaseStatement("dayorder"); got != `CREATE DATABASE "dayorder"` {
		t.Fatalf("production database statement = %q", got)
	}
	for _, database := range databaseTargets() {
		joined := joinQueries(databaseAccessStatements(database))
		for _, role := range []config.DatabaseRole{config.DatabaseRoleMigrator, config.DatabaseRoleAPI, config.DatabaseRoleWorker} {
			if !strings.Contains(joined, string(role)) {
				t.Fatalf("database access SQL for %s is missing role %s", database, role)
			}
		}
		if strings.Contains(joined, "dayorder_backup") || strings.Contains(joined, "dayorder_monitor") {
			t.Fatalf("database access SQL for %s included an out-of-scope role", database)
		}
	}
}

func TestSchemaStatementsKeepOwnerOperationsOnMigratorConnection(t *testing.T) {
	administratorSQL := joinQueries(administratorSchemaStatements())
	if !strings.Contains(administratorSQL, "REVOKE CREATE ON SCHEMA public FROM PUBLIC") {
		t.Fatal("administrator schema SQL does not secure the public schema")
	}
	if strings.Contains(administratorSQL, "SCHEMA dayorder") {
		t.Fatal("administrator schema SQL attempts an owner-only dayorder schema operation")
	}

	newSchemaSQL := joinQueries(migratorSchemaStatements(false))
	for _, expected := range []string{
		"CREATE SCHEMA dayorder AUTHORIZATION dayorder_migrator",
		"REVOKE ALL ON SCHEMA dayorder FROM PUBLIC",
		"GRANT USAGE, CREATE ON SCHEMA dayorder TO dayorder_migrator",
	} {
		if !strings.Contains(newSchemaSQL, expected) {
			t.Fatalf("new-schema Migrator SQL is missing %q", expected)
		}
	}
	if existingSchemaSQL := joinQueries(migratorSchemaStatements(true)); strings.Contains(existingSchemaSQL, "CREATE SCHEMA") {
		t.Fatal("existing-schema Migrator SQL attempts to recreate the schema")
	}
}

func TestDatabaseErrorsNeverEchoDriverDetails(t *testing.T) {
	const secret = "must-not-appear-in-errors"
	err := safeDatabaseError("reconcile database role dayorder_api", errors.New(secret))
	if err == nil || !strings.Contains(err.Error(), "reconcile database role dayorder_api") {
		t.Fatalf("sanitized error did not retain its operation: %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%+v", err), secret) {
		t.Fatal("sanitized database error exposed driver details")
	}
}

func bootstrapTestSource() config.ConfigHubDatabaseSource {
	return config.ConfigHubDatabaseSource{
		Address:          "db.example.internal",
		Port:             5432,
		AdminUsername:    "bootstrap-admin",
		AdminPassword:    "admin-secret-for-test",
		MigratorPassword: "migrator-secret-for-test",
		APIPassword:      "api-secret-for-test",
		WorkerPassword:   "worker-secret-for-test",
	}
}

func joinQueries(statements []sqlStatement) string {
	queries := make([]string, 0, len(statements))
	for _, statement := range statements {
		queries = append(queries, statement.query)
	}
	return strings.Join(queries, "\n")
}
