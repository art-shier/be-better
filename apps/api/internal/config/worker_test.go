package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadWorkerFallsBackToConfigHubWorkerURL(t *testing.T) {
	values := validConfigHubValues()

	cfg, err := LoadWorkerFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresURL(t, cfg.Database.URL, "dayorder_worker", "worker-secret", "dayorder-test", false)
}

func TestLoadWorkerKeepsNativeURLPriorityOverInvalidConfigHubFields(t *testing.T) {
	const nativeURL = "postgresql://native-worker:native-secret@native.example:5432/native-db"
	values := map[string]string{
		"WORKER_DATABASE_URL": nativeURL,
		"db_address":          "invalid/address",
	}

	cfg, err := LoadWorkerFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != nativeURL {
		t.Fatal("native Worker URL did not retain priority")
	}
}

func TestLoadWorkerKeepsPathlessNativeURLCompatibility(t *testing.T) {
	const nativeURL = "postgresql://native-worker@native.example:5432"
	cfg, err := LoadWorkerFrom(mapLookup(map[string]string{"WORKER_DATABASE_URL": nativeURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != nativeURL {
		t.Fatal("pathless native Worker URL was not returned unchanged")
	}
}

func TestLoadWorkerScrubsConfigHubDatabaseEnvironmentAfterSuccess(t *testing.T) {
	setConfigHubDatabaseEnvironment(t, validConfigHubValues())
	t.Setenv("WORKER_DATABASE_URL", "")
	t.Setenv("DAYORDER_ENV", "development")

	if _, err := LoadWorker(); err != nil {
		t.Fatal(err)
	}
	for _, key := range configHubDatabaseKeysForTest() {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("%s remained in the process environment", key)
		}
	}
}

func TestLoadWorkerPrefersConfigHubSMTPPasswordWithoutTrimming(t *testing.T) {
	values := map[string]string{
		"WORKER_DATABASE_URL":    "postgres://worker:secret@localhost/dayorder",
		"DAYORDER_PUBLIC_URL":    "http://127.0.0.1:8080",
		"DAYORDER_MAIL_SINK":     "smtp",
		"DAYORDER_SMTP_ADDRESS":  "smtp.example.com:587",
		"DAYORDER_SMTP_FROM":     "noreply@example.com",
		"DAYORDER_SMTP_USERNAME": "resend",
		"DAYORDER_SMTP_PASSWORD": "legacy-password",
		"dayorder_smtp_password": " config-hub-password ",
		"DAYORDER_SMTP_TLS_MODE": "starttls",
		"DAYORDER_AUTH_HMAC_KEY": "0123456789abcdef0123456789abcdef",
	}

	config, err := LoadWorkerFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.Mail.Password != " config-hub-password " {
		t.Fatal("ConfigHub SMTP password did not retain priority or exact bytes")
	}
}

func TestLoadWorkerPrefersConfigHubAuthHMACKeyWithoutTrimming(t *testing.T) {
	values := map[string]string{
		"WORKER_DATABASE_URL":    "postgres://worker:secret@localhost/dayorder",
		"DAYORDER_PUBLIC_URL":    "http://127.0.0.1:8080",
		"DAYORDER_AUTH_HMAC_KEY": "legacy-auth-hmac-key-with-at-least-32-bytes",
		"dayorder_auth_hmac_key": " config-hub-auth-hmac-key-with-at-least-32-bytes ",
	}

	config, err := LoadWorkerFrom(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if string(config.AuthHMACKey) != " config-hub-auth-hmac-key-with-at-least-32-bytes " {
		t.Fatal("ConfigHub auth HMAC key did not retain priority or exact bytes")
	}
}

func TestLoadWorkerScrubsConfigHubAuthHMACKeyAfterSuccess(t *testing.T) {
	t.Setenv("WORKER_DATABASE_URL", "postgres://worker:secret@localhost/dayorder")
	t.Setenv("DAYORDER_PUBLIC_URL", "http://127.0.0.1:8080")
	t.Setenv("dayorder_auth_hmac_key", "config-hub-auth-hmac-key-with-at-least-32-bytes")

	if _, err := LoadWorker(); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("dayorder_auth_hmac_key"); ok {
		t.Fatal("dayorder_auth_hmac_key remained in the process environment")
	}
}

func TestLoadWorkerScrubsConfigHubSMTPPasswordAfterSuccess(t *testing.T) {
	t.Setenv("WORKER_DATABASE_URL", "postgres://worker:secret@localhost/dayorder")
	t.Setenv("DAYORDER_PUBLIC_URL", "http://127.0.0.1:8080")
	t.Setenv("DAYORDER_MAIL_SINK", "smtp")
	t.Setenv("DAYORDER_SMTP_ADDRESS", "smtp.example.com:587")
	t.Setenv("DAYORDER_SMTP_FROM", "noreply@example.com")
	t.Setenv("DAYORDER_SMTP_USERNAME", "resend")
	t.Setenv("dayorder_smtp_password", "config-hub-password")

	if _, err := LoadWorker(); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("dayorder_smtp_password"); ok {
		t.Fatal("dayorder_smtp_password remained in the process environment")
	}
}

func TestLoadWorkerConfigRequiresProductionSMTPAndSecureTransport(t *testing.T) {
	base := map[string]string{
		"DAYORDER_ENV":              "production",
		"WORKER_DATABASE_URL":       "postgres://worker:secret@db/dayorder",
		"DAYORDER_PUBLIC_URL":       "https://dayorder.example",
		"DAYORDER_MAIL_SINK":        "smtp",
		"DAYORDER_SMTP_ADDRESS":     "smtp.example.com:587",
		"DAYORDER_SMTP_FROM":        "noreply@example.com",
		"DAYORDER_SMTP_TLS_MODE":    "starttls",
		"DAYORDER_SMTP_USERNAME":    "user",
		"DAYORDER_SMTP_PASSWORD":    "password",
		"DAYORDER_WORKER_POLL_RATE": "2s",
		"DAYORDER_AUTH_HMAC_KEY":    "0123456789abcdef0123456789abcdef",
	}
	lookup := func(key string) (string, bool) { value, ok := base[key]; return value, ok }
	config, err := LoadWorkerFrom(lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.MaxConns != 5 || config.PollInterval != 2*time.Second || config.Mail.Sink != "smtp" {
		t.Fatalf("worker config = %#v", config)
	}

	base["DAYORDER_SMTP_TLS_MODE"] = "none"
	if _, err = LoadWorkerFrom(lookup); err == nil {
		t.Fatal("production SMTP without TLS unexpectedly accepted")
	}
	base["DAYORDER_SMTP_TLS_MODE"] = "starttls"
	base["DAYORDER_MAIL_SINK"] = "log"
	if _, err = LoadWorkerFrom(lookup); err == nil {
		t.Fatal("production log mail sink unexpectedly accepted")
	}
}

func TestLoadWorkerProductionDoesNotRequireDisabledAgentProvider(t *testing.T) {
	values := map[string]string{
		"DAYORDER_ENV":           "production",
		"WORKER_DATABASE_URL":    "postgres://worker:secret@db/dayorder",
		"DAYORDER_PUBLIC_URL":    "https://dayorder.example",
		"DAYORDER_MAIL_SINK":     "smtp",
		"DAYORDER_SMTP_ADDRESS":  "smtp.example.com:587",
		"DAYORDER_SMTP_FROM":     "noreply@example.com",
		"DAYORDER_SMTP_TLS_MODE": "starttls",
		"DAYORDER_SMTP_PASSWORD": "password",
		"DAYORDER_AUTH_HMAC_KEY": "0123456789abcdef0123456789abcdef",
	}

	if _, err := LoadWorkerFrom(mapLookup(values)); err != nil {
		t.Fatalf("disabled Agent configuration blocked Worker startup: %v", err)
	}
}

func TestLoadWorkerConfigAllowsExplicitDevelopmentMetadataSink(t *testing.T) {
	values := map[string]string{
		"WORKER_DATABASE_URL": "postgres://worker:secret@localhost/dayorder",
		"DAYORDER_PUBLIC_URL": "http://127.0.0.1:8080",
		"DAYORDER_MAIL_SINK":  "log",
	}
	config, err := LoadWorkerFrom(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	if config.Mail.Sink != "log" || config.Database.MaxConns != 5 {
		t.Fatalf("worker config = %#v", config)
	}
	if config.MetricsAddress != "127.0.0.1:9091" {
		t.Fatalf("worker metrics address = %q", config.MetricsAddress)
	}
}

func TestLoadWorkerConfigRejectsInvalidMetricsAddress(t *testing.T) {
	values := map[string]string{
		"WORKER_DATABASE_URL":          "postgres://worker:secret@localhost/dayorder",
		"DAYORDER_WORKER_METRICS_ADDR": "missing-port",
	}
	if _, err := LoadWorkerFrom(mapLookup(values)); err == nil {
		t.Fatal("invalid worker metrics address unexpectedly accepted")
	}
}
