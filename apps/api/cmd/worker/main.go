package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/auth"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	daymail "dayorder.local/api/internal/mail"
	"dayorder.local/api/internal/observability"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/worker"
)

func main() {
	logger := observability.NewLogger(os.Stdout, "worker", slog.LevelInfo)
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
	metrics := observability.NewMetrics("worker", pool, pool)
	restorePasswordObserver := auth.SetPasswordObserver(metrics.ObservePasswordOperation)
	defer restorePasswordObserver()
	metricsServer := &http.Server{
		Addr: configuration.MetricsAddress, Handler: metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	go func() {
		logger.Info("DayOrder worker metrics started", "addr", configuration.MetricsAddress)
		if serveErr := metricsServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("serve worker metrics", "error", serveErr)
			stop()
		}
	}()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if shutdownErr := metricsServer.Shutdown(shutdownContext); shutdownErr != nil {
			logger.Error("graceful worker metrics shutdown", "error", shutdownErr)
		}
	}()
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
	transactor, err := database.NewPoolTransactor(pool)
	if err != nil {
		logger.Error("create worker transaction coordinator", "error", err)
		os.Exit(1)
	}
	syncService, err := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, configuration.AuthHMACKey)
	if err != nil {
		logger.Error("create worker sync recorder", "error", err)
		os.Exit(1)
	}
	auditService, err := service.NewAuditService(postgresstore.NewAuditRepository())
	if err != nil {
		logger.Error("create worker audit recorder", "error", err)
		os.Exit(1)
	}
	var provider service.AgentProvider
	if configuration.Agent.Provider == "http" {
		provider, err = agentprovider.NewHTTPProvider(
			configuration.Agent.HTTPURL, configuration.Agent.HTTPKey,
			configuration.Agent.Model, configuration.Agent.Timeout,
		)
	} else {
		provider = service.NewDeterministicAgentProvider(nil)
	}
	if err != nil {
		logger.Error("create agent provider", "error", err)
		os.Exit(1)
	}
	agentProcessor, err := service.NewAgentProcessor(postgresstore.NewAgentRepository(), transactor, syncService, auditService, provider)
	if err != nil {
		logger.Error("create agent processor", "error", err)
		os.Exit(1)
	}
	agentHandler, err := worker.NewAgentHandler(agentProcessor)
	if err != nil {
		logger.Error("create agent worker handler", "error", err)
		os.Exit(1)
	}
	runner, err := worker.NewRunner(repository, map[string]worker.Handler{
		"email.verification.requested":   verificationHandler,
		"email.password_reset.requested": passwordResetHandler,
		"reminder.delivery.requested":    reminderHandler,
		"agent.run.requested":            agentHandler,
	})
	if err != nil {
		logger.Error("create worker runner", "error", err)
		os.Exit(1)
	}
	logger.Info("dayorder worker started", "mailSink", configuration.Mail.Sink, "agentProvider", configuration.Agent.Provider, "agentModel", configuration.Agent.Model)
	if err = runner.Run(ctx, configuration.PollInterval); err != nil {
		logger.Error("worker stopped after an error", "error", err)
		os.Exit(1)
	}
	logger.Info("dayorder worker stopped")
}
