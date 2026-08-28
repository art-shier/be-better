package config

import (
	"strings"
	"testing"
	"time"
)

func mapLookup(values map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
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
