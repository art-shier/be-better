package postgres

import (
	"bytes"
	"context"
	"errors"

	"dayorder.local/api/internal/database"
	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SettingsRepository struct{}

func NewSettingsRepository() *SettingsRepository { return &SettingsRepository{} }

func (*SettingsRepository) Get(ctx context.Context, tx database.Tx, userID uuid.UUID) (model.UserSettings, error) {
	row, err := db.New(tx).GetUserSettings(ctx, pgUUID(userID))
	if err != nil {
		return model.UserSettings{}, mapDatabaseError("get user settings", err)
	}
	return settingsFromRow(row), nil
}
func (*SettingsRepository) Update(ctx context.Context, tx database.Tx, userID uuid.UUID, schemaVersion int, settings []byte, expected int64) (model.UserSettings, error) {
	q := db.New(tx)
	row, err := q.UpsertUserSettings(ctx, pgUUID(userID), int32(schemaVersion), bytes.Clone(settings), expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UserSettings{}, model.ErrConflict
	}
	if err != nil {
		return model.UserSettings{}, mapDatabaseError("update user settings", err)
	}
	return settingsFromRow(row), nil
}
func settingsFromRow(row *db.DayorderUserSetting) model.UserSettings {
	return model.UserSettings{SchemaVersion: int(row.SchemaVersion), Version: row.Version, Settings: bytes.Clone(row.Settings), UpdatedAt: row.UpdatedAt.Time.UTC()}
}
