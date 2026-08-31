package main

import (
	"errors"
	"testing"

	dbmigrations "dayorder.local/api/internal/migrations"
)

func TestValidateReadySchemaUsesTheEmbeddedVersionAsACompatibilityFloor(t *testing.T) {
	tests := []struct {
		name    string
		version uint
		dirty   bool
		want    error
	}{
		{name: "outdated", version: dbmigrations.LatestVersion - 1, want: dbmigrations.ErrSchemaOutdated},
		{name: "dirty current", version: dbmigrations.LatestVersion, dirty: true, want: dbmigrations.ErrSchemaDirty},
		{name: "current", version: dbmigrations.LatestVersion},
		{name: "clean newer", version: dbmigrations.LatestVersion + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateReadySchema(test.version, test.dirty)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateReadySchema() error = %v, want %v", err, test.want)
			}
		})
	}
}
