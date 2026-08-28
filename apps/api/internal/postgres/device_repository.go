package postgres

import (
	"context"
	"errors"
	"fmt"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DeviceRepository struct{}

func NewDeviceRepository() *DeviceRepository { return &DeviceRepository{} }

func (*DeviceRepository) Register(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	device model.UserDevice,
) (model.UserDevice, bool, error) {
	queries := db.New(tx)
	row, err := queries.CreateUserDevice(
		ctx, pgUUID(device.ID), pgUUID(userID), device.DeviceName, device.Platform,
	)
	if err == nil {
		return userDeviceFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.UserDevice{}, false, mapDatabaseError("create user device", err)
	}
	row, err = queries.RefreshUserDevice(
		ctx, device.DeviceName, device.Platform, pgUUID(userID), pgUUID(device.ID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UserDevice{}, false, model.ErrConflict
	}
	if err != nil {
		return model.UserDevice{}, false, mapDatabaseError("refresh user device", err)
	}
	return userDeviceFromRow(row), false, nil
}

func (*DeviceRepository) List(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
) ([]model.UserDevice, error) {
	rows, err := db.New(tx).ListUserDevices(ctx, pgUUID(userID))
	if err != nil {
		return nil, fmt.Errorf("list user devices: %w", err)
	}
	devices := make([]model.UserDevice, 0, len(rows))
	for _, row := range rows {
		devices = append(devices, userDeviceFromRow(row))
	}
	return devices, nil
}

func userDeviceFromRow(row *db.DayorderUserDevice) model.UserDevice {
	return model.UserDevice{
		ID: uuid.UUID(row.ID.Bytes), UserID: uuid.UUID(row.UserID.Bytes),
		DeviceName: row.DeviceName, Platform: row.Platform,
		LastSeenAt: row.LastSeenAt.Time.UTC(), LastSyncCursor: row.LastSyncCursor,
		CreatedAt: row.CreatedAt.Time.UTC(), RevokedAt: optionalTime(row.RevokedAt),
	}
}
