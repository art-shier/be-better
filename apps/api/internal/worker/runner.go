package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type OutboxRepository interface {
	Claim(context.Context, int, uuid.UUID, time.Duration) ([]model.OutboxEvent, error)
	Complete(context.Context, uuid.UUID, uuid.UUID) error
	Retry(context.Context, model.OutboxRetry) error
}

type Handler interface {
	Handle(context.Context, model.OutboxEvent) error
}

type Runner struct {
	repository  OutboxRepository
	handlers    map[string]Handler
	batchSize   int
	staleAfter  time.Duration
	maxAttempts int
	now         func() time.Time
	newUUID     func() (uuid.UUID, error)
}

func NewRunner(repository OutboxRepository, handlers map[string]Handler) (*Runner, error) {
	if repository == nil {
		return nil, errors.New("outbox repository is required")
	}
	copyHandlers := make(map[string]Handler, len(handlers))
	for eventType, handler := range handlers {
		if eventType == "" || handler == nil {
			return nil, errors.New("outbox handlers require a type and implementation")
		}
		copyHandlers[eventType] = handler
	}
	return &Runner{
		repository: repository, handlers: copyHandlers, batchSize: 25,
		staleAfter: 5 * time.Minute, maxAttempts: 10,
		now: func() time.Time { return time.Now().UTC() }, newUUID: uuid.NewRandom,
	}, nil
}

func (runner *Runner) RunOnce(ctx context.Context) (int, error) {
	lockToken, err := runner.newUUID()
	if err != nil {
		return 0, fmt.Errorf("generate outbox lock token: %w", err)
	}
	events, err := runner.repository.Claim(ctx, runner.batchSize, lockToken, runner.staleAfter)
	if err != nil {
		return 0, fmt.Errorf("claim outbox events: %w", err)
	}
	for _, event := range events {
		handler, exists := runner.handlers[event.EventType]
		if !exists {
			if err = runner.repository.Retry(ctx, model.OutboxRetry{
				EventID: event.ID, LockToken: event.LockToken, AvailableAt: runner.now().UTC(),
				LastError: "unsupported event type", Terminal: true,
			}); err != nil {
				return 0, fmt.Errorf("dead-letter unsupported outbox event: %w", err)
			}
			continue
		}
		if handleErr := handler.Handle(ctx, event); handleErr != nil {
			terminal := event.Attempts >= runner.maxAttempts
			retryAt := runner.now().UTC().Add(retryDelay(event.Attempts))
			if err = runner.repository.Retry(ctx, model.OutboxRetry{
				EventID: event.ID, LockToken: event.LockToken, AvailableAt: retryAt,
				LastError: "event handler failed", Terminal: terminal,
			}); err != nil {
				return 0, fmt.Errorf("retry outbox event: %w", err)
			}
			continue
		}
		if err = runner.repository.Complete(ctx, event.ID, event.LockToken); err != nil {
			return 0, fmt.Errorf("complete outbox event: %w", err)
		}
	}
	return len(events), nil
}

func (runner *Runner) Run(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return errors.New("worker poll interval must be positive")
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := runner.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := 5 * time.Second
	for count := 1; count < attempts && delay < time.Hour; count++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}
