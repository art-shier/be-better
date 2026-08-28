package migrations

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strconv"
	"strings"

	schema "dayorder.local/api/migrations"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const LatestVersion uint = 4

var (
	ErrDatabaseURLRequired = errors.New("migration database URL is required")
	ErrDatabaseURLInvalid  = errors.New("migration database URL must be a valid PostgreSQL URL")
	ErrSchemaDirty         = errors.New("database schema migration is dirty")
	ErrSchemaOutdated      = errors.New("database schema migration is not current")
	ErrSchemaUnsupported   = errors.New("database schema is newer than this application")
)

func Up(databaseURL string) (returnErr error) {
	runner, err := open(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		sourceErr, databaseErr := runner.Close()
		returnErr = errors.Join(returnErr, sourceErr, databaseErr)
	}()

	if err = runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}

func CurrentVersion(databaseURL string) (version uint, dirty bool, exists bool, returnErr error) {
	runner, err := open(databaseURL)
	if err != nil {
		return 0, false, false, err
	}
	defer func() {
		sourceErr, databaseErr := runner.Close()
		returnErr = errors.Join(returnErr, sourceErr, databaseErr)
	}()

	return versionFrom(runner.Version)
}

func RequireCurrent(databaseURL string) error {
	version, dirty, exists, err := CurrentVersion(databaseURL)
	if err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	return requireVersion(version, dirty, exists)
}

func open(databaseURL string) (*migrate.Migrate, error) {
	if err := validateDatabaseURL(databaseURL); err != nil {
		return nil, err
	}
	source, err := iofs.New(schema.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	runner, err := migrate.NewWithSourceInstance("iofs", source, strings.TrimSpace(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("initialize database migrator: %w", err)
	}
	return runner, nil
}

func validateDatabaseURL(databaseURL string) error {
	trimmed := strings.TrimSpace(databaseURL)
	if trimmed == "" {
		return ErrDatabaseURLRequired
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return ErrDatabaseURLInvalid
	}
	return nil
}

func latestEmbeddedVersion() (uint, error) {
	entries, err := fs.Glob(schema.FS, "*.up.sql")
	if err != nil {
		return 0, fmt.Errorf("list embedded migrations: %w", err)
	}
	if len(entries) == 0 {
		return 0, errors.New("no embedded database migrations")
	}
	value, err := strconv.ParseUint(entries[len(entries)-1][:6], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse latest embedded migration version: %w", err)
	}
	return uint(value), nil
}

func versionFrom(read func() (uint, bool, error)) (uint, bool, bool, error) {
	version, dirty, err := read()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	return version, dirty, true, nil
}

func requireVersion(version uint, dirty bool, exists bool) error {
	if dirty {
		return fmt.Errorf("%w at version %d", ErrSchemaDirty, version)
	}
	if !exists || version < LatestVersion {
		return fmt.Errorf("%w: have %d, require %d", ErrSchemaOutdated, version, LatestVersion)
	}
	if version > LatestVersion {
		return fmt.Errorf("%w: have %d, support %d", ErrSchemaUnsupported, version, LatestVersion)
	}
	return nil
}
