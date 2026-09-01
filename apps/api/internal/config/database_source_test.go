package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

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
	apiURL, err := source.RoleURL(Development, DatabaseRoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	workerURL, err := source.RoleURL(Development, DatabaseRoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	if apiURL == workerURL {
		t.Fatal("API and Worker URLs must differ")
	}
	assertPostgresURL(t, apiURL, "dayorder_api", "api-secret", "dayorder-test", false)
	assertPostgresURL(t, workerURL, "dayorder_worker", "worker-secret", "dayorder-test", false)
}

func TestRoleURLUsesMigratorSearchPathAndProductionDatabase(t *testing.T) {
	got, err := validConfigHubDatabaseSource().RoleURL(Production, DatabaseRoleMigrator)
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresURL(t, got, "dayorder_migrator", "migrator-secret", "dayorder", true)
}

func TestRoleURLRejectsUnsupportedRole(t *testing.T) {
	if _, err := validConfigHubDatabaseSource().RoleURL(Development, DatabaseRole("dayorder_unknown")); err == nil {
		t.Fatal("unsupported database role unexpectedly accepted")
	}
}

func TestAdminURLUsesEscapedCredentialsAndTLS(t *testing.T) {
	source := validConfigHubDatabaseSource()
	source.Address = "2001:db8::1"
	source.AdminUsername = "admin@tenant"
	source.AdminPassword = "p@ss:/?#[] value"

	got := source.AdminURL("postgres")
	assertPostgresURLWithHost(t, got, "admin@tenant", "p@ss:/?#[] value", "postgres", false, "2001:db8::1", 55432)
}

func TestLoadConfigHubDatabaseSourceTrimsMetadataAndPreservesPasswords(t *testing.T) {
	values := validConfigHubValues()
	values["db_address"] = "  db.example.internal  "
	values["db_port"] = " 55432 "
	values["db_username"] = "  bootstrap_admin  "
	values["db_api_password"] = " api password bytes "

	got, err := LoadConfigHubDatabaseSource(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "db.example.internal" || got.Port != 55432 || got.AdminUsername != "bootstrap_admin" {
		t.Fatalf("trimmed metadata was not loaded correctly: address=%q port=%d username=%q", got.Address, got.Port, got.AdminUsername)
	}
	if got.APIPassword != " api password bytes " {
		t.Fatal("database password bytes were not preserved exactly")
	}
}

func TestLoadConfigHubDatabaseSourceRequiresEveryKey(t *testing.T) {
	for _, key := range configHubDatabaseKeysForTest() {
		t.Run(key, func(t *testing.T) {
			values := validConfigHubValues()
			delete(values, key)

			_, err := LoadConfigHubDatabaseSource(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing key %s returned error %v", key, err)
			}
		})
	}
}

func TestLoadConfigHubDatabaseSourceRejectsEmptyCredentials(t *testing.T) {
	for _, key := range []string{"db_username", "db_password", "db_migrator_password", "db_api_password", "db_worker_password"} {
		t.Run(key, func(t *testing.T) {
			values := validConfigHubValues()
			values[key] = ""

			_, err := LoadConfigHubDatabaseSource(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("empty key %s returned error %v", key, err)
			}
		})
	}
}

func TestLoadConfigHubDatabaseSourceRejectsInvalidPortsWithoutEchoingValues(t *testing.T) {
	for _, value := range []string{"not-a-port", "0", "65536", "-1"} {
		t.Run(value, func(t *testing.T) {
			values := validConfigHubValues()
			values["db_port"] = value

			_, err := LoadConfigHubDatabaseSource(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), "db_port") {
				t.Fatalf("invalid port returned error %v", err)
			}
			if strings.Contains(err.Error(), value) {
				t.Fatal("invalid port value was echoed in the error")
			}
		})
	}
}

func TestLoadConfigHubDatabaseSourceRejectsUnsafeAddressesWithoutEchoingValues(t *testing.T) {
	for _, value := range []string{
		"postgresql://db.example.internal",
		"db.example.internal/path",
		"db\\example.internal",
		"admin@db.example.internal",
		"db example.internal",
		"db.example.internal:5432",
	} {
		t.Run(value, func(t *testing.T) {
			values := validConfigHubValues()
			values["db_address"] = value

			_, err := LoadConfigHubDatabaseSource(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), "db_address") {
				t.Fatalf("unsafe address returned error %v", err)
			}
			if strings.Contains(err.Error(), value) {
				t.Fatal("unsafe address was echoed in the error")
			}
		})
	}
}

func TestResolveDatabaseURLPrefersValidExplicitURL(t *testing.T) {
	const explicit = "postgresql://native-user:native-password@native.example:5432/native-db?sslmode=verify-full"
	values := map[string]string{
		"DATABASE_URL": explicit,
		"db_address":   "invalid/address",
	}

	got, err := ResolveDatabaseURL(mapLookup(values), Development, "DATABASE_URL", DatabaseRoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatal("explicit PostgreSQL URL was not returned unchanged")
	}
}

func TestResolveDatabaseURLRejectsInvalidExplicitURLWithoutFallingBack(t *testing.T) {
	values := validConfigHubValues()
	values["DATABASE_URL"] = "postgresql://should-not-leak"

	_, err := ResolveDatabaseURL(mapLookup(values), Development, "DATABASE_URL", DatabaseRoleAPI)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("invalid explicit URL returned error %v", err)
	}
	if strings.Contains(err.Error(), "should-not-leak") {
		t.Fatal("explicit URL was echoed in the error")
	}
}

func TestResolveDatabaseURLFallsBackToConfigHub(t *testing.T) {
	got, err := ResolveDatabaseURL(mapLookup(validConfigHubValues()), Development, "DATABASE_URL", DatabaseRoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresURL(t, got, "dayorder_api", "api-secret", "dayorder-test", false)
}

func TestResolveDatabaseURLRejectsTestEnvironmentFallback(t *testing.T) {
	if _, err := ResolveDatabaseURL(mapLookup(validConfigHubValues()), Test, "DATABASE_URL", DatabaseRoleAPI); err == nil {
		t.Fatal("ConfigHub fallback unexpectedly selected a database in test environment")
	}
}

func TestScrubConfigHubDatabaseEnvironmentUnsetsEverySourceKey(t *testing.T) {
	for _, key := range configHubDatabaseKeysForTest() {
		t.Setenv(key, "sentinel-value")
	}

	ScrubConfigHubDatabaseEnvironment()

	for _, key := range configHubDatabaseKeysForTest() {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s remained in the process environment", key)
		}
	}
}

func validConfigHubDatabaseSource() ConfigHubDatabaseSource {
	return ConfigHubDatabaseSource{
		Address:          "db.example.internal",
		Port:             55432,
		AdminUsername:    "bootstrap-admin",
		AdminPassword:    "admin-secret",
		MigratorPassword: "migrator-secret",
		APIPassword:      "api-secret",
		WorkerPassword:   "worker-secret",
	}
}

func validConfigHubValues() map[string]string {
	return map[string]string{
		"db_address":           "db.example.internal",
		"db_port":              "55432",
		"db_username":          "bootstrap-admin",
		"db_password":          "admin-secret",
		"db_migrator_password": "migrator-secret",
		"db_api_password":      "api-secret",
		"db_worker_password":   "worker-secret",
	}
}

func configHubDatabaseKeysForTest() []string {
	return []string{
		"db_address",
		"db_port",
		"db_username",
		"db_password",
		"db_migrator_password",
		"db_api_password",
		"db_worker_password",
	}
}

func assertPostgresURL(t *testing.T, rawURL, username, password, database string, searchPath bool) {
	t.Helper()
	assertPostgresURLWithHost(t, rawURL, username, password, database, searchPath, "db.example.internal", 55432)
}

func assertPostgresURLWithHost(t *testing.T, rawURL, username, password, database string, searchPath bool, host string, port int) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("database URL could not be parsed: %v", err)
	}
	if parsed.Scheme != "postgresql" {
		t.Fatalf("database URL scheme = %q", parsed.Scheme)
	}
	if parsed.Hostname() != host {
		t.Fatalf("database URL host = %q", parsed.Hostname())
	}
	if parsed.Port() != strconv.Itoa(port) {
		t.Fatalf("database URL port = %q", parsed.Port())
	}
	if parsed.Path != "/"+database {
		t.Fatalf("database URL path = %q", parsed.Path)
	}
	if parsed.User == nil || parsed.User.Username() != username {
		t.Fatal("database URL username was not preserved")
	}
	gotPassword, ok := parsed.User.Password()
	if !ok || gotPassword != password {
		t.Fatal("database URL password was not preserved")
	}
	query := parsed.Query()
	if query.Get("sslmode") != "require" || query.Get("timezone") != "UTC" {
		t.Fatalf("database URL TLS/timezone query = sslmode:%q timezone:%q", query.Get("sslmode"), query.Get("timezone"))
	}
	if searchPath && query.Get("search_path") != "dayorder" {
		t.Fatalf("migrator search_path = %q", query.Get("search_path"))
	}
	if !searchPath && query.Has("search_path") {
		t.Fatalf("runtime URL unexpectedly has search_path = %q", query.Get("search_path"))
	}
}
