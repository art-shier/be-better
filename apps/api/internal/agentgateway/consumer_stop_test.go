package agentgateway

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
)

func TestTurnConsumerStopDoesNotPublishAgainAndStillRequestsSettlement(t *testing.T) {
	for _, stopEarly := range []bool{true, false} {
		name := "consumer stops after text"
		if !stopEarly {
			name = "active consumer still receives terminal error"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			adapter := &consumerStopAdapter{cancel: cancel}
			turn := &Turn{
				ctx: ctx, cancelCtx: cancel, profile: Profile{Adapter: adapter},
				done: make(chan struct{}), consumerDone: make(chan struct{}),
				terminalReady: make(chan terminalRequest, 1),
			}

			// Substitute only the database-backed terminal acknowledgement. The
			// real Events/consume/cancellation/handoff path must request settlement.
			// The lifecycle integration test covers actual persistence/accounting.
			settled := make(chan terminalRequest, 1)
			settlementCtx, stopSettlement := context.WithTimeout(context.Background(), time.Second)
			go func() {
				defer close(turn.done)
				select {
				case request := <-turn.terminalReady:
					settled <- request
					turn.finalErr = &agentexecution.Error{Agent: agentprotocol.AgentError{
						Code: agentprotocol.ErrorCodeInternalError, Message: "execution_interrupted", Retryable: false,
					}}
				case <-settlementCtx.Done():
				}
			}()
			t.Cleanup(func() { stopSettlement(); <-turn.done })

			texts, failures := 0, 0
			for event, err := range turn.Events(nil) {
				if err != nil {
					var failure *agentexecution.Error
					if !errors.As(err, &failure) || failure.Agent.Code != agentprotocol.ErrorCodeInternalError {
						t.Fatalf("terminal error = %v, want internal_error", err)
					}
					failures++
					continue
				}
				if event.Type != agentprotocol.ProviderEventTypeTextDelta || event.Text == nil || *event.Text != "first" {
					t.Fatal("unexpected provider event")
				}
				texts++
				if stopEarly {
					break
				}
			}
			wantFailures := 1
			if stopEarly {
				wantFailures = 0
			}
			if texts != 1 || failures != wantFailures || adapter.calls != 1 || !adapter.exited || adapter.accepted == stopEarly {
				t.Fatalf("texts=%d failures=%d calls=%d exited=%t accepted=%t", texts, failures, adapter.calls, adapter.exited, adapter.accepted)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("consumer stop did not cancel the provider context")
			}
			select {
			case request := <-settled:
				if request.success || request.alreadySettled || request.code != agentprotocol.ErrorCodeInternalError || request.usage != (agentprotocol.Usage{}) {
					t.Fatalf("settlement request = %+v, want failed/interrupted with unknown usage", request)
				}
			case <-settlementCtx.Done():
				t.Fatal("consumer exit skipped terminal settlement")
			}
		})
	}
}

type consumerStopAdapter struct {
	cancel   context.CancelCauseFunc
	calls    int
	accepted bool
	exited   bool
}

func (adapter *consumerStopAdapter) Stream(context.Context, agentprotocol.ModelTurnRequest, agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	adapter.calls++
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		defer func() { adapter.exited = true }()
		text := "first"
		adapter.accepted = yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil)
		if adapter.accepted {
			adapter.cancel(context.Canceled)
		}
	}
}
