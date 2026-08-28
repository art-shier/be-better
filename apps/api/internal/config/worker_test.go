package config

import (
	"testing"
	"time"
)

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
		"DAYORDER_AGENT_PROVIDER":   "http",
		"DAYORDER_AGENT_HTTP_URL":   "https://agent.example/v1/analyze",
		"DAYORDER_AGENT_HTTP_KEY":   "test-agent-api-key",
		"DAYORDER_AGENT_MODEL":      "enterprise-agent-v1",
		"DAYORDER_AUTH_HMAC_KEY":    "0123456789abcdef0123456789abcdef",
	}
	lookup := func(key string) (string, bool) { value, ok := base[key]; return value, ok }
	config, err := LoadWorkerFrom(lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.MaxConns != 5 || config.PollInterval != 2*time.Second || config.Mail.Sink != "smtp" || config.Agent.Provider != "http" {
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
	base["DAYORDER_MAIL_SINK"] = "smtp"
	base["DAYORDER_AGENT_PROVIDER"] = "deterministic"
	if _, err = LoadWorkerFrom(lookup); err == nil {
		t.Fatal("production deterministic Agent provider unexpectedly accepted")
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
	if config.Mail.Sink != "log" || config.Database.MaxConns != 5 || config.Agent.Provider != "deterministic" {
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
