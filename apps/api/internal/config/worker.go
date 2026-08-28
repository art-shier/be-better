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

type WorkerAgentConfig struct {
	Provider string
	Model    string
	HTTPURL  string
	HTTPKey  string
	Timeout  time.Duration
}

type WorkerConfig struct {
	Environment    Environment
	PublicURL      string
	MetricsAddress string
	AuthHMACKey    []byte
	Database       DatabaseConfig
	PollInterval   time.Duration
	Mail           WorkerMailConfig
	Agent          WorkerAgentConfig
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
	metricsAddress := strings.TrimSpace(valueOr(lookup, "DAYORDER_WORKER_METRICS_ADDR", "127.0.0.1:9091"))
	if err = validateListenAddress(metricsAddress); err != nil {
		return WorkerConfig{}, fmt.Errorf("DAYORDER_WORKER_METRICS_ADDR: %w", err)
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
	hmacKey := valueOr(lookup, "DAYORDER_AUTH_HMAC_KEY", "")
	if hmacKey == "" && environment != Production {
		hmacKey = "development-only-hmac-key-change-before-production"
	}
	if len([]byte(hmacKey)) < 32 {
		return WorkerConfig{}, errors.New("DAYORDER_AUTH_HMAC_KEY must contain at least 32 bytes")
	}
	if mailConfig.Sink == "smtp" {
		if mailConfig.Address == "" || mailConfig.From == "" {
			return WorkerConfig{}, errors.New("SMTP address and sender are required")
		}
		if environment == Production && mailConfig.TLSMode == "none" {
			return WorkerConfig{}, errors.New("SMTP TLS is required in production")
		}
	}
	agentConfig := WorkerAgentConfig{
		Provider: strings.ToLower(valueOr(lookup, "DAYORDER_AGENT_PROVIDER", "deterministic")),
		Model:    valueOr(lookup, "DAYORDER_AGENT_MODEL", "rules-v1"),
		HTTPURL:  valueOr(lookup, "DAYORDER_AGENT_HTTP_URL", ""),
		HTTPKey:  valueOr(lookup, "DAYORDER_AGENT_HTTP_KEY", ""),
	}
	agentConfig.Timeout, err = parseDuration(lookup, "DAYORDER_AGENT_TIMEOUT", 30*time.Second)
	if err != nil || agentConfig.Timeout < time.Second || agentConfig.Timeout > 2*time.Minute {
		return WorkerConfig{}, errors.New("DAYORDER_AGENT_TIMEOUT must be between 1s and 2m")
	}
	if agentConfig.Provider != "deterministic" && agentConfig.Provider != "http" {
		return WorkerConfig{}, errors.New("DAYORDER_AGENT_PROVIDER must be deterministic or http")
	}
	if environment == Production && agentConfig.Provider != "http" {
		return WorkerConfig{}, errors.New("DAYORDER_AGENT_PROVIDER must be http in production")
	}
	if agentConfig.Provider == "http" {
		parsedAgentURL, parseErr := url.Parse(agentConfig.HTTPURL)
		if parseErr != nil || parsedAgentURL.Host == "" || (parsedAgentURL.Scheme != "http" && parsedAgentURL.Scheme != "https") || parsedAgentURL.User != nil || parsedAgentURL.Fragment != "" {
			return WorkerConfig{}, errors.New("DAYORDER_AGENT_HTTP_URL must be an absolute HTTP or HTTPS URL")
		}
		if environment == Production && parsedAgentURL.Scheme != "https" {
			return WorkerConfig{}, errors.New("DAYORDER_AGENT_HTTP_URL must use HTTPS in production")
		}
		if agentConfig.HTTPKey == "" || agentConfig.Model == "" {
			return WorkerConfig{}, errors.New("Agent HTTP key and model are required")
		}
	}
	return WorkerConfig{
		Environment: environment, PublicURL: strings.TrimSuffix(parsedPublicURL.String(), "/"), MetricsAddress: metricsAddress,
		AuthHMACKey: []byte(hmacKey), Database: database, PollInterval: pollInterval, Mail: mailConfig, Agent: agentConfig,
	}, nil
}
