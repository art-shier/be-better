package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type Beginner interface {
	Begin(context.Context) (Tx, error)
}

type poolBeginner struct{ pool *pgxpool.Pool }

func (beginner poolBeginner) Begin(ctx context.Context) (Tx, error) { return beginner.pool.Begin(ctx) }

type Transactor struct {
	beginner Beginner
	attempts int
	sleep    func(context.Context, time.Duration) error
}

func NewTransactor(beginner Beginner) *Transactor {
	return &Transactor{beginner: beginner, attempts: 3, sleep: sleepContext}
}

func NewPoolTransactor(pool *pgxpool.Pool) (*Transactor, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return NewTransactor(poolBeginner{pool: pool}), nil
}

func (transactor *Transactor) WithUser(ctx context.Context, userID uuid.UUID, operation func(context.Context, Tx) error) error {
	if transactor == nil || transactor.beginner == nil {
		return errors.New("transaction beginner is required")
	}
	if userID == uuid.Nil {
		return errors.New("transaction user ID is required")
	}
	if operation == nil {
		return errors.New("transaction operation is required")
	}
	for attempt := 1; attempt <= transactor.attempts; attempt++ {
		tx, err := transactor.beginner.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin user transaction: %w", err)
		}
		if _, err = tx.Exec(ctx, "SELECT dayorder.set_user_context($1)", userID); err == nil {
			err = operation(ctx, tx)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err == nil {
			return nil
		}
		rollback(tx)
		if !retryableTransactionError(err) || attempt == transactor.attempts {
			return err
		}
		delay := time.Duration(attempt*attempt)*10*time.Millisecond + time.Duration(time.Now().UnixNano()%int64(10*time.Millisecond))
		if sleepErr := transactor.sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
	return errors.New("transaction retry loop exhausted")
}

func retryableTransactionError(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	return postgresError.Code == "40001" || postgresError.Code == "40P01"
}

func rollback(tx Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
