package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

type SMTPTLSMode string

const (
	SMTPStartTLS SMTPTLSMode = "starttls"
	SMTPImplicit SMTPTLSMode = "implicit"
	SMTPNone     SMTPTLSMode = "none"
)

type SMTPConfig struct {
	Address  string
	From     string
	Username string
	Password string
	TLSMode  SMTPTLSMode
	Timeout  time.Duration
}

type SMTPSender struct {
	config SMTPConfig
	host   string
	from   *stdmail.Address
}

func NewSMTPSender(config SMTPConfig) (*SMTPSender, error) {
	config.Address = strings.TrimSpace(config.Address)
	host, _, err := net.SplitHostPort(config.Address)
	if err != nil || host == "" {
		return nil, errors.New("SMTP address must include a host and port")
	}
	from, err := stdmail.ParseAddress(strings.TrimSpace(config.From))
	if err != nil || from.Address == "" || hasHeaderBreak(config.From) {
		return nil, errors.New("SMTP From must be a valid email address")
	}
	if config.TLSMode != SMTPStartTLS && config.TLSMode != SMTPImplicit && config.TLSMode != SMTPNone {
		return nil, errors.New("SMTP TLS mode must be starttls, implicit, or none")
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("SMTP username and password must be configured together")
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	return &SMTPSender{config: config, host: host, from: from}, nil
}

func (sender *SMTPSender) Send(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	contents, err := sender.buildMessage(message)
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: sender.config.Timeout}
	connection, err := dialer.DialContext(ctx, "tcp", sender.config.Address)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(sender.config.Timeout))
	}
	if sender.config.TLSMode == SMTPImplicit {
		tlsConnection := tls.Client(connection, sender.tlsConfig())
		if err = tlsConnection.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return fmt.Errorf("establish implicit SMTP TLS: %w", err)
		}
		connection = tlsConnection
	}
	client, err := smtp.NewClient(connection, sender.host)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer func() { _ = client.Close() }()
	if err = client.Hello("dayorder"); err != nil {
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	if sender.config.TLSMode == SMTPStartTLS {
		if err = client.StartTLS(sender.tlsConfig()); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if sender.config.Username != "" {
		authentication := smtp.PlainAuth("", sender.config.Username, sender.config.Password, sender.host)
		if err = client.Auth(authentication); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	if err = client.Mail(sender.from.Address); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	to, _ := stdmail.ParseAddress(strings.TrimSpace(message.To))
	if err = client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("open SMTP message body: %w", err)
	}
	if _, err = data.Write(contents); err != nil {
		_ = data.Close()
		return fmt.Errorf("write SMTP message body: %w", err)
	}
	if err = data.Close(); err != nil {
		return fmt.Errorf("finish SMTP message body: %w", err)
	}
	if err = client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

func (sender *SMTPSender) buildMessage(message Message) ([]byte, error) {
	message.To = strings.TrimSpace(message.To)
	message.Subject = strings.TrimSpace(message.Subject)
	to, err := stdmail.ParseAddress(message.To)
	if err != nil || to.Address != message.To || hasHeaderBreak(message.To) {
		return nil, errors.New("message recipient must be a plain valid email address")
	}
	if message.Subject == "" || hasHeaderBreak(message.Subject) {
		return nil, errors.New("message subject is invalid")
	}
	if message.Text == "" && message.HTML == "" {
		return nil, errors.New("message body is required")
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	_, _ = fmt.Fprintf(&buffer, "From: %s\r\n", sender.from.String())
	_, _ = fmt.Fprintf(&buffer, "To: %s\r\n", to.Address)
	_, _ = fmt.Fprintf(&buffer, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", message.Subject))
	_, _ = fmt.Fprintf(&buffer, "MIME-Version: 1.0\r\n")
	_, _ = fmt.Fprintf(&buffer, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", writer.Boundary())
	if message.Text != "" {
		if err = writeMIMEPart(writer, "text/plain; charset=UTF-8", message.Text); err != nil {
			return nil, err
		}
	}
	if message.HTML != "" {
		if err = writeMIMEPart(writer, "text/html; charset=UTF-8", message.HTML); err != nil {
			return nil, err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, fmt.Errorf("finish MIME message: %w", err)
	}
	return buffer.Bytes(), nil
}

func (sender *SMTPSender) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: sender.host, MinVersion: tls.VersionTLS12}
}

func writeMIMEPart(writer *multipart.Writer, contentType, body string) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create MIME part: %w", err)
	}
	quoted := quotedprintable.NewWriter(part)
	if _, err = io.WriteString(quoted, body); err != nil {
		_ = quoted.Close()
		return fmt.Errorf("write MIME part: %w", err)
	}
	if err = quoted.Close(); err != nil {
		return fmt.Errorf("finish MIME part: %w", err)
	}
	return nil
}

func hasHeaderBreak(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}
