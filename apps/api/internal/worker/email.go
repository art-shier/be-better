package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"strings"

	daymail "dayorder.local/api/internal/mail"
	"dayorder.local/api/internal/model"
)

type VerificationEmailHandler struct {
	sender    daymail.Sender
	publicURL string
}

type PasswordResetEmailHandler struct {
	sender    daymail.Sender
	publicURL string
}

type verificationPayload struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Token       string `json:"token"`
}

func NewVerificationEmailHandler(sender daymail.Sender, publicURL string) (*VerificationEmailHandler, error) {
	validatedURL, err := validateEmailHandlerOptions(sender, publicURL)
	if err != nil {
		return nil, err
	}
	return &VerificationEmailHandler{sender: sender, publicURL: validatedURL}, nil
}

func NewPasswordResetEmailHandler(sender daymail.Sender, publicURL string) (*PasswordResetEmailHandler, error) {
	validatedURL, err := validateEmailHandlerOptions(sender, publicURL)
	if err != nil {
		return nil, err
	}
	return &PasswordResetEmailHandler{sender: sender, publicURL: validatedURL}, nil
}

func validateEmailHandlerOptions(sender daymail.Sender, publicURL string) (string, error) {
	if sender == nil {
		return "", errors.New("email sender is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("public URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("public URL cannot contain a query or fragment")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func (handler *VerificationEmailHandler) Handle(ctx context.Context, event model.OutboxEvent) error {
	payload, err := parseTokenEmailPayload(event.Payload)
	if err != nil {
		return err
	}
	verificationURL := handler.publicURL + "/verify-email?token=" + url.QueryEscape(payload.Token)
	message := daymail.Message{
		To: payload.Email, Subject: "验证你的日序账号",
		Text: fmt.Sprintf("%s，你好：\n\n请打开以下链接验证邮箱：\n%s\n\n如果不是你发起的注册，请忽略本邮件。", payload.DisplayName, verificationURL),
		HTML: fmt.Sprintf(
			"<p>%s，你好：</p><p>请点击下面的链接验证邮箱：</p><p><a href=\"%s\">验证邮箱</a></p><p>如果不是你发起的注册，请忽略本邮件。</p>",
			html.EscapeString(payload.DisplayName), html.EscapeString(verificationURL),
		),
	}
	if err = handler.sender.Send(ctx, message); err != nil {
		return fmt.Errorf("send verification email: %w", err)
	}
	return nil
}

func (handler *PasswordResetEmailHandler) Handle(ctx context.Context, event model.OutboxEvent) error {
	payload, err := parseTokenEmailPayload(event.Payload)
	if err != nil {
		return err
	}
	resetURL := handler.publicURL + "/reset-password?token=" + url.QueryEscape(payload.Token)
	message := daymail.Message{
		To: payload.Email, Subject: "重置你的日序密码",
		Text: fmt.Sprintf("%s，你好：\n\n请打开以下链接重置密码：\n%s\n\n如果不是你发起的请求，请忽略本邮件。", payload.DisplayName, resetURL),
		HTML: fmt.Sprintf(
			"<p>%s，你好：</p><p>请点击下面的链接重置密码：</p><p><a href=\"%s\">重置密码</a></p><p>如果不是你发起的请求，请忽略本邮件。</p>",
			html.EscapeString(payload.DisplayName), html.EscapeString(resetURL),
		),
	}
	if err = handler.sender.Send(ctx, message); err != nil {
		return fmt.Errorf("send password reset email: %w", err)
	}
	return nil
}

func parseTokenEmailPayload(contents json.RawMessage) (verificationPayload, error) {
	var payload verificationPayload
	if err := json.Unmarshal(contents, &payload); err != nil {
		return verificationPayload{}, errors.New("invalid token email payload")
	}
	payload.Email = strings.TrimSpace(payload.Email)
	payload.DisplayName = strings.TrimSpace(payload.DisplayName)
	payload.Token = strings.TrimSpace(payload.Token)
	address, err := mail.ParseAddress(payload.Email)
	if err != nil || address.Address != payload.Email || payload.Token == "" {
		return verificationPayload{}, errors.New("invalid token email payload")
	}
	if payload.DisplayName == "" {
		payload.DisplayName = "日序用户"
	}
	return payload, nil
}
