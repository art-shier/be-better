package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadFallsBackToConfigHubAPIURL(t *testing.T) {
	values := validConfigHubValues()

	cfg, err := LoadFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresURL(t, cfg.Database.URL, "dayorder_api", "api-secret", "dayorder-test", false)
}

func TestLoadKeepsNativeURLPriorityOverInvalidConfigHubFields(t *testing.T) {
	const nativeURL = "postgresql://native-api:native-secret@native.example:5432/native-db"
	values := map[string]string{
		"DATABASE_URL": nativeURL,
		"db_address":   "invalid/address",
	}

	cfg, err := LoadFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != nativeURL {
		t.Fatal("native API URL did not retain priority")
	}
}

func TestLoadKeepsPathlessNativeURLCompatibility(t *testing.T) {
	const nativeURL = "postgresql://native-api@native.example:5432"
	cfg, err := LoadFrom(mapLookup(map[string]string{"DATABASE_URL": nativeURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != nativeURL {
		t.Fatal("pathless native API URL was not returned unchanged")
	}
}

func TestLoadPrefersConfigHubAuthHMACKeyWithoutTrimming(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":           "postgres://dayorder:secret@127.0.0.1:5432/dayorder",
		"DAYORDER_AUTH_HMAC_KEY": "legacy-auth-hmac-key-with-at-least-32-bytes",
		"dayorder_auth_hmac_key": " config-hub-auth-hmac-key-with-at-least-32-bytes ",
	}

	config, err := LoadFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if string(config.AuthHMACKey) != " config-hub-auth-hmac-key-with-at-least-32-bytes " {
		t.Fatal("ConfigHub auth HMAC key did not retain priority or exact bytes")
	}
}

func TestLoadScrubsConfigHubAuthHMACKeyAfterSuccess(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://dayorder:secret@127.0.0.1:5432/dayorder")
	t.Setenv("dayorder_auth_hmac_key", "config-hub-auth-hmac-key-with-at-least-32-bytes")

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("dayorder_auth_hmac_key"); ok {
		t.Fatal("dayorder_auth_hmac_key remained in the process environment")
	}
}

func TestLoadScrubsConfigHubDatabaseEnvironmentAfterSuccess(t *testing.T) {
	setConfigHubDatabaseEnvironment(t, validConfigHubValues())
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DAYORDER_ENV", "development")

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, key := range configHubDatabaseKeysForTest() {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s remained in the process environment", key)
		}
	}
}

func TestLoadDoesNotScrubConfigHubDatabaseEnvironmentAfterFailure(t *testing.T) {
	values := validConfigHubValues()
	values["db_port"] = "invalid"
	setConfigHubDatabaseEnvironment(t, values)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DAYORDER_ENV", "development")

	if _, err := Load(); err == nil {
		t.Fatal("invalid ConfigHub database source unexpectedly loaded")
	}
	for _, key := range configHubDatabaseKeysForTest() {
		if _, ok := os.LookupEnv(key); !ok {
			t.Fatalf("%s was scrubbed after a failed load", key)
		}
	}
}

func mapLookup(values map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func setConfigHubDatabaseEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for _, key := range configHubDatabaseKeysForTest() {
		t.Setenv(key, values[key])
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := LoadFrom(mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected DATABASE_URL error, got %v", err)
	}
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	cfg, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://dayorder:secret@127.0.0.1:5432/dayorder?sslmode=disable",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != Development {
		t.Fatalf("environment = %q", cfg.Environment)
	}
	if cfg.Address != "127.0.0.1:8080" {
		t.Fatalf("address = %q", cfg.Address)
	}
	if cfg.MetricsAddress != "127.0.0.1:9090" {
		t.Fatalf("metrics address = %q", cfg.MetricsAddress)
	}
	if cfg.Database.MaxConns != 20 || cfg.Database.MinConns != 2 {
		t.Fatalf("pool defaults = %d/%d", cfg.Database.MinConns, cfg.Database.MaxConns)
	}
	if cfg.Database.StatementTimeout != 5*time.Second || cfg.Database.LockTimeout != 2*time.Second {
		t.Fatalf("query timeout defaults = %s/%s", cfg.Database.StatementTimeout, cfg.Database.LockTimeout)
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Fatalf("allowed origins = %#v", cfg.AllowedOrigins)
	}
	if len(cfg.AuthHMACKey) < 32 {
		t.Fatalf("development HMAC key is unexpectedly weak")
	}
}

func TestLoadRejectsInvalidPoolSettings(t *testing.T) {
	tests := []map[string]string{
		{"DATABASE_URL": "postgres://localhost/dayorder", "DAYORDER_DB_MAX_CONNS": "0"},
		{"DATABASE_URL": "postgres://localhost/dayorder", "DAYORDER_DB_MAX_CONNS": "5", "DAYORDER_DB_MIN_CONNS": "6"},
		{"DATABASE_URL": "postgres://localhost/dayorder", "DAYORDER_DB_STATEMENT_TIMEOUT": "not-a-duration"},
		{"DATABASE_URL": "postgres://localhost/dayorder", "DAYORDER_METRICS_ADDR": "not-an-address"},
	}
	for _, values := range tests {
		if _, err := LoadFrom(mapLookup(values)); err == nil {
			t.Fatalf("expected invalid configuration error for %#v", values)
		}
	}
}

func TestLoadProductionRequiresSecureConfiguration(t *testing.T) {
	base := map[string]string{
		"DAYORDER_ENV":        "production",
		"DATABASE_URL":        "postgres://dayorder:secret@postgres:5432/dayorder?sslmode=disable",
		"DAYORDER_PUBLIC_URL": "https://dayorder.example.com",
	}
	if _, err := LoadFrom(mapLookup(base)); err == nil || !strings.Contains(err.Error(), "DAYORDER_AUTH_HMAC_KEY") {
		t.Fatalf("expected production HMAC error, got %v", err)
	}

	base["DAYORDER_AUTH_HMAC_KEY"] = strings.Repeat("k", 32)
	base["DAYORDER_PUBLIC_URL"] = "http://dayorder.example.com"
	if _, err := LoadFrom(mapLookup(base)); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected production HTTPS error, got %v", err)
	}

	base["DAYORDER_PUBLIC_URL"] = "https://dayorder.example.com"
	base["DAYORDER_ALLOWED_ORIGINS"] = "https://dayorder.example.com"
	if _, err := LoadFrom(mapLookup(base)); err != nil {
		t.Fatalf("valid production config: %v", err)
	}
}

func TestLoadRejectsInvalidAllowedOrigin(t *testing.T) {
	_, err := LoadFrom(mapLookup(map[string]string{
		"DATABASE_URL":             "postgres://localhost/dayorder",
		"DAYORDER_ALLOWED_ORIGINS": "https://example.com/path",
	}))
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("expected origin error, got %v", err)
	}
}
