package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/dbbootstrap"
)

type options struct {
	Preflight bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "dayorder database Bootstrap failed: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	parsed, err := parseOptions(arguments)
	if err != nil {
		return err
	}

	source, loadErr := config.LoadConfigHubDatabaseSource(os.LookupEnv)
	config.ScrubConfigHubDatabaseEnvironment()
	if loadErr != nil {
		return fmt.Errorf("load ConfigHub database source: %w", loadErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if parsed.Preflight {
		if err = dbbootstrap.Preflight(ctx, source); err != nil {
			return fmt.Errorf("preflight PostgreSQL: %w", err)
		}
		_, err = fmt.Fprintln(output, "preflight ok: PostgreSQL TLS and administrator capabilities verified")
		return err
	}

	result, err := dbbootstrap.Run(ctx, source)
	if err != nil {
		return fmt.Errorf("Bootstrap PostgreSQL: %w", err)
	}
	return writeBootstrapResult(output, result)
}

func parseOptions(arguments []string) (options, error) {
	flagSet := flag.NewFlagSet("dayorder-database-bootstrap", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	var parsed options
	flagSet.BoolVar(&parsed.Preflight, "preflight", false, "verify PostgreSQL without changing it")
	if err := flagSet.Parse(arguments); err != nil {
		return options{}, errors.New("arguments must contain only the optional -preflight flag")
	}
	if flagSet.NArg() != 0 {
		return options{}, errors.New("positional arguments are not supported")
	}
	return parsed, nil
}

func writeBootstrapResult(output io.Writer, result dbbootstrap.Result) error {
	if len(result.Databases) != 2 || result.Databases[0].Name != "dayorder-test" || result.Databases[1].Name != "dayorder" {
		return errors.New("Bootstrap returned unexpected database targets")
	}
	for _, database := range result.Databases {
		status := "existing"
		if database.Created {
			status = "created"
		}
		if _, err := fmt.Fprintf(output, "database %s: %s, schema version %d\n", database.Name, status, database.Version); err != nil {
			return errors.New("write Bootstrap result")
		}
	}
	return nil
}
