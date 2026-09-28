package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"dayorder.local/api/internal/model"
)

type readonlyProcessorFunc func(context.Context, model.OutboxEvent) error

func (process readonlyProcessorFunc) Process(ctx context.Context, event model.OutboxEvent) error {
	return process(ctx, event)
}

func TestReadonlyAgentHandlerDelegatesTheClaimedEvent(t *testing.T) {
	want := testOutboxEvent(1)
	called := false
	handler, err := NewReadonlyAgentHandler(readonlyProcessorFunc(func(_ context.Context, got model.OutboxEvent) error {
		called = true
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Process event = %#v, want %#v", got, want)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = handler.Handle(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Process was not called")
	}
}

func TestReadonlyAgentHandlerPreservesProcessorFailure(t *testing.T) {
	want := errors.New("database unavailable")
	handler, err := NewReadonlyAgentHandler(readonlyProcessorFunc(func(context.Context, model.OutboxEvent) error {
		return want
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = handler.Handle(context.Background(), testOutboxEvent(1)); !errors.Is(err, want) {
		t.Fatalf("Handle error = %v, want %v", err, want)
	}
}

func TestReadonlyAgentHandlerRejectsNilProcessor(t *testing.T) {
	if _, err := NewReadonlyAgentHandler(nil); err == nil {
		t.Fatal("NewReadonlyAgentHandler accepted nil")
	}
}
