# ConfigHub PostgreSQL Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect DayOrder to PostgreSQL settings from ConfigHub, safely create and migrate `dayorder-test` and `dayorder`, and prove the local application works against `dayorder-test` without exposing credentials.

**Architecture:** Existing native `DATABASE_URL`, `WORKER_DATABASE_URL`, and `MIGRATION_DATABASE_URL` values remain the highest-priority configuration path. When a native URL is absent, a focused Go resolver consumes the seven ConfigHub fields injected by `confighub run`, builds TLS-only URLs for fixed roles and database names, and scrubs unrelated secrets. A separate idempotent Bootstrap command uses the ConfigHub administrator credentials to preflight capabilities, create the three restricted roles and two databases, apply the existing migrations, and verify role access.

**Tech Stack:** Go 1.25, pgx/v5 5.10, golang-migrate 4.19, PowerShell, Node.js/npm, ConfigHub CLI 0.1.1, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-09-01-confighub-postgresql-bootstrap-design.md`

## Global Constraints

- ConfigHub project/environment is exactly `shier/prod`.
- Required remote keys are `db_address`, `db_port`, `db_username`, `db_password`, `db_migrator_password`, `db_api_password`, and `db_worker_password`.
- Development uses database `dayorder-test`; production uses database `dayorder`.
- ConfigHub-derived PostgreSQL URLs always use `sslmode=require` and `timezone=UTC`; there is no plaintext fallback.
- Runtime roles are exactly `dayorder_migrator`, `dayorder_api`, and `dayorder_worker`.
- No password, Token, full DSN, or raw ConfigHub export may be printed or written to a project file.
- Existing explicit URL configuration remains compatible and takes precedence.
- Bootstrap may create missing objects and reconcile expected roles, but it must never drop a database, schema, table, or role.
- Local business acceptance writes only to `dayorder-test`; `dayorder` receives schema and permission initialization only.
- Bootstrap does not create `dayorder_backup` or `dayorder_monitor`; backup and monitoring remain separate work.
- ConfigHub stores configuration values in plaintext SQLite/backup data, so its existing access controls and backup policy remain part of the trust boundary.
- Redis is out of scope.

---

## File Structure

- `.gitignore`: protects `.confighub.yaml` from accidental commits.
- `scripts/validate-architecture.mjs`: asserts the ConfigHub credential file stays ignored.
- `apps/api/internal/config/database_source.go`: owns ConfigHub field parsing, fixed database selection, URL construction, explicit-URL precedence, and environment scrubbing.
- `apps/api/internal/config/database_source_test.go`: unit coverage for field validation, URL encoding, role isolation, database selection, and scrubbing.
- `apps/api/internal/config/config.go`: resolves the API database URL through the shared resolver.
- `apps/api/internal/config/worker.go`: resolves the Worker database URL through the shared resolver.
- `apps/api/cmd/migrate/main.go`: resolves the Migrator URL through the shared resolver.
- `apps/api/cmd/migrate/main_test.go`: tests flag, native URL, and ConfigHub precedence without opening PostgreSQL.
- `apps/api/internal/dbbootstrap/bootstrap.go`: contains preflight, idempotent role/database/schema creation, migrations, and permission verification.
- `apps/api/internal/dbbootstrap/bootstrap_test.go`: tests fixed targets, safe SQL construction, ordering, and no-drop invariants.
- `apps/api/internal/dbbootstrap/confighub_acceptance_test.go`: opt-in real-database API/role acceptance against `dayorder-test`.
- `apps/api/cmd/bootstrap/main.go`: CLI entry point with read-only `-preflight` and apply modes.
- `apps/api/cmd/bootstrap/main_test.go`: tests option parsing and sanitized result reporting.
- `package.json`: exposes ConfigHub preflight, Bootstrap, migration-check, API, and Worker commands.
- `README.md`: documents ConfigHub prerequisites, commands, rotation order, and security boundaries.

---

### Task 1: Protect the local ConfigHub Machine Token

**Files:**
- Modify: `.gitignore`
- Modify: `scripts/validate-architecture.mjs`

**Interfaces:**
- Consumes: repository-root `.confighub.yaml`.
- Produces: a permanent ignore rule and an architecture check that fails if the rule is removed.

- [ ] **Step 1: Record the failing safety check**

Run:

```powershell
git check-ignore -q -- .confighub.yaml
if ($LASTEXITCODE -eq 0) { throw '.confighub.yaml unexpectedly already ignored' }
```

Expected: the command exits non-zero because `.confighub.yaml` is currently untracked and not ignored.

- [ ] **Step 2: Add an architecture assertion before changing `.gitignore`**

Add this exact check to `scripts/validate-architecture.mjs` alongside the existing repository policy checks:

```js
const gitignore = readText(".gitignore");
if (!gitignore.split(/\r?\n/).includes(".confighub.yaml")) {
  fail(".gitignore must protect the local .confighub.yaml Machine Token file");
}
```

- [ ] **Step 3: Run the architecture check to verify it fails**

Run: `npm run test:architecture`

Expected: FAIL with `.gitignore must protect the local .confighub.yaml Machine Token file`.

- [ ] **Step 4: Add the credential file ignore rule**

Append this exact line next to the `.env` rules in `.gitignore`:

```gitignore
.confighub.yaml
```

- [ ] **Step 5: Verify the safety checks pass**

Run:

```powershell
npm run test:architecture
git check-ignore -v -- .confighub.yaml
git status --short -- .confighub.yaml
```

Expected: architecture PASS; `git check-ignore` names `.gitignore`; `git status` prints nothing for `.confighub.yaml`.

- [ ] **Step 6: Commit the security guard**

```powershell
git add .gitignore scripts/validate-architecture.mjs
git commit -m "chore: protect local ConfigHub credentials"
```

---

### Task 2: Implement the ConfigHub PostgreSQL source resolver

**Files:**
- Create: `apps/api/internal/config/database_source.go`
- Create: `apps/api/internal/config/database_source_test.go`

**Interfaces:**
- Consumes: `LookupFunc`, `Environment`, explicit URL key, and a `DatabaseRole`.
- Produces: `LoadConfigHubDatabaseSource(LookupFunc)`, `ResolveDatabaseURL(LookupFunc, Environment, string, DatabaseRole)`, `DatabaseName(Environment)`, `ConfigHubDatabaseSource.AdminURL(string)`, `ConfigHubDatabaseSource.RoleURL(Environment, DatabaseRole)`, and `ScrubConfigHubDatabaseEnvironment()`.

- [ ] **Step 1: Write failing tests for fixed database and role mapping**

Create table-driven tests with these assertions:

```go
func TestDatabaseNameUsesFixedEnvironmentTargets(t *testing.T) {
	if got, err := DatabaseName(Development); err != nil || got != "dayorder-test" {
		t.Fatalf("development database = %q, %v", got, err)
	}
	if got, err := DatabaseName(Production); err != nil || got != "dayorder" {
		t.Fatalf("production database = %q, %v", got, err)
	}
	if _, err := DatabaseName(Test); err == nil {
		t.Fatal("ConfigHub fallback must not select a database for automated unit tests")
	}
}

func TestRoleURLsUseIndependentPasswordsAndTLS(t *testing.T) {
	source := validConfigHubDatabaseSource()
	apiURL, _ := source.RoleURL(Development, DatabaseRoleAPI)
	workerURL, _ := source.RoleURL(Development, DatabaseRoleWorker)
	if apiURL == workerURL {
		t.Fatal("API and Worker URLs must differ")
	}
	assertURL(t, apiURL, "dayorder_api", "api-secret", "dayorder-test", "require")
	assertURL(t, workerURL, "dayorder_worker", "worker-secret", "dayorder-test", "require")
}
```

Also cover every missing key, non-numeric/out-of-range ports, addresses containing schemes, slashes, whitespace or credentials, empty passwords, native URL priority, URL-escaped usernames/passwords, and rejection of unsupported roles.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./apps/api/internal/config -run 'Test(DatabaseName|RoleURLs|LoadConfigHub|ResolveDatabaseURL)' -count=1`

Expected: FAIL because the types and functions do not exist.

- [ ] **Step 3: Implement focused source and role types**

Use these exact public shapes:

```go
type DatabaseRole string

const (
	DatabaseRoleMigrator DatabaseRole = "dayorder_migrator"
	DatabaseRoleAPI      DatabaseRole = "dayorder_api"
	DatabaseRoleWorker   DatabaseRole = "dayorder_worker"
)

type ConfigHubDatabaseSource struct {
	Address            string
	Port               uint16
	AdminUsername      string
	AdminPassword      string
	MigratorPassword   string
	APIPassword        string
	WorkerPassword     string
}
```

`LoadConfigHubDatabaseSource` must read the seven exact lower-case keys, trim address/port/username, preserve password bytes exactly, and return errors naming only the invalid key.

- [ ] **Step 4: Implement safe URL construction and precedence**

Construct URLs with `net.JoinHostPort`, `url.UserPassword`, and `url.Values` rather than string concatenation:

```go
func buildPostgresURL(address string, port uint16, username, password, database string, searchPath bool) string {
	query := url.Values{"sslmode": {"require"}, "timezone": {"UTC"}}
	if searchPath {
		query.Set("search_path", "dayorder")
	}
	return (&url.URL{
		Scheme: "postgresql",
		User: url.UserPassword(username, password),
		Host: net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10)),
		Path: "/" + database,
		RawQuery: query.Encode(),
	}).String()
}
```

`ResolveDatabaseURL` must return a valid explicit native URL first; only when that key is absent may it load ConfigHub fields. Migrator URLs set `search_path=dayorder`; API and Worker URLs do not.

- [ ] **Step 5: Implement environment scrubbing**

`ScrubConfigHubDatabaseEnvironment` must call `os.Unsetenv` for all seven source keys. Its test sets sentinel values with `t.Setenv`, invokes the function, and verifies `os.LookupEnv` returns false for each key without printing the values.

- [ ] **Step 6: Run focused and package tests**

Run:

```powershell
go test ./apps/api/internal/config -count=1
go vet ./apps/api/internal/config
```

Expected: PASS.

- [ ] **Step 7: Commit the resolver**

```powershell
git add apps/api/internal/config/database_source.go apps/api/internal/config/database_source_test.go
git commit -m "feat(config): resolve PostgreSQL settings from ConfigHub"
```

---

### Task 3: Integrate ConfigHub fallback into API, Worker, and Migrator

**Files:**
- Modify: `apps/api/internal/config/config.go`
- Modify: `apps/api/internal/config/config_test.go`
- Modify: `apps/api/internal/config/worker.go`
- Modify: `apps/api/internal/config/worker_test.go`
- Modify: `apps/api/cmd/migrate/main.go`
- Create: `apps/api/cmd/migrate/main_test.go`

**Interfaces:**
- Consumes: Task 2 `ResolveDatabaseURL` and `ScrubConfigHubDatabaseEnvironment`.
- Produces: API/Worker/Migrator behavior that preserves native URLs and falls back to ConfigHub safely.

- [ ] **Step 1: Add failing API and Worker fallback tests**

Extend the existing loader tests so a lookup containing the seven ConfigHub keys but no native URL succeeds and selects the correct role/database. Add a native URL test that deliberately supplies invalid ConfigHub fields and still succeeds, proving native URL priority.

```go
if got := cfg.Database.URL; !strings.Contains(got, "dayorder_api") || !strings.Contains(got, "/dayorder-test?") {
	t.Fatalf("API ConfigHub URL selected incorrectly: %s", redactURL(got))
}
```

The test failure message must use a test-only `redactURL` helper that removes `url.User` before formatting.

- [ ] **Step 2: Add failing Migrator resolver tests**

Extract a pure helper in `cmd/migrate/main.go`:

```go
func resolveDatabaseURL(flagValue string, lookup config.LookupFunc) (string, error)
```

Test flag priority, native `MIGRATION_DATABASE_URL` priority, ConfigHub fallback to `dayorder_migrator`, and an error when neither source exists.

- [ ] **Step 3: Run focused tests to verify they fail**

Run:

```powershell
go test ./apps/api/internal/config ./apps/api/cmd/migrate -count=1
```

Expected: FAIL on missing fallback integration and `resolveDatabaseURL`.

- [ ] **Step 4: Replace direct URL reads with the shared resolver**

In `LoadFrom`, call:

```go
databaseURL, err := ResolveDatabaseURL(lookup, environment, "DATABASE_URL", DatabaseRoleAPI)
```

In `LoadWorkerFrom`, call the same function with `WORKER_DATABASE_URL` and `DatabaseRoleWorker`. In Migrator, keep `-database-url` highest priority, then resolve `MIGRATION_DATABASE_URL` for the current `DAYORDER_ENV`.

- [ ] **Step 5: Scrub source fields after successful process-level loads**

`Load()` and `LoadWorker()` must scrub only after their `LoadFrom` call succeeds. Migrator must scrub immediately after it has copied the resolved URL. Pure `LoadFrom` tests remain side-effect free.

- [ ] **Step 6: Run configuration and migration tests**

Run:

```powershell
go test ./apps/api/internal/config ./apps/api/cmd/migrate -count=1
go vet ./apps/api/internal/config ./apps/api/cmd/migrate
```

Expected: PASS.

- [ ] **Step 7: Commit runtime integration**

```powershell
git add apps/api/internal/config apps/api/cmd/migrate
git commit -m "feat(config): inject ConfigHub database URLs into runtimes"
```

---

### Task 4: Build an idempotent PostgreSQL Bootstrap engine

**Files:**
- Create: `apps/api/internal/dbbootstrap/bootstrap.go`
- Create: `apps/api/internal/dbbootstrap/bootstrap_test.go`
- Create: `apps/api/internal/dbbootstrap/confighub_acceptance_test.go`

**Interfaces:**
- Consumes: `config.ConfigHubDatabaseSource`, `config.DatabaseRole`, and embedded migrations.
- Produces: `Preflight(context.Context, ConfigHubDatabaseSource) error` and `Run(context.Context, ConfigHubDatabaseSource) (Result, error)`.

- [ ] **Step 1: Write failing tests for immutable targets and ordering**

Define and test these result and target shapes:

```go
type Result struct {
	Databases []DatabaseResult
}

type DatabaseResult struct {
	Name    string
	Created bool
	Version uint
}

func TestTargetsKeepAcceptanceBeforeProduction(t *testing.T) {
	got := databaseTargets()
	if len(got) != 2 || got[0] != "dayorder-test" || got[1] != "dayorder" {
		t.Fatalf("database targets = %v", got)
	}
}
```

Add a source scan asserting Bootstrap SQL constants contain no case-insensitive `DROP DATABASE`, `DROP SCHEMA`, `DROP ROLE`, or `TRUNCATE`.

- [ ] **Step 2: Write failing tests for role statements**

The generated plan must contain exactly the three fixed role names, `NOSUPERUSER`, `NOCREATEDB`, `NOCREATEROLE`, `NOREPLICATION`, `NOBYPASSRLS`, expected connection limits, UTC timezone, and timeouts. Password values must be bound parameters and must never appear in generated SQL strings or errors.

- [ ] **Step 3: Run the Bootstrap tests to verify they fail**

Run: `go test ./apps/api/internal/dbbootstrap -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 4: Implement read-only preflight**

Connect to maintenance database `postgres` using `source.AdminURL("postgres")`. Query the current administrator row from `pg_roles`, require `rolcreatedb=true` and `rolcreaterole=true` (or `rolsuper=true`), confirm TLS via `pg_stat_ssl`, and inspect both target names in `pg_database`. Preflight must execute no DDL.

```sql
SELECT r.rolsuper, r.rolcreatedb, r.rolcreaterole
FROM pg_catalog.pg_roles r
WHERE r.rolname = current_user;

SELECT ssl
FROM pg_catalog.pg_stat_ssl
WHERE pid = pg_backend_pid();
```

- [ ] **Step 5: Implement transactional role reconciliation**

Create or alter only the three fixed roles. Supply passwords as protocol parameters to `set_config`, and use server-side `format('%L', current_setting(...))` inside a transaction so Go never interpolates a password into SQL. Reconcile role flags, connection limits, UTC, statement timeout, and idle transaction timeout to the existing `bootstrap-roles.sql` values.

- [ ] **Step 6: Implement safe database creation**

For each fixed target, query `pg_database` first. If missing, issue a `CREATE DATABASE` statement built with `pgx.Identifier{name}.Sanitize()`. If present, retain it only when its owner is the current administrator; otherwise fail with a non-secret ownership conflict. Do not expose any method accepting an arbitrary database name from CLI flags.

- [ ] **Step 7: Implement per-database grants, Schema, and migrations**

Connect as administrator to the target, revoke `PUBLIC` connect/temporary and public-Schema create permissions, grant connect to all three roles, grant database create to Migrator, create/validate the `dayorder` Schema owner, then invoke `migrations.Up` with the Migrator role URL. Finish with `migrations.RequireCurrent` and `SELECT 1` through API and Worker role connections.

- [ ] **Step 8: Run Bootstrap unit, config, and migration tests**

Before running the package tests, create `confighub_acceptance_test.go` with an opt-in `TestConfigHubAcceptance`. It skips unless `DAYORDER_CONFIGHUB_ACCEPTANCE=1`; when enabled it loads the ConfigHub source, starts the real API on a free loopback port, registers a unique account, reads the queued verification Token through the administrator connection without logging it, verifies the account, creates/reads/updates a goal and task over HTTP, confirms an anonymous request returns 401, deletes the test account through the administrator connection, and stops the API. It must also prove the API role cannot create a table, the Worker role can read permitted Outbox tables, and the Migrator schema version is current. Every cleanup SQL statement must filter by the generated account UUID or unique email and must target `dayorder-test` only.

Run:

```powershell
go test ./apps/api/internal/dbbootstrap ./apps/api/internal/config ./apps/api/internal/migrations -count=1
go vet ./apps/api/internal/dbbootstrap
```

Expected: PASS.

- [ ] **Step 9: Commit the Bootstrap engine**

```powershell
git add apps/api/internal/dbbootstrap
git commit -m "feat(database): add safe PostgreSQL bootstrap engine"
```

---

### Task 5: Add the Bootstrap command and operator-facing npm commands

**Files:**
- Create: `apps/api/cmd/bootstrap/main.go`
- Create: `apps/api/cmd/bootstrap/main_test.go`
- Modify: `package.json`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 4 `dbbootstrap.Preflight` and `dbbootstrap.Run`.
- Produces: read-only preflight and explicit apply commands routed through ConfigHub.

- [ ] **Step 1: Write failing command option tests**

Extract:

```go
type options struct { Preflight bool }
func parseOptions(args []string) (options, error)
```

Test no arguments as apply mode, `-preflight` as read-only mode, and rejection of positional arguments or unknown flags. There must be no database-name, host, username, password, reset, drop, or force flag.

- [ ] **Step 2: Run command tests to verify they fail**

Run: `go test ./apps/api/cmd/bootstrap -count=1`

Expected: FAIL because the command does not exist.

- [ ] **Step 3: Implement a sanitized command entry point**

Load the ConfigHub source directly with `config.LoadConfigHubDatabaseSource(os.LookupEnv)`, immediately call `config.ScrubConfigHubDatabaseEnvironment`, and then execute preflight or apply. Print only stages, fixed database names, created/existing status, and migration versions.

```text
preflight ok: PostgreSQL TLS and administrator capabilities verified
database dayorder-test: created, schema version 7
database dayorder: created, schema version 7
```

Errors must wrap an operation name without including a DSN or config value.

- [ ] **Step 4: Add exact npm commands**

Add these scripts without changing current default development commands:

```json
"config:db:preflight": "confighub run --project shier --env prod -- go run ./apps/api/cmd/bootstrap -preflight",
"config:db:bootstrap": "confighub run --project shier --env prod -- go run ./apps/api/cmd/bootstrap",
"config:db:check": "confighub run --project shier --env prod -- go run ./apps/api/cmd/migrate -check",
"config:dev:api": "confighub run --project shier --env prod -- go run ./apps/api/cmd/server",
"config:dev:worker": "confighub run --project shier --env prod -- go run ./apps/api/cmd/worker"
```

- [ ] **Step 5: Document operation and rotation order**

README must state: ConfigHub CLI is read-only; the seven keys live in `shier/prod`; local commands default to `dayorder-test`; production requires explicit `DAYORDER_ENV=production`; run preflight before Bootstrap; Bootstrap never drops databases; publish a password Revision, run Bootstrap, then restart the affected role; and never paste ConfigHub output into issue trackers or logs.

- [ ] **Step 6: Run command, JSON, and documentation checks**

Run:

```powershell
go test ./apps/api/cmd/bootstrap -count=1
node -e "JSON.parse(require('fs').readFileSync('package.json','utf8')); console.log('package json ok')"
npm run test:architecture
```

Expected: PASS and `package json ok`.

- [ ] **Step 7: Commit command and documentation**

```powershell
git add apps/api/cmd/bootstrap package.json README.md
git commit -m "feat(database): expose ConfigHub bootstrap commands"
```

---

### Task 6: Run the complete local code quality gate

**Files:**
- Modify only if a verification failure reveals a scoped defect in Tasks 1–5.

**Interfaces:**
- Consumes: all code deliverables.
- Produces: fresh evidence that existing behavior remains compatible before any external database write.

- [ ] **Step 1: Format changed Go files**

Run: `gofmt -w apps/api/internal/config/database_source.go apps/api/internal/config/database_source_test.go apps/api/internal/dbbootstrap/bootstrap.go apps/api/internal/dbbootstrap/bootstrap_test.go apps/api/cmd/bootstrap/main.go apps/api/cmd/bootstrap/main_test.go apps/api/cmd/migrate/main.go apps/api/cmd/migrate/main_test.go`

- [ ] **Step 2: Run backend tests and vet**

Run:

```powershell
go test ./apps/api/... -count=1
go vet ./apps/api/...
```

Expected: all non-Docker tests PASS; Docker-dependent tests may report their existing explicit skip because Docker is absent.

- [ ] **Step 3: Run repository gates**

Run:

```powershell
npm run typecheck
npm test
npm run test:architecture
npm run test:security
npm run build
```

Expected: all commands exit 0. Any documented Docker-only acceptance may skip; no other failure is accepted.

- [ ] **Step 4: Inspect the diff for secret leakage**

Run:

```powershell
git diff --check
rg -n --hidden --glob '!.git/**' --glob '!.confighub.yaml' 'db_(api|worker|migrator)_password\s*[=:]\s*[A-Za-z0-9_-]{20,}' .
```

Expected: `git diff --check` is silent and the secret-pattern search returns no matches.

---

### Task 7: Preflight, create, and initialize the two remote databases

**Files:**
- No repository changes; this task changes the explicitly authorized PostgreSQL server.

**Interfaces:**
- Consumes: ConfigHub Revision 3 and Task 5 operator commands.
- Produces: initialized `dayorder-test` and `dayorder` at embedded migration version 7.

- [ ] **Step 1: Refresh PATH without printing credentials**

Run in the execution PowerShell:

```powershell
$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$env:Path = "$machinePath;$userPath"
confighub version
```

Expected: `v0.1.1`.

- [ ] **Step 2: Revalidate only ConfigHub metadata**

Capture `confighub export --project shier --env prod --format json` in memory, assert Revision is at least 3, assert all seven keys are non-empty and three role passwords differ, then discard the object. Print no values.

- [ ] **Step 3: Run read-only PostgreSQL preflight**

Run: `npm run config:db:preflight`

Expected: TLS enabled; administrator can create databases and roles; the command reports whether the two fixed databases exist without DDL.

- [ ] **Step 4: Stop if preflight reports an unexpected existing target**

If either database exists with conflicting ownership or Schema state, do not apply Bootstrap. Report the exact non-secret conflict for user direction. If both are absent or compatible, continue.

- [ ] **Step 5: Apply idempotent Bootstrap**

Run: `npm run config:db:bootstrap`

Expected: both fixed databases reach migration version 7; output contains no URL, Token, or password.

- [ ] **Step 6: Verify development and production schema versions independently**

Run development check:

```powershell
npm run config:db:check
```

Run production check in a child scope so `DAYORDER_ENV` cannot leak into later local commands:

```powershell
& {
  $env:DAYORDER_ENV = 'production'
  confighub run --project shier --env prod -- go run ./apps/api/cmd/migrate -check
}
```

Expected: both commands print `database schema is current at version 7`.

- [ ] **Step 7: Re-run Bootstrap to prove idempotency**

Run: `npm run config:db:bootstrap`

Expected: both databases report existing/current, no duplicate-role or duplicate-Schema error, and no destructive action.

---

### Task 8: Validate DayOrder locally against `dayorder-test`

**Files:**
- No repository changes unless a scoped runtime defect is discovered and covered by a failing test first.

**Interfaces:**
- Consumes: initialized `dayorder-test`, Task 5 API/Worker commands, and existing HTTP health endpoints.
- Produces: evidence for API readiness, Worker startup, restricted-role access, and unchanged frontend/backend behavior.

- [ ] **Step 1: Start the API in a managed exec session**

Run: `npm run config:dev:api`

Expected: the command remains running, logs `DayOrder PostgreSQL API started`, and does not print a DSN or password. Record the session ID for cleanup.

- [ ] **Step 2: Verify API liveness, readiness, and anonymous protection**

Run:

```powershell
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8080/health/live
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8080/health/ready
try {
  Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8080/api/v1/goals -ErrorAction Stop
  throw 'anonymous goals request unexpectedly succeeded'
} catch {
  if ([int]$_.Exception.Response.StatusCode -ne 401) { throw }
}
```

Expected: live and ready return HTTP 200; anonymous goals return HTTP 401.

- [ ] **Step 3: Start the Worker in a second managed exec session**

Run: `npm run config:dev:worker`

Expected: the command remains running, logs `dayorder worker started`, and its metrics endpoint at `http://127.0.0.1:9091/metrics` returns HTTP 200.

- [ ] **Step 4: Run the opt-in HTTP and restricted-role acceptance**

Run the gated acceptance test created in Task 4. It starts its own API instance on a free loopback port and cleans up only its unique `dayorder-test` account data.

Run:

```powershell
& {
  $env:DAYORDER_CONFIGHUB_ACCEPTANCE = '1'
  confighub run --project shier --env prod -- go test ./apps/api/internal/dbbootstrap -run TestConfigHubAcceptance -count=1 -v
}
```

Expected: PASS.

- [ ] **Step 5: Stop API and Worker sessions cleanly**

Send Ctrl+C to each recorded session and wait for exit. Expected: API performs graceful shutdown and Worker logs `dayorder worker stopped`.

- [ ] **Step 6: Run final build and repository status checks**

Run:

```powershell
npm test
npm run build
git status --short --branch
```

Expected: tests/build exit 0; `.confighub.yaml` is absent from status; only intentional commits are ahead of `origin/main`.

---

## Completion Evidence

Before claiming completion, record fresh output proving:

- ConfigHub Revision 3+ has all seven fields without printing values.
- `.confighub.yaml` is ignored.
- Go tests, vet, TypeScript checks, security/architecture gates, and builds pass.
- PostgreSQL preflight confirms TLS and administrator capabilities.
- Both `dayorder-test` and `dayorder` are clean at schema version 7.
- Bootstrap succeeds twice without destructive behavior.
- API live/ready and Worker metrics succeed against `dayorder-test`.
- Restricted API/Worker/Migrator role acceptance passes.
- No password, Token, full DSN, or raw ConfigHub export appears in Git diff or logs.
