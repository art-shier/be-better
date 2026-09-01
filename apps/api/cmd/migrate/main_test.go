package main

import (
	"net/url"
	"strings"
	"testing"

	"dayorder.local/api/internal/config"
)

func TestResolveDatabaseURLPrefersFlag(t *testing.T) {
	const flagURL = "postgresql://flag-user:flag-password@flag.example:5432/flag-db"
	got, err := resolveDatabaseURL(flagURL, lookupFromMap(map[string]string{
		"DAYORDER_ENV":           "invalid",
		"MIGRATION_DATABASE_URL": "not-a-url",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got != flagURL {
		t.Fatal("migration flag URL did not retain priority")
	}
}

func TestResolveDatabaseURLPrefersNativeEnvironmentURL(t *testing.T) {
	const nativeURL = "postgresql://native-migrator:native-password@native.example:5432/native-db"
	got, err := resolveDatabaseURL("", lookupFromMap(map[string]string{
		"MIGRATION_DATABASE_URL": nativeURL,
		"db_address":             "invalid/address",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got != nativeURL {
		t.Fatal("native Migrator URL did not retain priority")
	}
}

func TestResolveDatabaseURLKeepsPathlessExplicitURLCompatibility(t *testing.T) {
	const flagURL = "postgresql://flag-user@flag.example:5432"
	got, err := resolveDatabaseURL(flagURL, lookupFromMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != flagURL {
		t.Fatal("pathless migration flag URL was not returned unchanged")
	}

	const nativeURL = "postgresql://native-user@native.example:5432"
	got, err = resolveDatabaseURL("", lookupFromMap(map[string]string{"MIGRATION_DATABASE_URL": nativeURL}))
	if err != nil {
		t.Fatal(err)
	}
	if got != nativeURL {
		t.Fatal("pathless native Migrator URL was not returned unchanged")
	}
}

func TestResolveDatabaseURLFallsBackToConfigHubMigrator(t *testing.T) {
	got, err := resolveDatabaseURL("", lookupFromMap(validConfigHubValuesForMigration()))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("resolved migration URL could not be parsed: %v", err)
	}
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
	}
	if username != "dayorder_migrator" || parsed.Path != "/dayorder-test" {
		t.Fatalf("resolved migration URL selected username=%q path=%q", username, parsed.Path)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "migrator-secret" {
		t.Fatal("resolved migration URL did not use the Migrator password")
	}
	if parsed.Query().Get("sslmode") != "require" || parsed.Query().Get("search_path") != "dayorder" {
		t.Fatal("resolved migration URL did not require TLS and the dayorder search path")
	}
}

func TestResolveDatabaseURLRequiresNativeOrConfigHubSource(t *testing.T) {
	_, err := resolveDatabaseURL("", lookupFromMap(nil))
	if err == nil || !strings.Contains(err.Error(), "db_address") {
		t.Fatalf("missing migration database source returned error %v", err)
	}
}

func lookupFromMap(values map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validConfigHubValuesForMigration() map[string]string {
	return map[string]string{
		"DAYORDER_ENV":         "development",
		"db_address":           "db.example.internal",
		"db_port":              "55432",
		"db_username":          "bootstrap-admin",
		"db_password":          "admin-secret",
		"db_migrator_password": "migrator-secret",
		"db_api_password":      "api-secret",
		"db_worker_password":   "worker-secret",
	}
}
