package mail

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

type Sender interface {
	Send(context.Context, Message) error
}

type MetadataLogSender struct{ logger *slog.Logger }

func NewMetadataLogSender(logger *slog.Logger) (*MetadataLogSender, error) {
	if logger == nil {
		return nil, errors.New("mail metadata logger is required")
	}
	return &MetadataLogSender{logger: logger}, nil
}

func (sender *MetadataLogSender) Send(_ context.Context, message Message) error {
	if strings.TrimSpace(message.To) == "" || strings.TrimSpace(message.Subject) == "" {
		return errors.New("mail recipient and subject are required")
	}
	sender.logger.Info(
		"mail discarded by metadata-only development sink",
		"to", message.To,
		"subject", message.Subject,
		"textBytes", len(message.Text),
		"htmlBytes", len(message.HTML),
	)
	return nil
}
