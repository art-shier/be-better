package mail

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewSMTPSenderValidatesTransportAndAddresses(t *testing.T) {
	tests := []SMTPConfig{
		{},
		{Address: "missing-port", From: "noreply@example.com", TLSMode: SMTPStartTLS},
		{Address: "smtp.example.com:587", From: "not-an-email", TLSMode: SMTPStartTLS},
		{Address: "smtp.example.com:587", From: "noreply@example.com", TLSMode: "invalid"},
		{Address: "smtp.example.com:587", From: "noreply@example.com", TLSMode: SMTPStartTLS, Username: "user"},
	}
	for _, config := range tests {
		if _, err := NewSMTPSender(config); err == nil {
			t.Errorf("config %#v unexpectedly accepted", config)
		}
	}
	if _, err := NewSMTPSender(SMTPConfig{
		Address: "smtp.example.com:587", From: "日序 <noreply@example.com>",
		TLSMode: SMTPStartTLS, Timeout: 10 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPMessageHasMultipartBodiesAndRejectsHeaderInjection(t *testing.T) {
	sender, err := NewSMTPSender(SMTPConfig{
		Address: "smtp.example.com:587", From: "日序 <noreply@example.com>", TLSMode: SMTPStartTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := sender.buildMessage(Message{
		To: "user@example.com", Subject: "验证日序账号", Text: "plain body", HTML: "<p>html body</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(message)
	for _, fragment := range []string{
		"From:", "To: user@example.com", "Subject:", "multipart/alternative", "text/plain", "text/html", "plain body", "html body",
	} {
		if !strings.Contains(content, fragment) {
			t.Errorf("SMTP message is missing %q", fragment)
		}
	}
	for _, injected := range []Message{
		{To: "user@example.com\r\nBcc: attacker@example.com", Subject: "subject", Text: "body"},
		{To: "user@example.com", Subject: "subject\r\nBcc: attacker@example.com", Text: "body"},
	} {
		if _, err = sender.buildMessage(injected); err == nil {
			t.Errorf("header injection message %#v unexpectedly accepted", injected)
		}
	}
}

func TestSMTPSendHonorsCancelledContextBeforeDial(t *testing.T) {
	sender, err := NewSMTPSender(SMTPConfig{
		Address: "127.0.0.1:1", From: "noreply@example.com", TLSMode: SMTPNone, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = sender.Send(ctx, Message{To: "user@example.com", Subject: "subject", Text: "body"}); err == nil {
		t.Fatal("Send unexpectedly succeeded with a cancelled context")
	}
}
