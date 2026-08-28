package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type WorkerMailConfig struct {
	Sink     string
	Address  string
	From     string
	Username string
	Password string
	TLSMode  string
	Timeout  time.Duration
}

type WorkerConfig struct {
	Environment  Environment
	PublicURL    string
	Database     DatabaseConfig
	PollInterval time.Duration
	Mail         WorkerMailConfig
}

func LoadWorker() (WorkerConfig, error) { return LoadWorkerFrom(os.LookupEnv) }

func LoadWorkerFrom(lookup LookupFunc) (WorkerConfig, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	environment := Environment(valueOr(lookup, "DAYORDER_ENV", string(Development)))
	if environment != Development && environment != Test && environment != Production {
		return WorkerConfig{}, errors.New("DAYORDER_ENV must be development, test, or production")
	}
	databaseURL := strings.TrimSpace(valueOr(lookup, "WORKER_DATABASE_URL", ""))
	parsedDatabaseURL, err := url.Parse(databaseURL)
	if databaseURL == "" || err != nil || parsedDatabaseURL.Host == "" || (parsedDatabaseURL.Scheme != "postgres" && parsedDatabaseURL.Scheme != "postgresql") {
		return WorkerConfig{}, errors.New("WORKER_DATABASE_URL must be a valid PostgreSQL URL")
	}
	maxConns, err := parseInt32(lookup, "DAYORDER_WORKER_DB_MAX_CONNS", 5)
	if err != nil || maxConns < 1 {
		return WorkerConfig{}, errors.New("DAYORDER_WORKER_DB_MAX_CONNS must be a positive integer")
	}
	minConns, err := parseInt32(lookup, "DAYORDER_WORKER_DB_MIN_CONNS", 1)
	if err != nil || minConns < 0 || minConns > maxConns {
		return WorkerConfig{}, errors.New("DAYORDER_WORKER_DB_MIN_CONNS must be between zero and DAYORDER_WORKER_DB_MAX_CONNS")
	}
	database := DatabaseConfig{URL: databaseURL, MaxConns: maxConns, MinConns: minConns}
	durations := []struct {
		key      string
		fallback time.Duration
		target   *time.Duration
	}{
		{"DAYORDER_DB_MAX_CONN_LIFETIME", 30 * time.Minute, &database.MaxConnLifetime},
		{"DAYORDER_DB_MAX_CONN_IDLE_TIME", 5 * time.Minute, &database.MaxConnIdleTime},
		{"DAYORDER_DB_STATEMENT_TIMEOUT", 30 * time.Second, &database.StatementTimeout},
		{"DAYORDER_DB_LOCK_TIMEOUT", 2 * time.Second, &database.LockTimeout},
		{"DAYORDER_DB_IDLE_TX_TIMEOUT", 10 * time.Second, &database.IdleTransactionTimeout},
		{"DAYORDER_DB_HEALTH_TIMEOUT", 3 * time.Second, &database.HealthTimeout},
	}
	for _, item := range durations {
		value, parseErr := parseDuration(lookup, item.key, item.fallback)
		if parseErr != nil || value <= 0 {
			return WorkerConfig{}, fmt.Errorf("%s must be a positive duration", item.key)
		}
		*item.target = value
	}

	publicURL := strings.TrimSpace(valueOr(lookup, "DAYORDER_PUBLIC_URL", "http://127.0.0.1:8080"))
	parsedPublicURL, err := parseOrigin(publicURL)
	if err != nil || (environment == Production && parsedPublicURL.Scheme != "https") {
		return WorkerConfig{}, errors.New("DAYORDER_PUBLIC_URL must be a valid HTTPS origin in production")
	}
	pollInterval, err := parseDuration(lookup, "DAYORDER_WORKER_POLL_RATE", time.Second)
	if err != nil || pollInterval < 100*time.Millisecond || pollInterval > time.Minute {
		return WorkerConfig{}, errors.New("DAYORDER_WORKER_POLL_RATE must be between 100ms and 1m")
	}
	mailConfig := WorkerMailConfig{
		Sink:    strings.ToLower(valueOr(lookup, "DAYORDER_MAIL_SINK", "log")),
		Address: valueOr(lookup, "DAYORDER_SMTP_ADDRESS", ""), From: valueOr(lookup, "DAYORDER_SMTP_FROM", ""),
		Username: valueOr(lookup, "DAYORDER_SMTP_USERNAME", ""), Password: valueOr(lookup, "DAYORDER_SMTP_PASSWORD", ""),
		TLSMode: strings.ToLower(valueOr(lookup, "DAYORDER_SMTP_TLS_MODE", "starttls")),
	}
	mailConfig.Timeout, err = parseDuration(lookup, "DAYORDER_SMTP_TIMEOUT", 15*time.Second)
	if err != nil || mailConfig.Timeout <= 0 {
		return WorkerConfig{}, errors.New("DAYORDER_SMTP_TIMEOUT must be a positive duration")
	}
	if mailConfig.Sink != "smtp" && mailConfig.Sink != "log" {
		return WorkerConfig{}, errors.New("DAYORDER_MAIL_SINK must be smtp or log")
	}
	if environment == Production && mailConfig.Sink != "smtp" {
		return WorkerConfig{}, errors.New("DAYORDER_MAIL_SINK must be smtp in production")
	}
	if mailConfig.Sink == "smtp" {
		if mailConfig.Address == "" || mailConfig.From == "" {
			return WorkerConfig{}, errors.New("SMTP address and sender are required")
		}
		if environment == Production && mailConfig.TLSMode == "none" {
			return WorkerConfig{}, errors.New("SMTP TLS is required in production")
		}
	}
	return WorkerConfig{
		Environment: environment, PublicURL: strings.TrimSuffix(parsedPublicURL.String(), "/"),
		Database: database, PollInterval: pollInterval, Mail: mailConfig,
	}, nil
}
