package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const (
	syncCursorVersion    = byte(1)
	syncCursorPayload    = 1 + 16 + 8 + 8
	syncCursorSignature  = sha256.Size
	defaultSyncRetention = 90 * 24 * time.Hour
	maxSyncPageSize      = 500
)

var (
	ErrInvalidSyncCursor = errors.New("INVALID_SYNC_CURSOR")
	ErrSyncResetRequired = errors.New("SYNC_RESET_REQUIRED")
)

var syncEntityTypes = map[string]struct{}{
	"goal": {}, "milestone": {}, "task": {}, "calendar_event": {}, "reminder": {},
	"record": {}, "note": {}, "daily_review": {}, "tag": {}, "agent_run": {},
	"agent_change": {}, "settings": {},
}

type UserTransactor interface {
	WithUser(context.Context, uuid.UUID, func(context.Context, database.Tx) error) error
}

type SyncStore interface {
	Append(context.Context, database.Tx, uuid.UUID, model.SyncChangeDraft) (model.SyncChange, error)
	CurrentCursor(context.Context, database.Tx, uuid.UUID) (int64, error)
	List(context.Context, database.Tx, uuid.UUID, int64, int) ([]model.SyncChange, error)
	Resolve(context.Context, database.Tx, uuid.UUID, model.SyncChange) ([]byte, error)
	RequireActiveDevice(context.Context, database.Tx, uuid.UUID, uuid.UUID) error
	AdvanceDeviceCursor(context.Context, database.Tx, uuid.UUID, uuid.UUID, int64) error
}

type SyncPage struct {
	Changes    []model.SyncChange `json:"changes"`
	NextCursor string             `json:"nextCursor"`
	HasMore    bool               `json:"hasMore"`
}

type SyncBootstrap struct {
	Cursor string `json:"cursor"`
}

type SyncService struct {
	store      SyncStore
	transactor UserTransactor
	hmacKey    []byte
	now        func() time.Time
	retention  time.Duration
}

func NewSyncService(store SyncStore, transactor UserTransactor, hmacKey []byte) (*SyncService, error) {
	if store == nil {
		return nil, errors.New("sync store is required")
	}
	if transactor == nil {
		return nil, errors.New("sync transactor is required")
	}
	if len(hmacKey) < 32 {
		return nil, errors.New("sync cursor HMAC key must contain at least 32 bytes")
	}
	return &SyncService{
		store: store, transactor: transactor, hmacKey: append([]byte(nil), hmacKey...),
		now: time.Now, retention: defaultSyncRetention,
	}, nil
}

func (service *SyncService) Record(
	ctx context.Context,
	tx database.Tx,
	userID uuid.UUID,
	changes []model.SyncChangeDraft,
) error {
	if service == nil || service.store == nil {
		return errors.New("sync service is required")
	}
	if tx == nil || userID == uuid.Nil {
		return fmt.Errorf("%w: sync transaction and user are required", ErrValidation)
	}
	for index, change := range changes {
		if err := validateSyncChange(change); err != nil {
			return fmt.Errorf("validate sync change %d: %w", index, err)
		}
	}
	for _, change := range changes {
		if _, err := service.store.Append(ctx, tx, userID, change); err != nil {
			return fmt.Errorf("append sync change: %w", err)
		}
	}
	return nil
}

func (service *SyncService) CurrentCursor(ctx context.Context, userID uuid.UUID) (string, error) {
	if service == nil || service.transactor == nil || userID == uuid.Nil {
		return "", fmt.Errorf("%w: sync user is required", ErrValidation)
	}
	var sequence int64
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		sequence, readErr = service.store.CurrentCursor(ctx, tx, userID)
		return readErr
	})
	if err != nil {
		return "", fmt.Errorf("read current sync cursor: %w", err)
	}
	return service.encodeCursor(userID, sequence, service.now().UTC())
}

func (service *SyncService) Bootstrap(ctx context.Context, userID, deviceID uuid.UUID) (SyncBootstrap, error) {
	if service == nil || service.transactor == nil || userID == uuid.Nil || deviceID == uuid.Nil {
		return SyncBootstrap{}, fmt.Errorf("%w: sync user and device are required", ErrValidation)
	}
	var sequence int64
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		if requireErr := service.store.RequireActiveDevice(ctx, tx, userID, deviceID); requireErr != nil {
			return requireErr
		}
		var readErr error
		sequence, readErr = service.store.CurrentCursor(ctx, tx, userID)
		if readErr != nil {
			return readErr
		}
		return service.store.AdvanceDeviceCursor(ctx, tx, userID, deviceID, sequence)
	})
	if err != nil {
		return SyncBootstrap{}, fmt.Errorf("prepare sync bootstrap: %w", err)
	}
	cursor, err := service.encodeCursor(userID, sequence, service.now().UTC())
	if err != nil {
		return SyncBootstrap{}, err
	}
	return SyncBootstrap{Cursor: cursor}, nil
}

func (service *SyncService) Changes(
	ctx context.Context,
	userID uuid.UUID,
	cursor string,
	pageSize int,
) (SyncPage, error) {
	return service.changes(ctx, userID, uuid.Nil, cursor, pageSize)
}

func (service *SyncService) DeviceChanges(
	ctx context.Context,
	userID uuid.UUID,
	deviceID uuid.UUID,
	cursor string,
	pageSize int,
) (SyncPage, error) {
	if deviceID == uuid.Nil {
		return SyncPage{}, fmt.Errorf("%w: sync device is required", ErrValidation)
	}
	return service.changes(ctx, userID, deviceID, cursor, pageSize)
}

func (service *SyncService) changes(
	ctx context.Context,
	userID uuid.UUID,
	deviceID uuid.UUID,
	cursor string,
	pageSize int,
) (SyncPage, error) {
	if service == nil || service.transactor == nil || userID == uuid.Nil {
		return SyncPage{}, fmt.Errorf("%w: sync user is required", ErrValidation)
	}
	if pageSize < 1 || pageSize > maxSyncPageSize {
		return SyncPage{}, fmt.Errorf("%w: sync page size must be between 1 and %d", ErrValidation, maxSyncPageSize)
	}
	sequence, issuedAt, err := service.decodeCursor(userID, cursor)
	if err != nil {
		return SyncPage{}, err
	}
	now := service.now().UTC()
	if now.Sub(issuedAt) > service.retention {
		return SyncPage{}, ErrSyncResetRequired
	}
	var changes []model.SyncChange
	err = service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		if deviceID != uuid.Nil {
			if requireErr := service.store.RequireActiveDevice(ctx, tx, userID, deviceID); requireErr != nil {
				return requireErr
			}
		}
		var readErr error
		changes, readErr = service.store.List(ctx, tx, userID, sequence, pageSize+1)
		if readErr != nil {
			return readErr
		}
		visible := changes
		if len(visible) > pageSize {
			visible = visible[:pageSize]
		}
		for index := range visible {
			if visible[index].Operation == "delete" {
				continue
			}
			data, resolveErr := service.store.Resolve(ctx, tx, userID, visible[index])
			if errors.Is(resolveErr, model.ErrNotFound) {
				visible[index].Operation = "delete"
				visible[index].Data = nil
				continue
			}
			if resolveErr != nil {
				return fmt.Errorf("resolve sync entity: %w", resolveErr)
			}
			var object map[string]any
			if jsonErr := json.Unmarshal(data, &object); jsonErr != nil || object == nil {
				return fmt.Errorf("%w: resolved sync entity must be a JSON object", ErrValidation)
			}
			visible[index].Data = append([]byte(nil), data...)
		}
		if deviceID == uuid.Nil {
			return nil
		}
		nextSequence := sequence
		if len(visible) > 0 {
			nextSequence = visible[len(visible)-1].Sequence
		}
		return service.store.AdvanceDeviceCursor(ctx, tx, userID, deviceID, nextSequence)
	})
	if err != nil {
		return SyncPage{}, fmt.Errorf("list sync changes: %w", err)
	}
	hasMore := len(changes) > pageSize
	if hasMore {
		changes = changes[:pageSize]
	}
	nextSequence := sequence
	if len(changes) > 0 {
		nextSequence = changes[len(changes)-1].Sequence
	}
	nextCursor, err := service.encodeCursor(userID, nextSequence, now)
	if err != nil {
		return SyncPage{}, err
	}
	return SyncPage{Changes: changes, NextCursor: nextCursor, HasMore: hasMore}, nil
}

func validateSyncChange(change model.SyncChangeDraft) error {
	if _, ok := syncEntityTypes[change.EntityType]; !ok {
		return fmt.Errorf("%w: invalid sync entity type", ErrValidation)
	}
	if change.EntityID == uuid.Nil {
		return fmt.Errorf("%w: sync entity ID is required", ErrValidation)
	}
	if change.Operation != "create" && change.Operation != "update" && change.Operation != "delete" {
		return fmt.Errorf("%w: invalid sync operation", ErrValidation)
	}
	if change.EntityVersion < 1 {
		return fmt.Errorf("%w: sync entity version must be positive", ErrValidation)
	}
	return nil
}

func (service *SyncService) encodeCursor(userID uuid.UUID, sequence int64, issuedAt time.Time) (string, error) {
	if userID == uuid.Nil || sequence < 0 || issuedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid sync cursor values", ErrInvalidSyncCursor)
	}
	payload := make([]byte, syncCursorPayload)
	payload[0] = syncCursorVersion
	copy(payload[1:17], userID[:])
	binary.BigEndian.PutUint64(payload[17:25], uint64(sequence))
	binary.BigEndian.PutUint64(payload[25:33], uint64(issuedAt.UTC().Unix()))
	mac := hmac.New(sha256.New, service.hmacKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...)), nil
}

func (service *SyncService) decodeCursor(userID uuid.UUID, cursor string) (int64, time.Time, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) != syncCursorPayload+syncCursorSignature {
		return 0, time.Time{}, ErrInvalidSyncCursor
	}
	payload := decoded[:syncCursorPayload]
	signature := decoded[syncCursorPayload:]
	mac := hmac.New(sha256.New, service.hmacKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) || payload[0] != syncCursorVersion {
		return 0, time.Time{}, ErrInvalidSyncCursor
	}
	var cursorUserID uuid.UUID
	copy(cursorUserID[:], payload[1:17])
	sequence := binary.BigEndian.Uint64(payload[17:25])
	issuedUnix := binary.BigEndian.Uint64(payload[25:33])
	if cursorUserID != userID || sequence > math.MaxInt64 || issuedUnix > math.MaxInt64 {
		return 0, time.Time{}, ErrInvalidSyncCursor
	}
	issuedAt := time.Unix(int64(issuedUnix), 0).UTC()
	if issuedAt.After(service.now().UTC().Add(5 * time.Minute)) {
		return 0, time.Time{}, ErrInvalidSyncCursor
	}
	return int64(sequence), issuedAt, nil
}
