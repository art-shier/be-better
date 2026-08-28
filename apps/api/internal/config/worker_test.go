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
}
