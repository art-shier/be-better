package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const resourceCursorPayloadSize = 1 + 16 + 8 + 16 + 8

var ErrInvalidCursor = errors.New("INVALID_CURSOR")

type ResourceCursorCodec struct{ hmacKey []byte }

func NewResourceCursorCodec(hmacKey []byte) (*ResourceCursorCodec, error) {
	if len(hmacKey) < 32 {
		return nil, errors.New("resource cursor HMAC key must contain at least 32 bytes")
	}
	return &ResourceCursorCodec{hmacKey: append([]byte(nil), hmacKey...)}, nil
}

func (codec *ResourceCursorCodec) Encode(userID uuid.UUID, scope string, position model.ResourcePosition) (string, error) {
	if codec == nil || userID == uuid.Nil || scope == "" || position.ID == uuid.Nil || position.UpdatedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid resource cursor values", ErrInvalidCursor)
	}
	payload := make([]byte, resourceCursorPayloadSize)
	payload[0] = 1
	copy(payload[1:17], userID[:])
	binary.BigEndian.PutUint64(payload[17:25], uint64(position.UpdatedAt.UTC().UnixNano()))
	copy(payload[25:41], position.ID[:])
	scopeHash := sha256.Sum256([]byte(scope))
	copy(payload[41:49], scopeHash[:8])
	mac := hmac.New(sha256.New, codec.hmacKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...)), nil
}

func (codec *ResourceCursorCodec) Decode(userID uuid.UUID, scope, cursor string) (model.ResourcePosition, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) != resourceCursorPayloadSize+sha256.Size {
		return model.ResourcePosition{}, ErrInvalidCursor
	}
	payload := decoded[:resourceCursorPayloadSize]
	mac := hmac.New(sha256.New, codec.hmacKey)
	_, _ = mac.Write(payload)
	if payload[0] != 1 || !hmac.Equal(decoded[resourceCursorPayloadSize:], mac.Sum(nil)) {
		return model.ResourcePosition{}, ErrInvalidCursor
	}
	var cursorUserID, resourceID uuid.UUID
	copy(cursorUserID[:], payload[1:17])
	copy(resourceID[:], payload[25:41])
	scopeHash := sha256.Sum256([]byte(scope))
	if cursorUserID != userID || !hmac.Equal(payload[41:49], scopeHash[:8]) {
		return model.ResourcePosition{}, ErrInvalidCursor
	}
	timestamp := binary.BigEndian.Uint64(payload[17:25])
	if timestamp > math.MaxInt64 {
		return model.ResourcePosition{}, ErrInvalidCursor
	}
	return model.ResourcePosition{UpdatedAt: time.Unix(0, int64(timestamp)).UTC(), ID: resourceID}, nil
}
