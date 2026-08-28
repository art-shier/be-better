package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	daymail "dayorder.local/api/internal/mail"
	"dayorder.local/api/internal/model"
)

type fakeSender struct{ messages []daymail.Message }

func (sender *fakeSender) Send(_ context.Context, message daymail.Message) error {
	sender.messages = append(sender.messages, message)
	return nil
}

func TestVerificationEmailBuildsEscapedLinkAndDoesNotExposeTokenInSubject(t *testing.T) {
	sender := &fakeSender{}
	handler, err := NewVerificationEmailHandler(sender, "https://dayorder.example")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{
		"email": "user@example.com", "displayName": "日序用户", "token": "token/with+symbols",
	})
	event := testOutboxEvent(1)
	event.Payload = payload
	if err = handler.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %d", len(sender.messages))
	}
	message := sender.messages[0]
	if message.To != "user@example.com" || strings.Contains(message.Subject, "token/with+symbols") {
		t.Fatalf("message metadata = %#v", message)
	}
	if !strings.Contains(message.Text, "token%2Fwith%2Bsymbols") || !strings.Contains(message.HTML, "token%2Fwith%2Bsymbols") {
		t.Fatalf("verification link was not URL-escaped: text=%q html=%q", message.Text, message.HTML)
	}
}

func TestVerificationEmailRejectsMalformedPayload(t *testing.T) {
	handler, err := NewVerificationEmailHandler(&fakeSender{}, "https://dayorder.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"not-json", `{}`, `{"email":"bad","token":"token"}`} {
		event := model.OutboxEvent{Payload: []byte(payload)}
		if err = handler.Handle(context.Background(), event); err == nil {
			t.Errorf("payload %q unexpectedly accepted", payload)
		}
	}
}
