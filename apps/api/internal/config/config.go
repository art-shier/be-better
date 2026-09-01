package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

type LookupFunc func(string) (string, bool)

type DatabaseConfig struct {
	URL                    string
	MaxConns               int32
	MinConns               int32
	MaxConnLifetime        time.Duration
	MaxConnIdleTime        time.Duration
	StatementTimeout       time.Duration
	LockTimeout            time.Duration
	IdleTransactionTimeout time.Duration
	HealthTimeout          time.Duration
}

type Config struct {
	Environment    Environment
	Address        string
	MetricsAddress string
	PublicURL      string
	AllowedOrigins []string
	AuthHMACKey    []byte
	Database       DatabaseConfig
}

func Load() (Config, error) {
	config, err := LoadFrom(os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	ScrubConfigHubDatabaseEnvironment()
	return config, nil
}

func LoadFrom(lookup LookupFunc) (Config, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	environment := Environment(valueOr(lookup, "DAYORDER_ENV", string(Development)))
	if environment != Development && environment != Test && environment != Production {
		return Config{}, fmt.Errorf("DAYORDER_ENV must be development, test, or production")
	}

	databaseURL, err := ResolveDatabaseURL(lookup, environment, "DATABASE_URL", DatabaseRoleAPI)
	if err != nil {
		return Config{}, err
	}

	maxConns, err := parseInt32(lookup, "DAYORDER_DB_MAX_CONNS", 20)
	if err != nil || maxConns < 1 {
		return Config{}, errors.New("DAYORDER_DB_MAX_CONNS must be a positive integer")
	}
	minConns, err := parseInt32(lookup, "DAYORDER_DB_MIN_CONNS", 2)
	if err != nil || minConns < 0 || minConns > maxConns {
		return Config{}, errors.New("DAYORDER_DB_MIN_CONNS must be between zero and DAYORDER_DB_MAX_CONNS")
	}

	database := DatabaseConfig{URL: databaseURL, MaxConns: maxConns, MinConns: minConns}
	durationTargets := []struct {
		key      string
		fallback time.Duration
		target   *time.Duration
	}{
		{"DAYORDER_DB_MAX_CONN_LIFETIME", 30 * time.Minute, &database.MaxConnLifetime},
		{"DAYORDER_DB_MAX_CONN_IDLE_TIME", 5 * time.Minute, &database.MaxConnIdleTime},
		{"DAYORDER_DB_STATEMENT_TIMEOUT", 5 * time.Second, &database.StatementTimeout},
		{"DAYORDER_DB_LOCK_TIMEOUT", 2 * time.Second, &database.LockTimeout},
		{"DAYORDER_DB_IDLE_TX_TIMEOUT", 10 * time.Second, &database.IdleTransactionTimeout},
		{"DAYORDER_DB_HEALTH_TIMEOUT", 3 * time.Second, &database.HealthTimeout},
	}
	for _, item := range durationTargets {
		parsed, parseErr := parseDuration(lookup, item.key, item.fallback)
		if parseErr != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", item.key)
		}
		*item.target = parsed
	}

	publicFallback := "http://127.0.0.1:8080"
	if environment == Production {
		publicFallback = ""
	}
	publicURL := strings.TrimSpace(valueOr(lookup, "DAYORDER_PUBLIC_URL", publicFallback))
	if publicURL == "" {
		return Config{}, errors.New("DAYORDER_PUBLIC_URL is required")
	}
	parsedPublicURL, err := parseOrigin(publicURL)
	if err != nil {
		return Config{}, fmt.Errorf("DAYORDER_PUBLIC_URL: %w", err)
	}
	if environment == Production && parsedPublicURL.Scheme != "https" {
		return Config{}, errors.New("DAYORDER_PUBLIC_URL must use HTTPS in production")
	}

	originsValue := strings.TrimSpace(valueOr(lookup, "DAYORDER_ALLOWED_ORIGINS", ""))
	if originsValue == "" && environment != Production {
		originsValue = "http://127.0.0.1:5173,http://localhost:5173"
	}
	allowedOrigins := make([]string, 0)
	for _, candidate := range strings.Split(originsValue, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		parsed, parseErr := parseOrigin(candidate)
		if parseErr != nil {
			return Config{}, fmt.Errorf("allowed origin %q: %w", candidate, parseErr)
		}
		if environment == Production && parsed.Scheme != "https" {
			return Config{}, fmt.Errorf("allowed origin %q must use HTTPS in production", candidate)
		}
		allowedOrigins = append(allowedOrigins, strings.TrimSuffix(parsed.String(), "/"))
	}

	hmacKey := valueOr(lookup, "DAYORDER_AUTH_HMAC_KEY", "")
	if hmacKey == "" && environment != Production {
		hmacKey = "development-only-hmac-key-change-before-production"
	}
	if len([]byte(hmacKey)) < 32 {
		return Config{}, errors.New("DAYORDER_AUTH_HMAC_KEY must contain at least 32 bytes")
	}

	address := strings.TrimSpace(valueOr(lookup, "DAYORDER_ADDR", "127.0.0.1:8080"))
	if err = validateListenAddress(address); err != nil {
		return Config{}, fmt.Errorf("DAYORDER_ADDR: %w", err)
	}
	metricsAddress := strings.TrimSpace(valueOr(lookup, "DAYORDER_METRICS_ADDR", "127.0.0.1:9090"))
	if err = validateListenAddress(metricsAddress); err != nil {
		return Config{}, fmt.Errorf("DAYORDER_METRICS_ADDR: %w", err)
	}

	return Config{
		Environment:    environment,
		Address:        address,
		MetricsAddress: metricsAddress,
		PublicURL:      strings.TrimSuffix(parsedPublicURL.String(), "/"),
		AllowedOrigins: allowedOrigins,
		AuthHMACKey:    []byte(hmacKey),
		Database:       database,
	}, nil
}

func validateListenAddress(value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return errors.New("must be a host:port listen address")
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return errors.New("must contain a port between 1 and 65535")
	}
	return nil
}

func valueOr(lookup LookupFunc, key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func parseInt32(lookup LookupFunc, key string, fallback int32) (int32, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	return int32(parsed), err
}

func parseDuration(lookup LookupFunc, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return time.ParseDuration(strings.TrimSpace(value))
}

func parseOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("origin must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("origin cannot include credentials, path, query, or fragment")
	}
	parsed.Path = ""
	return parsed, nil
}
