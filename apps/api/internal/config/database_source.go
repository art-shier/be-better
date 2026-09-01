package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type DatabaseRole string

const (
	DatabaseRoleMigrator DatabaseRole = "dayorder_migrator"
	DatabaseRoleAPI      DatabaseRole = "dayorder_api"
	DatabaseRoleWorker   DatabaseRole = "dayorder_worker"
)

var configHubDatabaseKeys = [...]string{
	"db_address",
	"db_port",
	"db_username",
	"db_password",
	"db_migrator_password",
	"db_api_password",
	"db_worker_password",
}

type ConfigHubDatabaseSource struct {
	Address          string
	Port             uint16
	AdminUsername    string
	AdminPassword    string
	MigratorPassword string
	APIPassword      string
	WorkerPassword   string
}

func LoadConfigHubDatabaseSource(lookup LookupFunc) (ConfigHubDatabaseSource, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	address, err := requiredTrimmedValue(lookup, "db_address")
	if err != nil || !validDatabaseAddress(address) {
		return ConfigHubDatabaseSource{}, errors.New("db_address is required and must be a host or IP address")
	}
	portValue, err := requiredTrimmedValue(lookup, "db_port")
	if err != nil {
		return ConfigHubDatabaseSource{}, err
	}
	parsedPort, err := strconv.ParseUint(portValue, 10, 16)
	if err != nil || parsedPort == 0 {
		return ConfigHubDatabaseSource{}, errors.New("db_port must be an integer between 1 and 65535")
	}
	username, err := requiredTrimmedValue(lookup, "db_username")
	if err != nil {
		return ConfigHubDatabaseSource{}, err
	}
	adminPassword, err := requiredPassword(lookup, "db_password")
	if err != nil {
		return ConfigHubDatabaseSource{}, err
	}
	migratorPassword, err := requiredPassword(lookup, "db_migrator_password")
	if err != nil {
		return ConfigHubDatabaseSource{}, err
	}
	apiPassword, err := requiredPassword(lookup, "db_api_password")
	if err != nil {
		return ConfigHubDatabaseSource{}, err
	}
	workerPassword, err := requiredPassword(lookup, "db_worker_password")
	if err != nil {
		return ConfigHubDatabaseSource{}, err
	}
	for _, role := range []DatabaseRole{DatabaseRoleMigrator, DatabaseRoleAPI, DatabaseRoleWorker} {
		if strings.EqualFold(username, string(role)) {
			return ConfigHubDatabaseSource{}, errors.New("db_username must not match a DayOrder runtime role")
		}
	}
	passwords := []struct {
		key   string
		value string
	}{
		{key: "db_password", value: adminPassword},
		{key: "db_migrator_password", value: migratorPassword},
		{key: "db_api_password", value: apiPassword},
		{key: "db_worker_password", value: workerPassword},
	}
	for first := range passwords {
		for second := first + 1; second < len(passwords); second++ {
			if passwords[first].value == passwords[second].value {
				return ConfigHubDatabaseSource{}, fmt.Errorf("%s and %s must use different values", passwords[first].key, passwords[second].key)
			}
		}
	}

	return ConfigHubDatabaseSource{
		Address:          address,
		Port:             uint16(parsedPort),
		AdminUsername:    username,
		AdminPassword:    adminPassword,
		MigratorPassword: migratorPassword,
		APIPassword:      apiPassword,
		WorkerPassword:   workerPassword,
	}, nil
}

func DatabaseName(environment Environment) (string, error) {
	switch environment {
	case Development:
		return "dayorder-test", nil
	case Production:
		return "dayorder", nil
	default:
		return "", errors.New("ConfigHub database fallback is unavailable for this environment")
	}
}

func (source ConfigHubDatabaseSource) AdminURL(database string) string {
	return buildPostgresURL(source.Address, source.Port, source.AdminUsername, source.AdminPassword, database, false)
}

func (source ConfigHubDatabaseSource) RoleURL(environment Environment, role DatabaseRole) (string, error) {
	database, err := DatabaseName(environment)
	if err != nil {
		return "", err
	}

	var password string
	searchPath := false
	switch role {
	case DatabaseRoleMigrator:
		password = source.MigratorPassword
		searchPath = true
	case DatabaseRoleAPI:
		password = source.APIPassword
	case DatabaseRoleWorker:
		password = source.WorkerPassword
	default:
		return "", errors.New("unsupported database role")
	}

	return buildPostgresURL(source.Address, source.Port, string(role), password, database, searchPath), nil
}

func ResolveDatabaseURL(lookup LookupFunc, environment Environment, explicitKey string, role DatabaseRole) (string, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	if value, ok := lookup(explicitKey); ok && strings.TrimSpace(value) != "" {
		candidate := strings.TrimSpace(value)
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			return "", fmt.Errorf("%s must be a valid PostgreSQL URL", explicitKey)
		}
		return candidate, nil
	}

	source, err := LoadConfigHubDatabaseSource(lookup)
	if err != nil {
		return "", fmt.Errorf("%s or ConfigHub database source: %w", explicitKey, err)
	}
	return source.RoleURL(environment, role)
}

func ScrubConfigHubDatabaseEnvironment() {
	for _, key := range configHubDatabaseKeys {
		_ = os.Unsetenv(key)
	}
}

func buildPostgresURL(address string, port uint16, username, password, database string, searchPath bool) string {
	query := url.Values{"sslmode": {"require"}, "timezone": {"UTC"}}
	if searchPath {
		query.Set("search_path", "dayorder")
	}
	return (&url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10)),
		Path:     "/" + database,
		RawQuery: query.Encode(),
	}).String()
}

func requiredTrimmedValue(lookup LookupFunc, key string) (string, error) {
	value, ok := lookup(key)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func requiredPassword(lookup LookupFunc, key string) (string, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func validDatabaseAddress(address string) bool {
	if net.ParseIP(address) != nil {
		return true
	}
	if strings.ContainsAny(address, ":/\\@?#") {
		return false
	}
	for _, character := range address {
		if unicode.IsSpace(character) || !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '.' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}
