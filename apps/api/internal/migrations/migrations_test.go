package migrations

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestLatestVersionMatchesEmbeddedSchema(t *testing.T) {
	version, err := latestEmbeddedVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != LatestVersion {
		t.Fatalf("latest embedded version = %d, want %d", version, LatestVersion)
	}
}

func TestUpRejectsMissingDatabaseURL(t *testing.T) {
	err := Up("  ")
	if !errors.Is(err, ErrDatabaseURLRequired) {
		t.Fatalf("Up() error = %v, want ErrDatabaseURLRequired", err)
	}
}

func TestCurrentVersionMapsNilVersionToEmptyDatabase(t *testing.T) {
	version, dirty, exists, err := versionFrom(func() (uint, bool, error) {
		return 0, false, migrate.ErrNilVersion
	})
	if err != nil || version != 0 || dirty || exists {
		t.Fatalf("versionFrom(nil version) = (%d, %t, %t, %v), want empty clean database", version, dirty, exists, err)
	}
}

func TestRequireCurrentAcceptsCleanSchemasAtOrAboveTheEmbeddedFloor(t *testing.T) {
	tests := []struct {
		name    string
		version uint
		dirty   bool
		exists  bool
		want    error
	}{
		{name: "empty", want: ErrSchemaOutdated},
		{name: "outdated", version: LatestVersion - 1, exists: true, want: ErrSchemaOutdated},
		{name: "dirty", version: LatestVersion, dirty: true, exists: true, want: ErrSchemaDirty},
		{name: "current", version: LatestVersion, exists: true},
		{name: "clean newer", version: LatestVersion + 1, exists: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireVersion(test.version, test.dirty, test.exists)
			if !errors.Is(err, test.want) {
				t.Fatalf("requireVersion() error = %v, want %v", err, test.want)
			}
		})
	}
}
