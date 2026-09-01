package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"dayorder.local/api/internal/config"
	dbmigrations "dayorder.local/api/internal/migrations"
)

func main() {
	databaseURL := flag.String("database-url", "", "PostgreSQL migration connection URL (defaults to MIGRATION_DATABASE_URL)")
	check := flag.Bool("check", false, "verify that the database schema is at the required version")
	flag.Parse()

	resolvedURL, err := resolveDatabaseURL(*databaseURL, os.LookupEnv)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "dayorder migration configuration failed: %v\n", err)
		os.Exit(1)
	}
	config.ScrubConfigHubDatabaseEnvironment()

	if *check {
		err = dbmigrations.RequireCurrent(resolvedURL)
	} else {
		err = dbmigrations.Up(resolvedURL)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "dayorder migration failed: %v\n", err)
		os.Exit(1)
	}

	if *check {
		_, _ = fmt.Fprintf(os.Stdout, "database schema is current at version %d\n", dbmigrations.LatestVersion)
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "database migrations applied through version %d\n", dbmigrations.LatestVersion)
}

func resolveDatabaseURL(flagValue string, lookup config.LookupFunc) (string, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	if strings.TrimSpace(flagValue) != "" {
		flagLookup := func(key string) (string, bool) {
			if key == "-database-url" {
				return flagValue, true
			}
			return "", false
		}
		return config.ResolveDatabaseURL(flagLookup, config.Development, "-database-url", config.DatabaseRoleMigrator)
	}

	environment := config.Development
	if value, ok := lookup("DAYORDER_ENV"); ok && strings.TrimSpace(value) != "" {
		environment = config.Environment(strings.TrimSpace(value))
	}
	if environment != config.Development && environment != config.Test && environment != config.Production {
		return "", fmt.Errorf("DAYORDER_ENV must be development, test, or production")
	}
	return config.ResolveDatabaseURL(lookup, environment, "MIGRATION_DATABASE_URL", config.DatabaseRoleMigrator)
}
