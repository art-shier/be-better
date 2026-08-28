package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	dbmigrations "dayorder.local/api/internal/migrations"
)

func main() {
	databaseURL := flag.String("database-url", "", "PostgreSQL migration connection URL (defaults to MIGRATION_DATABASE_URL)")
	check := flag.Bool("check", false, "verify that the database schema is at the required version")
	flag.Parse()

	resolvedURL := strings.TrimSpace(*databaseURL)
	if resolvedURL == "" {
		resolvedURL = strings.TrimSpace(os.Getenv("MIGRATION_DATABASE_URL"))
	}

	var err error
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
