package migrations

import (
	"testing"

	"dayorder.local/api/internal/testdb"
)

func TestUpgradeFromPreviousMigrationVersion(t *testing.T) {
	database := testdb.StartForTest(t)
	runner, err := open(database.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Steps(int(LatestVersion - 1)); err != nil {
		_, _ = runner.Close()
		t.Fatalf("apply previous migration versions: %v", err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close previous-version migrator: source=%v database=%v", sourceErr, databaseErr)
	}

	version, dirty, exists, err := CurrentVersion(database.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || dirty || version != LatestVersion-1 {
		t.Fatalf("previous schema state = version %d, dirty %t, exists %t", version, dirty, exists)
	}
	if err = Up(database.MigrationURL); err != nil {
		t.Fatalf("upgrade previous schema to latest: %v", err)
	}
	if err = RequireCurrent(database.MigrationURL); err != nil {
		t.Fatal(err)
	}
}
