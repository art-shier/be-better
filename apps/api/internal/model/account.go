package model

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type AccountStatus string

const (
	AccountPendingVerification AccountStatus = "pending_verification"
	AccountActive              AccountStatus = "active"
	AccountDisabled            AccountStatus = "disabled"
	AccountDeletionPending     AccountStatus = "deletion_pending"
)

type Account struct {
	ID              uuid.UUID     `json:"id"`
	Email           string        `json:"email"`
	NormalizedEmail string        `json:"-"`
	DisplayName     string        `json:"displayName"`
	Status          AccountStatus `json:"status"`
	EmailVerifiedAt *time.Time    `json:"emailVerifiedAt,omitempty"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

type LoginAccount struct {
	Account
	PasswordHash string
}

type AccountTokenPurpose string

const (
	TokenVerifyEmail   AccountTokenPurpose = "verify_email"
	TokenResetPassword AccountTokenPurpose = "reset_password"
	TokenChangeEmail   AccountTokenPurpose = "change_email"
)

type AccountToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Purpose   AccountTokenPurpose
	TokenHash []byte
	ExpiresAt time.Time
	CreatedAt time.Time
}

type PendingAccountRegistration struct {
	Account           Account
	PasswordHash      string
	VerificationToken AccountToken
	OutboxID          uuid.UUID
	OutboxPayload     json.RawMessage
}

type AccountTokenDelivery struct {
	Token         AccountToken
	OutboxID      uuid.UUID
	EventType     string
	OutboxPayload json.RawMessage
}

type Session struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"-"`
	UserAgent  string    `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	RevokedAt  *time.Time
}

type NewSession struct {
	Session
	TokenHash []byte
}

type AuthenticatedSession struct {
	Session Session
	Account Account
}

type PasswordSessionRotation struct {
	UserID       uuid.UUID
	PasswordHash string
	Session      NewSession
}

type LoginThrottle struct {
	Failures     int
	BlockedUntil *time.Time
}
