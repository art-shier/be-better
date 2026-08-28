package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	daymail "dayorder.local/api/internal/mail"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	configuration, err := config.LoadWorker()
	if err != nil {
		logger.Error("invalid worker configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, configuration.Database)
	if err != nil {
		logger.Error("open worker database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	repository, err := postgresstore.NewOutboxRepository(pool)
	if err != nil {
		logger.Error("create outbox repository", "error", err)
		os.Exit(1)
	}

	var sender daymail.Sender
	if configuration.Mail.Sink == "smtp" {
		sender, err = daymail.NewSMTPSender(daymail.SMTPConfig{
			Address: configuration.Mail.Address, From: configuration.Mail.From,
			Username: configuration.Mail.Username, Password: configuration.Mail.Password,
			TLSMode: daymail.SMTPTLSMode(configuration.Mail.TLSMode), Timeout: configuration.Mail.Timeout,
		})
	} else {
		sender, err = daymail.NewMetadataLogSender(logger)
	}
	if err != nil {
		logger.Error("create mail sender", "error", err)
		os.Exit(1)
	}
	verificationHandler, err := worker.NewVerificationEmailHandler(sender, configuration.PublicURL)
	if err != nil {
		logger.Error("create verification email handler", "error", err)
		os.Exit(1)
	}
	passwordResetHandler, err := worker.NewPasswordResetEmailHandler(sender, configuration.PublicURL)
	if err != nil {
		logger.Error("create password reset email handler", "error", err)
		os.Exit(1)
	}
	reminderRepository, err := postgresstore.NewReminderDeliveryRepository(pool)
	if err != nil {
		logger.Error("create reminder delivery repository", "error", err)
		os.Exit(1)
	}
	reminderHandler, err := worker.NewReminderHandler(reminderRepository, sender)
	if err != nil {
		logger.Error("create reminder handler", "error", err)
		os.Exit(1)
	}
	runner, err := worker.NewRunner(repository, map[string]worker.Handler{
		"email.verification.requested":   verificationHandler,
		"email.password_reset.requested": passwordResetHandler,
		"reminder.delivery.requested":    reminderHandler,
	})
	if err != nil {
		logger.Error("create worker runner", "error", err)
		os.Exit(1)
	}
	logger.Info("dayorder worker started", "mailSink", configuration.Mail.Sink)
	if err = runner.Run(ctx, configuration.PollInterval); err != nil {
		logger.Error("worker stopped after an error", "error", err)
		os.Exit(1)
	}
	logger.Info("dayorder worker stopped")
}
