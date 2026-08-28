package model

import (
	"encoding/json"
	"time"
)

type UserSettings struct {
	SchemaVersion int             `json:"schemaVersion"`
	Version       int64           `json:"version"`
	Settings      json.RawMessage `json:"settings"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}
