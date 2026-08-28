package model

import (
	"time"

	"github.com/google/uuid"
)

type UserDevice struct {
	ID             uuid.UUID  `json:"id"`
	UserID         uuid.UUID  `json:"-"`
	DeviceName     string     `json:"deviceName"`
	Platform       string     `json:"platform"`
	LastSeenAt     time.Time  `json:"lastSeenAt"`
	LastSyncCursor int64      `json:"lastSyncCursor"`
	CreatedAt      time.Time  `json:"createdAt"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
}
