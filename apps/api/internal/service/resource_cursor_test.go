package service

import (
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

func TestResourceCursorIsOpaqueUserAndFilterBound(t *testing.T) {
	codec, err := NewResourceCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	position := model.ResourcePosition{UpdatedAt: time.Now().UTC(), ID: uuid.New()}
	cursor, err := codec.Encode(userID, "tasks:todo", position)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(userID, "tasks:todo", cursor)
	if err != nil || decoded.ID != position.ID || !decoded.UpdatedAt.Equal(position.UpdatedAt) {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}
	for _, test := range []struct {
		user  uuid.UUID
		scope string
	}{
		{uuid.New(), "tasks:todo"}, {userID, "tasks:done"}, {userID, "goals"},
	} {
		if _, err = codec.Decode(test.user, test.scope, cursor); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("Decode(%s, %q) error = %v", test.user, test.scope, err)
		}
	}
}
