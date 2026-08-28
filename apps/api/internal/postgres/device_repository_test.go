package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeviceRepositoryRegistersIdempotentlyAndRejectsRevokedDevice(t *testing.T) {
	databaseFixture := testdb.StartForTest(t)
	if err := dbmigrations.Up(databaseFixture.MigrationURL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	apiPool, err := database.Open(ctx, testDatabaseConfig(databaseFixture.APIURL))
	if err != nil {
		t.Fatal(err)
	}
	defer apiPool.Close()
	migrationPool, err := pgxpool.New(ctx, databaseFixture.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationPool.Close()
	userID := uuid.New()
	if _, err = migrationPool.Exec(ctx, `
INSERT INTO dayorder.users (id, email, normalized_email, display_name, password_hash, status, email_verified_at)
VALUES ($1, 'device@example.com', 'device@example.com', 'Device User', 'test-password-hash', 'active', now())
`, userID); err != nil {
		t.Fatal(err)
	}

	transactor, _ := database.NewPoolTransactor(apiPool)
	auditService, _ := service.NewAuditService(postgresstore.NewAuditRepository())
	deviceService, err := service.NewDeviceService(postgresstore.NewDeviceRepository(), transactor, auditService)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := uuid.New()
	first, err := deviceService.Register(ctx, userID, deviceID, service.RegisterDeviceInput{DeviceName: "Browser", Platform: "web"})
	if err != nil || !first.Created {
		t.Fatalf("first registration = %#v, %v", first, err)
	}
	second, err := deviceService.Register(ctx, userID, deviceID, service.RegisterDeviceInput{DeviceName: "Renamed", Platform: "web"})
	if err != nil || second.Created || second.Device.DeviceName != "Renamed" {
		t.Fatalf("second registration = %#v, %v", second, err)
	}
	devices, err := deviceService.List(ctx, userID)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices = %#v, %v", devices, err)
	}
	if _, err = migrationPool.Exec(ctx, "UPDATE dayorder.user_devices SET revoked_at = now() WHERE id = $1", deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = deviceService.Register(ctx, userID, deviceID, service.RegisterDeviceInput{DeviceName: "Browser", Platform: "web"}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("revoked registration error = %v, want conflict", err)
	}
}
