package agentintegration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/worker"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewTestRunStatePreservesAuthoritativeUsageCompleteness(t *testing.T) {
	view := agentprotocol.ReadonlyRunView{
		Status:        agentprotocol.ReadonlyRunViewStatusFailed,
		Usage:         agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		UsageComplete: false,
	}
	state := newTestRunState(view)
	if state.Status != "failed" || state.Usage != view.Usage || state.UsageComplete {
		t.Fatalf("test Run projection status=%s usage=%+v usageComplete=%t", state.Status, state.Usage, state.UsageComplete)
	}
	if state.Attempts == nil || state.SourceRefs == nil {
		t.Fatal("test Run projection did not initialize repeated diagnostic fields")
	}
}

func TestControlledOutboxCompleteRetainsOneShotWhenDiagnosticStepFails(t *testing.T) {
	userID := uuid.New()
	runID := uuid.New()
	eventID := uuid.New()
	token := uuid.New()
	secret := "task14-outbox-diagnostic-secret-canary"
	tests := []struct {
		name      string
		configure func(*recordingOutboxFaultStore)
	}{
		{name: "lookup error", configure: func(store *recordingOutboxFaultStore) { store.eventRunErr = errors.New(secret) }},
		{name: "lease error", configure: func(store *recordingOutboxFaultStore) { store.expireErr = errors.New(secret) }},
		{name: "lease not held", configure: func(store *recordingOutboxFaultStore) { store.expired = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := committedFaultControl(userID, runID, "complete_once")
			base := &recordingOutboxRepository{}
			diagnostics := &recordingOutboxFaultStore{runID: runID, expired: true}
			test.configure(diagnostics)
			repository := controlledOutbox{base: base, faults: diagnostics, control: control}

			err := repository.Complete(context.Background(), eventID, token)
			if err == nil {
				t.Fatal("Complete() diagnostic failure returned nil")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Complete() leaked diagnostic error: %v", err)
			}
			if base.completeCalls != 0 {
				t.Fatalf("Complete() delegated %d times after diagnostic failure", base.completeCalls)
			}

			diagnostics.eventRunErr = nil
			diagnostics.expireErr = nil
			diagnostics.expired = true
			if err = repository.Complete(context.Background(), eventID, token); err == nil || err.Error() != "controlled integration outbox confirmation failure" {
				t.Fatalf("Complete() recovered injection error = %v", err)
			}
			if base.completeCalls != 0 {
				t.Fatalf("Complete() delegated %d times during recovered injection", base.completeCalls)
			}
			if err = repository.Complete(context.Background(), eventID, token); err != nil {
				t.Fatalf("Complete() after consumed injection error = %v", err)
			}
			if base.completeCalls != 1 {
				t.Fatalf("Complete() final delegate calls = %d, want 1", base.completeCalls)
			}
		})
	}
}

func TestControlledOutboxRetryReleasesInterruptionOnlyAfterSuccessfulRetry(t *testing.T) {
	userID := uuid.New()
	runID := uuid.New()
	eventID := uuid.New()
	token := uuid.New()
	secret := "task14-outbox-lookup-secret-canary"
	control := committedFaultControl(userID, runID, "interrupted")
	base := &recordingOutboxRepository{}
	diagnostics := &recordingOutboxFaultStore{runID: runID, eventRunErr: errors.New(secret), expired: true}
	repository := controlledOutbox{base: base, faults: diagnostics, control: control}
	retry := model.OutboxRetry{EventID: eventID, LockToken: token, AvailableAt: time.Now().UTC().Add(time.Hour)}

	err := repository.Retry(context.Background(), retry)
	if err == nil {
		t.Fatal("Retry() lookup failure returned nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Retry() leaked lookup error: %v", err)
	}
	if base.retryCalls != 0 || control.interruptionReleased(runID) {
		t.Fatalf("lookup failure retry calls=%d released=%v", base.retryCalls, control.interruptionReleased(runID))
	}

	diagnostics.eventRunErr = nil
	base.retryErr = errors.New("controlled base retry failure")
	if err = repository.Retry(context.Background(), retry); err == nil {
		t.Fatal("Retry() base failure returned nil")
	}
	if base.retryCalls != 1 || control.interruptionReleased(runID) {
		t.Fatalf("base failure retry calls=%d released=%v", base.retryCalls, control.interruptionReleased(runID))
	}

	base.retryErr = nil
	before := time.Now().UTC()
	if err = repository.Retry(context.Background(), retry); err != nil {
		t.Fatalf("Retry() recovery error = %v", err)
	}
	if !control.interruptionReleased(runID) {
		t.Fatal("successful Retry() did not release interrupted Run")
	}
	if base.lastRetry.AvailableAt.Before(before) || base.lastRetry.AvailableAt.After(time.Now().UTC()) {
		t.Fatalf("successful Retry() availableAt = %s", base.lastRetry.AvailableAt)
	}
}

func TestControlledTransactionRestoresFaultAfterPostInsertRollback(t *testing.T) {
	userID := uuid.New()
	rolledBackRun := uuid.New()
	committedRun := uuid.New()
	control := &faultControl{}
	if err := control.Arm(userID, "provider_disconnect"); err != nil {
		t.Fatal(err)
	}
	beginner := &scriptedBeginner{transactions: []*scriptedTx{{}, {}}}
	transactor := database.NewTransactor(controlledBeginner{base: beginner, control: control})
	base := &recordingCreateStore{}
	store := controlledStore{Store: base, control: control}
	postInsertErr := errors.New("controlled post-insert failure")

	err := transactor.WithUser(context.Background(), userID, func(ctx context.Context, tx database.Tx) error {
		if err := store.Create(ctx, tx, executionRecord(userID, rolledBackRun)); err != nil {
			return err
		}
		return postInsertErr
	})
	if !errors.Is(err, postInsertErr) {
		t.Fatalf("post-insert transaction error = %v", err)
	}
	if !control.armedFor(userID, "provider_disconnect") || control.forRun(rolledBackRun) != "" {
		t.Fatal("post-insert rollback did not restore the armed fault")
	}

	err = transactor.WithUser(context.Background(), userID, func(ctx context.Context, tx database.Tx) error {
		return store.Create(ctx, tx, executionRecord(userID, committedRun))
	})
	if err != nil {
		t.Fatalf("next transaction error = %v", err)
	}
	if control.armedFor(userID, "provider_disconnect") || control.forRunOwner(committedRun, userID) != "provider_disconnect" {
		t.Fatal("next committed Run did not receive the restored fault")
	}
	if base.createCalls != 2 {
		t.Fatalf("repository Create() calls = %d, want 2", base.createCalls)
	}
}

func TestControlledTransactionPreservesTransactorRetryForConclusiveCommitRollback(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			userID := uuid.New()
			runID := uuid.New()
			control := &faultControl{}
			if err := control.Arm(userID, "commit_once"); err != nil {
				t.Fatal(err)
			}
			beginner := &scriptedBeginner{transactions: []*scriptedTx{
				{commitErr: &pgconn.PgError{Code: code}},
				{},
			}}
			transactor := database.NewTransactor(controlledBeginner{base: beginner, control: control})
			base := &recordingCreateStore{}
			store := controlledStore{Store: base, control: control}

			err := transactor.WithUser(context.Background(), userID, func(ctx context.Context, tx database.Tx) error {
				return store.Create(ctx, tx, executionRecord(userID, runID))
			})
			if err != nil {
				t.Fatalf("WithUser() retry error = %v", err)
			}
			if beginner.begins != 2 || base.createCalls != 2 {
				t.Fatalf("retry begins=%d creates=%d, want 2/2", beginner.begins, base.createCalls)
			}
			if control.forRunOwner(runID, userID) != "commit_once" || control.armedFor(userID, "commit_once") {
				t.Fatal("retried transaction did not commit exactly one fault binding")
			}
		})
	}
}

func TestControlledTransactionRestoresFaultAfterExplicitCommitRollback(t *testing.T) {
	userID := uuid.New()
	runID := uuid.New()
	control := &faultControl{}
	if err := control.Arm(userID, "provider_429"); err != nil {
		t.Fatal(err)
	}
	beginner := &scriptedBeginner{transactions: []*scriptedTx{{commitErr: pgx.ErrTxCommitRollback}}}
	transactor := database.NewTransactor(controlledBeginner{base: beginner, control: control})
	store := controlledStore{Store: &recordingCreateStore{}, control: control}

	err := transactor.WithUser(context.Background(), userID, func(ctx context.Context, tx database.Tx) error {
		return store.Create(ctx, tx, executionRecord(userID, runID))
	})
	if !errors.Is(err, pgx.ErrTxCommitRollback) {
		t.Fatalf("WithUser() error = %v, want ErrTxCommitRollback", err)
	}
	if !control.armedFor(userID, "provider_429") || control.forRun(runID) != "" {
		t.Fatal("explicit commit rollback did not restore the armed fault")
	}
}

func TestControlledTransactionQuarantinesAmbiguousCommitByAccount(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	runA := uuid.New()
	laterRunA := uuid.New()
	runB := uuid.New()
	control := &faultControl{}
	if err := control.Arm(userA, "complete_once"); err != nil {
		t.Fatal(err)
	}
	unknownCommit := errors.New("controlled connection failure")
	beginner := &scriptedBeginner{transactions: []*scriptedTx{{commitErr: unknownCommit}, {}, {}}}
	transactor := database.NewTransactor(controlledBeginner{base: beginner, control: control})
	base := &recordingCreateStore{}
	store := controlledStore{Store: base, control: control}

	err := transactor.WithUser(context.Background(), userA, func(ctx context.Context, tx database.Tx) error {
		return store.Create(ctx, tx, executionRecord(userA, runA))
	})
	if !errors.Is(err, unknownCommit) {
		t.Fatalf("ambiguous commit error = %v", err)
	}
	if control.forRunOwner(runA, userA) != "complete_once" || control.armedFor(userA, "complete_once") {
		t.Fatal("ambiguous commit did not quarantine the exact Run binding")
	}
	if err = control.Arm(userA, "provider_429"); err == nil {
		t.Fatal("Arm() replaced an ambiguous account reservation")
	}

	err = transactor.WithUser(context.Background(), userA, func(ctx context.Context, tx database.Tx) error {
		return store.Create(ctx, tx, executionRecord(userA, laterRunA))
	})
	if err == nil {
		t.Fatal("same-account Create() succeeded after ambiguous commit")
	}
	if control.forRun(laterRunA) != "" || base.createCalls != 1 {
		t.Fatalf("same-account Create() mapping=%q repository calls=%d", control.forRun(laterRunA), base.createCalls)
	}

	if err = control.Arm(userB, "provider_disconnect"); err != nil {
		t.Fatalf("Arm(other account) error = %v", err)
	}
	err = transactor.WithUser(context.Background(), userB, func(ctx context.Context, tx database.Tx) error {
		return store.Create(ctx, tx, executionRecord(userB, runB))
	})
	if err != nil || control.forRunOwner(runB, userB) != "provider_disconnect" {
		t.Fatalf("other-account transaction error=%v fault=%q", err, control.forRunOwner(runB, userB))
	}
}

func TestControlledTransactionPublishesBeforeCommittedTransactionReturns(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	runID := uuid.New()
	control := &faultControl{}
	if err := control.Arm(userA, "tool_timeout"); err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{})
	releaseCommit := make(chan struct{})
	baseTx := &scriptedTx{committed: committed, releaseCommit: releaseCommit}
	beginner := &scriptedBeginner{transactions: []*scriptedTx{baseTx}}
	transactor := database.NewTransactor(controlledBeginner{base: beginner, control: control})
	store := controlledStore{Store: &recordingCreateStore{}, control: control}
	done := make(chan error, 1)
	go func() {
		done <- transactor.WithUser(context.Background(), userA, func(ctx context.Context, tx database.Tx) error {
			return store.Create(ctx, tx, executionRecord(userA, runID))
		})
	}()

	select {
	case <-committed:
	case <-time.After(time.Second):
		t.Fatal("transaction did not reach actual commit boundary")
	}
	if control.forRunOwner(runID, userA) != "tool_timeout" {
		t.Fatal("committed Run became visible before its provisional fault mapping")
	}
	if err := control.Arm(userA, "provider_429"); err == nil {
		t.Fatal("Arm() replaced a provisional account reservation")
	}
	otherAccount := make(chan error, 1)
	go func() { otherAccount <- control.Arm(userB, "provider_disconnect") }()
	select {
	case err := <-otherAccount:
		if err != nil {
			t.Fatalf("Arm(other account) error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("database Commit held the global fault mutex")
	}
	close(releaseCommit)
	if err := <-done; err != nil {
		t.Fatalf("WithUser() error = %v", err)
	}
	if err := control.Arm(userA, "provider_429"); err != nil {
		t.Fatalf("Arm() after commit error = %v", err)
	}
}

func TestControlledStoreRejectsIncompatibleTransactionWithoutSideEffects(t *testing.T) {
	userID := uuid.New()
	runID := uuid.New()
	control := &faultControl{}
	if err := control.Arm(userID, "commit_once"); err != nil {
		t.Fatal(err)
	}
	base := &recordingCreateStore{}
	store := controlledStore{Store: base, control: control}

	err := store.Create(context.Background(), &scriptedTx{}, executionRecord(userID, runID))
	if err == nil {
		t.Fatal("Create() accepted an incompatible transaction")
	}
	if base.createCalls != 0 || !control.armedFor(userID, "commit_once") || control.forRun(runID) != "" {
		t.Fatalf("mismatch calls=%d armed=%v runFault=%q", base.createCalls, control.armedFor(userID, "commit_once"), control.forRun(runID))
	}
}

func committedFaultControl(userID, runID uuid.UUID, fault string) *faultControl {
	return &faultControl{
		runs:   map[uuid.UUID]string{runID: fault},
		owners: map[uuid.UUID]uuid.UUID{runID: userID},
	}
}

type recordingOutboxFaultStore struct {
	runID       uuid.UUID
	eventRunErr error
	expired     bool
	expireErr   error
}

func (store *recordingOutboxFaultStore) EventRun(context.Context, uuid.UUID) (uuid.UUID, error) {
	return store.runID, store.eventRunErr
}

func (store *recordingOutboxFaultStore) ExpireLease(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return store.expired, store.expireErr
}

type recordingOutboxRepository struct {
	worker.OutboxRepository
	completeCalls int
	retryCalls    int
	retryErr      error
	lastRetry     model.OutboxRetry
}

func (repository *recordingOutboxRepository) Complete(context.Context, uuid.UUID, uuid.UUID) error {
	repository.completeCalls++
	return nil
}

func (repository *recordingOutboxRepository) Retry(_ context.Context, retry model.OutboxRetry) error {
	repository.retryCalls++
	repository.lastRetry = retry
	return repository.retryErr
}

type recordingCreateStore struct {
	agentexecution.Store
	createCalls int
}

func (store *recordingCreateStore) Create(context.Context, database.Tx, agentexecution.Record) error {
	store.createCalls++
	return nil
}

type scriptedBeginner struct {
	transactions []*scriptedTx
	begins       int
}

func (beginner *scriptedBeginner) Begin(context.Context) (database.Tx, error) {
	if beginner.begins >= len(beginner.transactions) {
		return nil, errors.New("unexpected transaction begin")
	}
	tx := beginner.transactions[beginner.begins]
	beginner.begins++
	return tx, nil
}

type scriptedTx struct {
	database.Tx
	commitErr     error
	committed     chan struct{}
	releaseCommit chan struct{}
	rollbacks     int
}

func (tx *scriptedTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func (tx *scriptedTx) Commit(context.Context) error {
	if tx.committed != nil {
		close(tx.committed)
	}
	if tx.releaseCommit != nil {
		<-tx.releaseCommit
	}
	return tx.commitErr
}

func (tx *scriptedTx) Rollback(context.Context) error {
	tx.rollbacks++
	return nil
}
