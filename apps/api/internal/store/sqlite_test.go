package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func createTestAccount(t *testing.T, storage *SQLiteStore, id, email string, data json.RawMessage) (User, Session, State) {
	t.Helper()
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	user, session, state, err := storage.CreateAccount(context.Background(), CreateAccountParams{
		UserID: id, Email: email, DisplayName: id, PasswordHash: "hash",
		SessionID: "session_" + id, TokenHash: []byte("hash_" + id),
		StateData: data, Now: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return user, session, state
}

func TestCreateAccountIsAtomicAndEmailIsUnique(t *testing.T) {
	storage, err := Open(filepath.Join(t.TempDir(), "dayorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	createTestAccount(t, storage, "user_one", "one@example.com", json.RawMessage(`{"version":1,"goals":[]}`))

	_, _, _, err = storage.CreateAccount(context.Background(), CreateAccountParams{
		UserID: "user_two", Email: "ONE@example.com", DisplayName: "Two", PasswordHash: "hash",
		SessionID: "session_two", TokenHash: []byte("hash_two"), StateData: json.RawMessage(`{"version":1}`),
		Now: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Fatalf("expected duplicate email, got %v", err)
	}
	if _, err = storage.UserByID(context.Background(), "user_two"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("registration left a user behind: %v", err)
	}
	if _, err = storage.LoadUserState(context.Background(), "user_two"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("registration left state behind: %v", err)
	}
}

func TestUserStateIsIsolatedAndConflictsPerUser(t *testing.T) {
	storage, err := Open(filepath.Join(t.TempDir(), "dayorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	ctx := context.Background()
	createTestAccount(t, storage, "user_one", "one@example.com", json.RawMessage(`{"version":1,"owner":"one"}`))
	createTestAccount(t, storage, "user_two", "two@example.com", json.RawMessage(`{"version":1,"owner":"two"}`))

	one, err := storage.SaveUserState(ctx, "user_one", json.RawMessage(`{"version":1,"owner":"one-updated"}`), 1)
	if err != nil || one.Revision != 2 {
		t.Fatalf("save user one: %#v %v", one, err)
	}
	two, err := storage.LoadUserState(ctx, "user_two")
	if err != nil || two.Revision != 1 || string(two.Data) != `{"version":1,"owner":"two"}` {
		t.Fatalf("user two was affected: %#v %v", two, err)
	}
	if _, err = storage.SaveUserState(ctx, "user_one", json.RawMessage(`{"version":1}`), 1); err == nil {
		t.Fatal("expected per-user revision conflict")
	}
	if _, err = storage.LoadUserState(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestSessionExpiryAndPasswordRotation(t *testing.T) {
	storage, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	createTestAccount(t, storage, "user_one", "one@example.com", json.RawMessage(`{"version":1}`))
	if _, _, err = storage.SessionUser(context.Background(), []byte("hash_user_one"), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = storage.SessionUser(context.Background(), []byte("hash_user_one"), now.Add(31*24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session should fail: %v", err)
	}

	_, err = storage.CreateSession(context.Background(), "session_other", "user_one", []byte("other"), "test", now, now.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := storage.RotatePasswordSession(context.Background(), "user_one", "new-hash", "session_new", []byte("new"), "test", now, now.Add(30*24*time.Hour))
	if err != nil || rotated.ID != "session_new" {
		t.Fatalf("rotate: %#v %v", rotated, err)
	}
	if _, _, err = storage.SessionUser(context.Background(), []byte("other"), now); !errors.Is(err, ErrNotFound) {
		t.Fatal("other session was not revoked")
	}
	if _, _, err = storage.SessionUser(context.Background(), []byte("new"), now); err != nil {
		t.Fatal("rotated session is not active")
	}
}
