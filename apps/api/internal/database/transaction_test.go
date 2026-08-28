package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeBeginner struct {
	transactions []*fakeTransaction
	begins       int
}

func (beginner *fakeBeginner) Begin(context.Context) (Tx, error) {
	transaction := beginner.transactions[beginner.begins]
	beginner.begins++
	return transaction, nil
}

type fakeTransaction struct {
	execSQL   []string
	execArgs  [][]any
	commits   int
	rollbacks int
	commitErr error
}

func (transaction *fakeTransaction) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	transaction.execSQL = append(transaction.execSQL, sql)
	transaction.execArgs = append(transaction.execArgs, args)
	return pgconn.NewCommandTag("SELECT 1"), nil
}
func (*fakeTransaction) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not implemented")
}
func (*fakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (transaction *fakeTransaction) Commit(context.Context) error {
	transaction.commits++
	return transaction.commitErr
}
func (transaction *fakeTransaction) Rollback(context.Context) error {
	transaction.rollbacks++
	return nil
}

func TestTransactorSetsParameterizedUserContextAndCommits(t *testing.T) {
	tx := &fakeTransaction{}
	transactor := NewTransactor(&fakeBeginner{transactions: []*fakeTransaction{tx}})
	userID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	called := false
	err := transactor.WithUser(context.Background(), userID, func(_ context.Context, received Tx) error {
		called = received == tx
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || tx.commits != 1 || len(tx.execSQL) != 1 || tx.execSQL[0] != "SELECT dayorder.set_user_context($1)" {
		t.Fatalf("called=%t commits=%d exec=%v", called, tx.commits, tx.execSQL)
	}
	if len(tx.execArgs[0]) != 1 || tx.execArgs[0][0] != userID {
		t.Fatalf("user context args = %#v, want %s", tx.execArgs[0], userID)
	}
}

func TestTransactorStopsAfterThreeRetryableFailures(t *testing.T) {
	transactions := []*fakeTransaction{{}, {}, {}}
	beginner := &fakeBeginner{transactions: transactions}
	transactor := NewTransactor(beginner)
	transactor.sleep = func(context.Context, time.Duration) error { return nil }
	err := transactor.WithUser(context.Background(), uuid.New(), func(context.Context, Tx) error {
		return &pgconn.PgError{Code: "40001"}
	})
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || beginner.begins != 3 {
		t.Fatalf("error=%v begins=%d, want PostgreSQL error and 3 attempts", err, beginner.begins)
	}
	for index, transaction := range transactions {
		if transaction.rollbacks != 1 || transaction.commits != 0 {
			t.Fatalf("transaction %d rollback/commit = %d/%d", index, transaction.rollbacks, transaction.commits)
		}
	}
}

func TestTransactorRollsBackBusinessErrorsWithoutRetry(t *testing.T) {
	tx := &fakeTransaction{}
	transactor := NewTransactor(&fakeBeginner{transactions: []*fakeTransaction{tx}})
	businessError := errors.New("entity version conflict")
	err := transactor.WithUser(context.Background(), uuid.New(), func(context.Context, Tx) error { return businessError })
	if !errors.Is(err, businessError) || tx.rollbacks != 1 || tx.commits != 0 {
		t.Fatalf("error=%v rollbacks=%d commits=%d", err, tx.rollbacks, tx.commits)
	}
}

func TestTransactorRetriesSerializationAndDeadlockErrorsOnly(t *testing.T) {
	tests := []string{"40001", "40P01"}
	for _, code := range tests {
		t.Run(code, func(t *testing.T) {
			first := &fakeTransaction{}
			second := &fakeTransaction{}
			beginner := &fakeBeginner{transactions: []*fakeTransaction{first, second}}
			transactor := NewTransactor(beginner)
			transactor.sleep = func(context.Context, time.Duration) error { return nil }
			attempt := 0
			err := transactor.WithUser(context.Background(), uuid.New(), func(context.Context, Tx) error {
				attempt++
				if attempt == 1 {
					return &pgconn.PgError{Code: code}
				}
				return nil
			})
			if err != nil || beginner.begins != 2 || first.rollbacks != 1 || second.commits != 1 {
				t.Fatalf("error=%v begins=%d first rollback=%d second commit=%d", err, beginner.begins, first.rollbacks, second.commits)
			}
		})
	}
}
