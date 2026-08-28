package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeOutbox struct {
	events    []model.OutboxEvent
	claimErr  error
	completed []uuid.UUID
	retried   []model.OutboxRetry
}

func (repository *fakeOutbox) Claim(context.Context, int, uuid.UUID, time.Duration) ([]model.OutboxEvent, error) {
	return repository.events, repository.claimErr
}
func (repository *fakeOutbox) Complete(_ context.Context, eventID, _ uuid.UUID) error {
	repository.completed = append(repository.completed, eventID)
	return nil
}
func (repository *fakeOutbox) Retry(_ context.Context, retry model.OutboxRetry) error {
	repository.retried = append(repository.retried, retry)
	return nil
}

type fakeHandler struct {
	events []model.OutboxEvent
	err    error
}

func (handler *fakeHandler) Handle(_ context.Context, event model.OutboxEvent) error {
	handler.events = append(handler.events, event)
	return handler.err
}

func TestRunnerCompletesSuccessfulEvents(t *testing.T) {
	event := testOutboxEvent(1)
	repository := &fakeOutbox{events: []model.OutboxEvent{event}}
	handler := &fakeHandler{}
	runner, err := NewRunner(repository, map[string]Handler{"email.verification.requested": handler})
	if err != nil {
		t.Fatal(err)
	}
	runner.now = func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) }
	processed, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || len(handler.events) != 1 || len(repository.completed) != 1 || len(repository.retried) != 0 {
		t.Fatalf("processed=%d handled=%d completed=%d retried=%d", processed, len(handler.events), len(repository.completed), len(repository.retried))
	}
}

func TestRunnerRetriesTransientFailuresAndDeadLettersTerminalFailures(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		terminal bool
	}{
		{name: "transient", attempts: 2},
		{name: "terminal", attempts: 10, terminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := testOutboxEvent(test.attempts)
			repository := &fakeOutbox{events: []model.OutboxEvent{event}}
			handler := &fakeHandler{err: errors.New("SMTP unavailable with secret token abc")}
			runner, err := NewRunner(repository, map[string]Handler{"email.verification.requested": handler})
			if err != nil {
				t.Fatal(err)
			}
			fixedNow := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
			runner.now = func() time.Time { return fixedNow }
			if _, err = runner.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(repository.retried) != 1 {
				t.Fatalf("retries = %d", len(repository.retried))
			}
			retry := repository.retried[0]
			if retry.Terminal != test.terminal || retry.AvailableAt.Before(fixedNow) {
				t.Fatalf("retry = %#v", retry)
			}
			if retry.LastError != "event handler failed" {
				t.Fatalf("retry error = %q", retry.LastError)
			}
		})
	}
}

func TestRunnerDeadLettersUnknownEventType(t *testing.T) {
	event := testOutboxEvent(1)
	event.EventType = "unknown.event"
	repository := &fakeOutbox{events: []model.OutboxEvent{event}}
	runner, err := NewRunner(repository, map[string]Handler{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.retried) != 1 || !repository.retried[0].Terminal {
		t.Fatalf("unknown event retry = %#v", repository.retried)
	}
}

func testOutboxEvent(attempts int) model.OutboxEvent {
	return model.OutboxEvent{
		ID:        uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		UserID:    uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		EventType: "email.verification.requested", Attempts: attempts,
		LockToken: uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
	}
}
