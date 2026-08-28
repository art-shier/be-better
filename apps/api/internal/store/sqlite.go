package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrDuplicateEmail = errors.New("email already registered")
)

type ConflictError struct{ CurrentRevision int64 }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("revision conflict: current revision is %d", e.CurrentRevision)
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Session struct {
	ID, UserID, UserAgent            string
	CreatedAt, LastSeenAt, ExpiresAt time.Time
}

type State struct {
	Revision  int64           `json:"revision"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type CreateAccountParams struct {
	UserID, Email, DisplayName, PasswordHash string
	SessionID                                string
	TokenHash                                []byte
	UserAgent                                string
	StateData                                json.RawMessage
	Now, ExpiresAt                           time.Time
}

type SQLiteStore struct{ db *sql.DB }

func Open(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{`PRAGMA busy_timeout = 5000;`, `PRAGMA foreign_keys = ON;`} {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if path != ":memory:" {
		if _, err = db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite WAL: %w", err)
		}
	}
	if _, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS app_state (
			id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL,
			payload BLOB NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, email TEXT NOT NULL COLLATE NOCASE UNIQUE,
			display_name TEXT NOT NULL, password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash BLOB NOT NULL UNIQUE, created_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL, expires_at TEXT NOT NULL,
			user_agent TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions(user_id);
		CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);
		CREATE TABLE IF NOT EXISTS user_app_state (
			user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL CHECK (revision > 0), payload BLOB NOT NULL,
			updated_at TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) CreateAccount(ctx context.Context, p CreateAccountParams) (User, Session, State, error) {
	if !json.Valid(p.StateData) {
		return User{}, Session{}, State{}, errors.New("state payload is not valid JSON")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, Session{}, State{}, fmt.Errorf("begin registration transaction: %w", err)
	}
	defer tx.Rollback()
	now := p.Now.UTC()
	stamp := now.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO users (id,email,display_name,password_hash,created_at,updated_at) VALUES (?,?,?,?,?,?)`, p.UserID, p.Email, p.DisplayName, p.PasswordHash, stamp, stamp)
	if err != nil {
		if isUniqueConstraint(err) {
			return User{}, Session{}, State{}, ErrDuplicateEmail
		}
		return User{}, Session{}, State{}, fmt.Errorf("insert user: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_app_state (user_id,revision,payload,updated_at) VALUES (?,1,?,?)`, p.UserID, []byte(p.StateData), stamp); err != nil {
		return User{}, Session{}, State{}, fmt.Errorf("insert user state: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions (id,user_id,token_hash,created_at,last_seen_at,expires_at,user_agent) VALUES (?,?,?,?,?,?,?)`, p.SessionID, p.UserID, p.TokenHash, stamp, stamp, p.ExpiresAt.UTC().Format(time.RFC3339Nano), p.UserAgent); err != nil {
		return User{}, Session{}, State{}, fmt.Errorf("insert session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return User{}, Session{}, State{}, fmt.Errorf("commit registration: %w", err)
	}
	user := User{ID: p.UserID, Email: p.Email, DisplayName: p.DisplayName, PasswordHash: p.PasswordHash, CreatedAt: now, UpdatedAt: now}
	session := Session{ID: p.SessionID, UserID: p.UserID, UserAgent: p.UserAgent, CreatedAt: now, LastSeenAt: now, ExpiresAt: p.ExpiresAt.UTC()}
	state := State{Revision: 1, Data: append(json.RawMessage(nil), p.StateData...), UpdatedAt: now}
	return user, session, state, nil
}

func (s *SQLiteStore) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,display_name,password_hash,created_at,updated_at FROM users WHERE email=? COLLATE NOCASE`, email))
}
func (s *SQLiteStore) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,email,display_name,password_hash,created_at,updated_at FROM users WHERE id=?`, id))
}
func scanUser(row *sql.Row) (User, error) {
	var user User
	var created, updated string
	if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	var err error
	if user.CreatedAt, err = parseTime(created); err != nil {
		return User{}, err
	}
	if user.UpdatedAt, err = parseTime(updated); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, id, userID string, tokenHash []byte, userAgent string, now, expires time.Time) (Session, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id,user_id,token_hash,created_at,last_seen_at,expires_at,user_agent) VALUES (?,?,?,?,?,?,?)`, id, userID, tokenHash, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), expires.UTC().Format(time.RFC3339Nano), userAgent)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return Session{ID: id, UserID: userID, UserAgent: userAgent, CreatedAt: now.UTC(), LastSeenAt: now.UTC(), ExpiresAt: expires.UTC()}, nil
}

func (s *SQLiteStore) SessionUser(ctx context.Context, tokenHash []byte, now time.Time) (Session, User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.created_at,s.last_seen_at,s.expires_at,s.user_agent,u.id,u.email,u.display_name,u.password_hash,u.created_at,u.updated_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, now.UTC().Format(time.RFC3339Nano))
	var session Session
	var user User
	var sc, sl, se, uc, uu string
	if err := row.Scan(&session.ID, &session.UserID, &sc, &sl, &se, &session.UserAgent, &user.ID, &user.Email, &user.DisplayName, &user.PasswordHash, &uc, &uu); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, User{}, ErrNotFound
		}
		return Session{}, User{}, fmt.Errorf("load session: %w", err)
	}
	values := []struct {
		target *time.Time
		value  string
	}{{&session.CreatedAt, sc}, {&session.LastSeenAt, sl}, {&session.ExpiresAt, se}, {&user.CreatedAt, uc}, {&user.UpdatedAt, uu}}
	for _, item := range values {
		parsed, err := parseTime(item.value)
		if err != nil {
			return Session{}, User{}, err
		}
		*item.target = parsed
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id=?`, now.UTC().Format(time.RFC3339Nano), session.ID)
	return session, user, nil
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
func (s *SQLiteStore) UpdatePasswordHash(ctx context.Context, userID, passwordHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=?,updated_at=? WHERE id=?`, passwordHash, now.UTC().Format(time.RFC3339Nano), userID)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateDisplayName(ctx context.Context, userID, name string, now time.Time) (User, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET display_name=?,updated_at=? WHERE id=?`, name, now.UTC().Format(time.RFC3339Nano), userID)
	if err != nil {
		return User{}, fmt.Errorf("update display name: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return User{}, ErrNotFound
	}
	return s.UserByID(ctx, userID)
}
func (s *SQLiteStore) UpdateEmail(ctx context.Context, userID, email string, now time.Time) (User, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE users SET email=?,updated_at=? WHERE id=?`, email, now.UTC().Format(time.RFC3339Nano), userID)
	if err != nil {
		if isUniqueConstraint(err) {
			return User{}, ErrDuplicateEmail
		}
		return User{}, fmt.Errorf("update email: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return User{}, ErrNotFound
	}
	return s.UserByID(ctx, userID)
}

func (s *SQLiteStore) RotatePasswordSession(ctx context.Context, userID, passwordHash, newID string, tokenHash []byte, userAgent string, now, expires time.Time) (Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin password transaction: %w", err)
	}
	defer tx.Rollback()
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,updated_at=? WHERE id=?`, passwordHash, stamp, userID)
	if err != nil {
		return Session{}, fmt.Errorf("update password: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Session{}, ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		return Session{}, fmt.Errorf("revoke sessions: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions (id,user_id,token_hash,created_at,last_seen_at,expires_at,user_agent) VALUES (?,?,?,?,?,?,?)`, newID, userID, tokenHash, stamp, stamp, expires.UTC().Format(time.RFC3339Nano), userAgent); err != nil {
		return Session{}, fmt.Errorf("rotate session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit password transaction: %w", err)
	}
	return Session{ID: newID, UserID: userID, UserAgent: userAgent, CreatedAt: now.UTC(), LastSeenAt: now.UTC(), ExpiresAt: expires.UTC()}, nil
}

func (s *SQLiteStore) LoadUserState(ctx context.Context, userID string) (State, error) {
	var state State
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT revision,payload,updated_at FROM user_app_state WHERE user_id=?`, userID).Scan(&state.Revision, &state.Data, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("load user state: %w", err)
	}
	state.UpdatedAt, err = parseTime(updated)
	return state, err
}
func (s *SQLiteStore) SaveUserState(ctx context.Context, userID string, data json.RawMessage, expected int64) (State, error) {
	if !json.Valid(data) {
		return State{}, errors.New("state payload is not valid JSON")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, fmt.Errorf("begin state transaction: %w", err)
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM user_app_state WHERE user_id=?`, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("read current revision: %w", err)
	}
	if expected != current {
		return State{}, &ConflictError{CurrentRevision: current}
	}
	now := time.Now().UTC()
	next := current + 1
	result, err := tx.ExecContext(ctx, `UPDATE user_app_state SET revision=?,payload=?,updated_at=? WHERE user_id=? AND revision=?`, next, []byte(data), now.Format(time.RFC3339Nano), userID, current)
	if err != nil {
		return State{}, fmt.Errorf("update user state: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return State{}, &ConflictError{CurrentRevision: current}
	}
	if err = tx.Commit(); err != nil {
		return State{}, fmt.Errorf("commit state: %w", err)
	}
	return State{Revision: next, Data: append(json.RawMessage(nil), data...), UpdatedAt: now}, nil
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp: %w", err)
	}
	return parsed, nil
}
func isUniqueConstraint(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed") && strings.Contains(message, "users.email")
}
