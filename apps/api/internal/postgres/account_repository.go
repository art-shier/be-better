package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	db "dayorder.local/api/internal/db/gen"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AccountRepository struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewAccountRepository(pool *pgxpool.Pool) (*AccountRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &AccountRepository{pool: pool, queries: db.New(pool)}, nil
}

func (repository *AccountRepository) CreatePendingAccount(ctx context.Context, registration model.PendingAccountRegistration) (model.Account, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return model.Account{}, fmt.Errorf("begin pending account transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := repository.queries.WithTx(tx)
	userID := pgUUID(registration.Account.ID)
	if err = queries.SetUserContext(ctx, userID); err != nil {
		return model.Account{}, fmt.Errorf("set pending account context: %w", err)
	}
	user, err := queries.CreateUser(ctx, db.CreateUserParams{
		ID: userID, Email: registration.Account.Email,
		NormalizedEmail: registration.Account.NormalizedEmail,
		DisplayName:     registration.Account.DisplayName,
		PasswordHash:    registration.PasswordHash,
		Status:          string(model.AccountPendingVerification),
		EmailVerifiedAt: pgtype.Timestamptz{},
	})
	if err != nil {
		return model.Account{}, mapDatabaseError("create pending account", err)
	}
	if _, err = queries.CreateUserSettings(ctx, userID); err != nil {
		return model.Account{}, mapDatabaseError("create account settings", err)
	}
	if _, err = queries.CreateAccountToken(ctx, db.CreateAccountTokenParams{
		ID: pgUUID(registration.VerificationToken.ID), UserID: userID,
		Purpose:   string(registration.VerificationToken.Purpose),
		TokenHash: append([]byte(nil), registration.VerificationToken.TokenHash...),
		ExpiresAt: pgTime(registration.VerificationToken.ExpiresAt),
	}); err != nil {
		return model.Account{}, mapDatabaseError("create verification token", err)
	}
	if _, err = queries.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		ID: pgUUID(registration.OutboxID), UserID: userID,
		EventType: "email.verification.requested", AggregateType: "user", AggregateID: userID,
		Payload: append([]byte(nil), registration.OutboxPayload...), AvailableAt: pgTime(registration.Account.CreatedAt),
	}); err != nil {
		return model.Account{}, mapDatabaseError("create verification outbox event", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Account{}, fmt.Errorf("commit pending account transaction: %w", err)
	}
	return accountFromRow(user), nil
}

func (repository *AccountRepository) ConsumeAccountToken(ctx context.Context, tokenHash []byte, purpose model.AccountTokenPurpose, _ time.Time) (model.Account, error) {
	token, err := repository.queries.LookupAccountToken(ctx, tokenHash)
	if err != nil {
		return model.Account{}, mapDatabaseError("lookup account token", err)
	}
	if token.Purpose != string(purpose) {
		return model.Account{}, model.ErrNotFound
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return model.Account{}, fmt.Errorf("begin account token transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := repository.queries.WithTx(tx)
	if err = queries.SetUserContext(ctx, token.UserID); err != nil {
		return model.Account{}, fmt.Errorf("set account token context: %w", err)
	}
	affected, err := queries.ConsumeAccountToken(ctx, token.UserID, token.TokenID)
	if err != nil {
		return model.Account{}, mapDatabaseError("consume account token", err)
	}
	if affected != 1 {
		return model.Account{}, model.ErrNotFound
	}
	var user *db.DayorderUser
	if purpose == model.TokenVerifyEmail {
		user, err = queries.ActivateUser(ctx, token.UserID)
	} else {
		user, err = queries.GetUser(ctx, token.UserID)
	}
	if err != nil {
		return model.Account{}, mapDatabaseError("apply account token", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Account{}, fmt.Errorf("commit account token transaction: %w", err)
	}
	return accountFromRow(user), nil
}

func (repository *AccountRepository) CreateAccountTokenDelivery(ctx context.Context, account model.Account, delivery model.AccountTokenDelivery) error {
	return repository.inUserTransaction(ctx, account.ID, func(queries *db.Queries) error {
		if _, err := queries.InvalidateAccountTokens(ctx, pgUUID(account.ID), string(delivery.Token.Purpose)); err != nil {
			return mapDatabaseError("invalidate previous account tokens", err)
		}
		if _, err := queries.CreateAccountToken(ctx, db.CreateAccountTokenParams{
			ID: pgUUID(delivery.Token.ID), UserID: pgUUID(account.ID), Purpose: string(delivery.Token.Purpose),
			TokenHash: append([]byte(nil), delivery.Token.TokenHash...), ExpiresAt: pgTime(delivery.Token.ExpiresAt),
		}); err != nil {
			return mapDatabaseError("create account token delivery", err)
		}
		if _, err := queries.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			ID: pgUUID(delivery.OutboxID), UserID: pgUUID(account.ID), EventType: delivery.EventType,
			AggregateType: "user", AggregateID: pgUUID(account.ID),
			Payload: append([]byte(nil), delivery.OutboxPayload...), AvailableAt: pgTime(delivery.Token.CreatedAt),
		}); err != nil {
			return mapDatabaseError("create account token outbox", err)
		}
		return nil
	})
}

func (repository *AccountRepository) ResetPasswordWithToken(ctx context.Context, tokenHash []byte, passwordHash string, _ time.Time) (model.Account, error) {
	token, err := repository.queries.LookupAccountToken(ctx, tokenHash)
	if err != nil {
		return model.Account{}, mapDatabaseError("lookup password reset token", err)
	}
	if token.Purpose != string(model.TokenResetPassword) {
		return model.Account{}, model.ErrNotFound
	}
	var account model.Account
	err = repository.inUserTransaction(ctx, uuid.UUID(token.UserID.Bytes), func(queries *db.Queries) error {
		affected, err := queries.ConsumeAccountToken(ctx, token.UserID, token.TokenID)
		if err != nil {
			return mapDatabaseError("consume password reset token", err)
		}
		if affected != 1 {
			return model.ErrNotFound
		}
		affected, err = queries.UpdatePasswordHash(ctx, passwordHash, token.UserID)
		if err != nil {
			return mapDatabaseError("apply reset password", err)
		}
		if affected != 1 {
			return model.ErrNotFound
		}
		if _, err = queries.RevokeAllUserSessions(ctx, token.UserID); err != nil {
			return mapDatabaseError("revoke password reset sessions", err)
		}
		user, err := queries.GetUser(ctx, token.UserID)
		if err != nil {
			return mapDatabaseError("read password reset account", err)
		}
		account = accountFromRow(user)
		return nil
	})
	return account, err
}

func (repository *AccountRepository) UpdateDisplayName(ctx context.Context, userID uuid.UUID, displayName string) (model.Account, error) {
	var account model.Account
	err := repository.inUserTransaction(ctx, userID, func(queries *db.Queries) error {
		user, err := queries.UpdateDisplayName(ctx, displayName, pgUUID(userID))
		if err != nil {
			return mapDatabaseError("update display name", err)
		}
		account = accountFromRow(user)
		return nil
	})
	return account, err
}

func (repository *AccountRepository) UpdateEmail(ctx context.Context, userID uuid.UUID, normalizedEmail string, delivery model.AccountTokenDelivery) (model.Account, error) {
	var account model.Account
	err := repository.inUserTransaction(ctx, userID, func(queries *db.Queries) error {
		user, err := queries.UpdateEmail(ctx, normalizedEmail, normalizedEmail, pgUUID(userID))
		if err != nil {
			return mapDatabaseError("update account email", err)
		}
		account = accountFromRow(user)
		if _, err = queries.InvalidateAccountTokens(ctx, pgUUID(userID), string(model.TokenVerifyEmail)); err != nil {
			return mapDatabaseError("invalidate email verification tokens", err)
		}
		if _, err = queries.CreateAccountToken(ctx, db.CreateAccountTokenParams{
			ID: pgUUID(delivery.Token.ID), UserID: pgUUID(userID), Purpose: string(model.TokenVerifyEmail),
			TokenHash: append([]byte(nil), delivery.Token.TokenHash...), ExpiresAt: pgTime(delivery.Token.ExpiresAt),
		}); err != nil {
			return mapDatabaseError("create changed-email verification token", err)
		}
		if _, err = queries.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
			ID: pgUUID(delivery.OutboxID), UserID: pgUUID(userID), EventType: delivery.EventType,
			AggregateType: "user", AggregateID: pgUUID(userID),
			Payload: append([]byte(nil), delivery.OutboxPayload...), AvailableAt: pgTime(delivery.Token.CreatedAt),
		}); err != nil {
			return mapDatabaseError("create changed-email verification outbox", err)
		}
		return nil
	})
	return account, err
}

func (repository *AccountRepository) LookupLoginAccount(ctx context.Context, normalizedEmail string) (model.LoginAccount, error) {
	row, err := repository.queries.LookupLoginAccount(ctx, normalizedEmail)
	if err != nil {
		return model.LoginAccount{}, mapDatabaseError("lookup login account", err)
	}
	return model.LoginAccount{
		Account: model.Account{
			ID: uuid.UUID(row.UserID.Bytes), Email: row.Email, NormalizedEmail: row.NormalizedEmail,
			DisplayName: row.DisplayName, Status: model.AccountStatus(row.UserStatus),
			EmailVerifiedAt: optionalTime(row.EmailVerifiedAt), CreatedAt: row.CreatedAt.Time.UTC(),
			UpdatedAt: row.UpdatedAt.Time.UTC(),
		},
		PasswordHash: row.PasswordHash,
	}, nil
}

func (repository *AccountRepository) CreateSession(ctx context.Context, session model.NewSession) (model.Session, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return model.Session{}, fmt.Errorf("begin session transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := repository.queries.WithTx(tx)
	if err = queries.SetUserContext(ctx, pgUUID(session.UserID)); err != nil {
		return model.Session{}, fmt.Errorf("set session context: %w", err)
	}
	row, err := queries.CreateSession(ctx, db.CreateSessionParams{
		ID: pgUUID(session.ID), UserID: pgUUID(session.UserID), TokenHash: append([]byte(nil), session.TokenHash...),
		UserAgent: session.UserAgent, ExpiresAt: pgTime(session.ExpiresAt),
	})
	if err != nil {
		return model.Session{}, mapDatabaseError("create session", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Session{}, fmt.Errorf("commit session transaction: %w", err)
	}
	return sessionFromRow(row), nil
}

func (repository *AccountRepository) AuthenticateSession(ctx context.Context, tokenHash []byte, touchInterval time.Duration) (model.AuthenticatedSession, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return model.AuthenticatedSession{}, fmt.Errorf("begin authentication transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := repository.queries.WithTx(tx)
	authenticated, err := queries.AuthenticateSession(ctx, tokenHash)
	if err != nil {
		return model.AuthenticatedSession{}, mapDatabaseError("authenticate session", err)
	}
	if err = queries.SetUserContext(ctx, authenticated.UserID); err != nil {
		return model.AuthenticatedSession{}, fmt.Errorf("set authenticated user context: %w", err)
	}
	_, err = queries.TouchSession(ctx, authenticated.UserID, authenticated.SessionID, pgtype.Interval{
		Microseconds: touchInterval.Microseconds(), Valid: true,
	})
	if err != nil {
		return model.AuthenticatedSession{}, mapDatabaseError("touch authenticated session", err)
	}
	session, err := queries.GetSession(ctx, authenticated.UserID, authenticated.SessionID)
	if err != nil {
		return model.AuthenticatedSession{}, mapDatabaseError("read authenticated session", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return model.AuthenticatedSession{}, fmt.Errorf("commit authentication transaction: %w", err)
	}
	return model.AuthenticatedSession{
		Session: sessionFromRow(session),
		Account: model.Account{
			ID: uuid.UUID(authenticated.UserID.Bytes), Email: authenticated.Email,
			NormalizedEmail: authenticated.Email, DisplayName: authenticated.DisplayName,
			Status:          model.AccountStatus(authenticated.UserStatus),
			EmailVerifiedAt: optionalTime(authenticated.EmailVerifiedAt),
		},
	}, nil
}

func (repository *AccountRepository) RevokeSession(ctx context.Context, userID, sessionID uuid.UUID) error {
	return repository.inUserTransaction(ctx, userID, func(queries *db.Queries) error {
		affected, err := queries.RevokeSession(ctx, pgUUID(userID), pgUUID(sessionID))
		if err != nil {
			return mapDatabaseError("revoke session", err)
		}
		if affected != 1 {
			return model.ErrNotFound
		}
		return nil
	})
}

func (repository *AccountRepository) RotatePasswordSession(ctx context.Context, rotation model.PasswordSessionRotation) (model.Session, error) {
	var persisted model.Session
	err := repository.inUserTransaction(ctx, rotation.UserID, func(queries *db.Queries) error {
		affected, err := queries.UpdatePasswordHash(ctx, rotation.PasswordHash, pgUUID(rotation.UserID))
		if err != nil {
			return mapDatabaseError("update password", err)
		}
		if affected != 1 {
			return model.ErrNotFound
		}
		if _, err = queries.RevokeAllUserSessions(ctx, pgUUID(rotation.UserID)); err != nil {
			return mapDatabaseError("revoke password sessions", err)
		}
		row, err := queries.CreateSession(ctx, db.CreateSessionParams{
			ID: pgUUID(rotation.Session.ID), UserID: pgUUID(rotation.UserID),
			TokenHash: append([]byte(nil), rotation.Session.TokenHash...),
			UserAgent: rotation.Session.UserAgent, ExpiresAt: pgTime(rotation.Session.ExpiresAt),
		})
		if err != nil {
			return mapDatabaseError("create rotated session", err)
		}
		persisted = sessionFromRow(row)
		return nil
	})
	return persisted, err
}

func (repository *AccountRepository) UpdatePasswordHash(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	return repository.inUserTransaction(ctx, userID, func(queries *db.Queries) error {
		affected, err := queries.UpdatePasswordHash(ctx, passwordHash, pgUUID(userID))
		if err != nil {
			return mapDatabaseError("update password hash", err)
		}
		if affected != 1 {
			return model.ErrNotFound
		}
		return nil
	})
}

func (repository *AccountRepository) PasswordHashByUserID(ctx context.Context, userID uuid.UUID) (string, error) {
	var passwordHash string
	err := repository.inUserTransaction(ctx, userID, func(queries *db.Queries) error {
		value, err := queries.PasswordHashByUserID(ctx, pgUUID(userID))
		if err != nil {
			return mapDatabaseError("read password hash", err)
		}
		passwordHash = value
		return nil
	})
	return passwordHash, err
}

func (repository *AccountRepository) LoginThrottleStatus(ctx context.Context, dimension string, keyHash []byte) (model.LoginThrottle, error) {
	row, err := repository.queries.LoginThrottleStatus(ctx, dimension, keyHash)
	if err != nil {
		return model.LoginThrottle{}, mapDatabaseError("read login throttle", err)
	}
	return model.LoginThrottle{Failures: int(row.Failures), BlockedUntil: optionalTime(row.BlockedUntil)}, nil
}

func (repository *AccountRepository) RecordLoginFailure(ctx context.Context, dimension string, keyHash []byte) (model.LoginThrottle, error) {
	row, err := repository.queries.RecordLoginFailure(ctx, dimension, keyHash)
	if err != nil {
		return model.LoginThrottle{}, mapDatabaseError("record login failure", err)
	}
	return model.LoginThrottle{Failures: int(row.Failures), BlockedUntil: optionalTime(row.BlockedUntil)}, nil
}

func (repository *AccountRepository) ClearLoginThrottle(ctx context.Context, dimension string, keyHash []byte) error {
	if err := repository.queries.ClearLoginThrottle(ctx, dimension, keyHash); err != nil {
		return mapDatabaseError("clear login throttle", err)
	}
	return nil
}

func (repository *AccountRepository) inUserTransaction(ctx context.Context, userID uuid.UUID, operation func(*db.Queries) error) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin user transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := repository.queries.WithTx(tx)
	if err = queries.SetUserContext(ctx, pgUUID(userID)); err != nil {
		return fmt.Errorf("set user transaction context: %w", err)
	}
	if err = operation(queries); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit user transaction: %w", err)
	}
	return nil
}

func accountFromRow(row *db.DayorderUser) model.Account {
	return model.Account{
		ID: uuid.UUID(row.ID.Bytes), Email: row.Email, NormalizedEmail: row.NormalizedEmail,
		DisplayName: row.DisplayName, Status: model.AccountStatus(row.Status),
		EmailVerifiedAt: optionalTime(row.EmailVerifiedAt), CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
}

func sessionFromRow(row *db.DayorderSession) model.Session {
	return model.Session{
		ID: uuid.UUID(row.ID.Bytes), UserID: uuid.UUID(row.UserID.Bytes), UserAgent: row.UserAgent,
		CreatedAt: row.CreatedAt.Time.UTC(), LastSeenAt: row.LastSeenAt.Time.UTC(),
		ExpiresAt: row.ExpiresAt.Time.UTC(), RevokedAt: optionalTime(row.RevokedAt),
	}
}

func pgUUID(value uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: value, Valid: true} }
func pgTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := value.Time.UTC()
	return &timestamp
}

func mapDatabaseError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return model.ErrConflict
		case "23503":
			return model.ErrNotFound
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
