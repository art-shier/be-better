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

	"dayorder.local/api/internal/auth"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/httpapi"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/observability"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
)

func main() {
	logger := observability.NewLogger(os.Stdout, "api", slog.LevelInfo)
	configuration, err := config.Load()
	if err != nil {
		logger.Error("invalid server configuration", "error", err)
		os.Exit(1)
	}
	startupContext, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := database.Open(startupContext, configuration.Database)
	startupCancel()
	if err != nil {
		logger.Error("open API database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	metrics := observability.NewMetrics("api", pool, nil)
	restorePasswordObserver := auth.SetPasswordObserver(metrics.ObservePasswordOperation)
	defer restorePasswordObserver()
	repository, err := postgresstore.NewAccountRepository(pool)
	if err != nil {
		logger.Error("create account repository", "error", err)
		os.Exit(1)
	}
	accounts, err := service.NewAccountService(repository)
	if err != nil {
		logger.Error("create account service", "error", err)
		os.Exit(1)
	}
	sessions, err := service.NewSessionService(repository, repository, configuration.AuthHMACKey)
	if err != nil {
		logger.Error("create session service", "error", err)
		os.Exit(1)
	}
	transactor, err := database.NewPoolTransactor(pool)
	if err != nil {
		logger.Error("create transaction coordinator", "error", err)
		os.Exit(1)
	}
	idempotency, err := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	if err != nil {
		logger.Error("create idempotency service", "error", err)
		os.Exit(1)
	}
	syncService, err := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, configuration.AuthHMACKey)
	if err != nil {
		logger.Error("create sync service", "error", err)
		os.Exit(1)
	}
	auditRepository := postgresstore.NewAuditRepository()
	auditService, err := service.NewAuditService(auditRepository)
	if err != nil {
		logger.Error("create audit service", "error", err)
		os.Exit(1)
	}
	commands, err := service.NewCommandService(transactor, idempotency, syncService, auditService, postgresstore.NewOutboxWriter())
	if err != nil {
		logger.Error("create command service", "error", err)
		os.Exit(1)
	}
	cursors, err := service.NewResourceCursorCodec(configuration.AuthHMACKey)
	if err != nil {
		logger.Error("create resource cursor codec", "error", err)
		os.Exit(1)
	}
	goals, err := service.NewGoalService(postgresstore.NewGoalRepository(), transactor, commands, cursors)
	if err != nil {
		logger.Error("create goal service", "error", err)
		os.Exit(1)
	}
	tasks, err := service.NewTaskService(postgresstore.NewTaskRepository(), transactor, commands, cursors)
	if err != nil {
		logger.Error("create task service", "error", err)
		os.Exit(1)
	}
	calendar, err := service.NewCalendarService(postgresstore.NewCalendarRepository(), transactor, commands, cursors)
	if err != nil {
		logger.Error("create calendar service", "error", err)
		os.Exit(1)
	}
	content, err := service.NewContentService(postgresstore.NewContentRepository(), transactor, commands, cursors)
	if err != nil {
		logger.Error("create content service", "error", err)
		os.Exit(1)
	}
	settings, err := service.NewSettingsService(postgresstore.NewSettingsRepository(), transactor, commands)
	if err != nil {
		logger.Error("create settings service", "error", err)
		os.Exit(1)
	}
	devices, err := service.NewDeviceService(postgresstore.NewDeviceRepository(), transactor, auditService)
	if err != nil {
		logger.Error("create device service", "error", err)
		os.Exit(1)
	}
	agents, err := service.NewAgentService(postgresstore.NewAgentRepository(), transactor, commands, cursors)
	if err != nil {
		logger.Error("create agent service", "error", err)
		os.Exit(1)
	}
	audits, err := service.NewAuditQueryService(auditRepository, transactor, cursors)
	if err != nil {
		logger.Error("create audit query service", "error", err)
		os.Exit(1)
	}
	undos, err := service.NewUndoService(auditRepository, commands)
	if err != nil {
		logger.Error("create undo service", "error", err)
		os.Exit(1)
	}
	handler, err := httpapi.NewRouter(httpapi.RouterOptions{
		Accounts: accounts, Sessions: sessions, Goals: goals, Tasks: tasks, Calendar: calendar,
		Content: content, Settings: settings, Devices: devices, Sync: syncService, Agents: agents, Audits: audits, Undos: undos, AllowedOrigins: configuration.AllowedOrigins, Logger: logger, Metrics: metrics,
		Ready: func(ctx context.Context) error {
			if err := database.Ping(ctx, pool, configuration.Database.HealthTimeout); err != nil {
				return err
			}
			var version uint
			var dirty bool
			if err := pool.QueryRow(ctx, "SELECT version, dirty FROM dayorder.schema_migrations LIMIT 1").Scan(&version, &dirty); err != nil {
				return err
			}
			return validateReadySchema(version, dirty)
		},
	})
	if err != nil {
		logger.Error("create API router", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: configuration.Address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
	}
	metricsServer := &http.Server{
		Addr: configuration.MetricsAddress, Handler: metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	go func() {
		logger.Info("DayOrder PostgreSQL API started", "addr", configuration.Address)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("serve PostgreSQL API", "error", serveErr)
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("DayOrder API metrics started", "addr", configuration.MetricsAddress)
		if serveErr := metricsServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("serve API metrics", "error", serveErr)
			os.Exit(1)
		}
	}()
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-shutdownContext.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = server.Shutdown(ctx); err != nil {
		logger.Error("graceful API shutdown", "error", err)
	}
	if err = metricsServer.Shutdown(ctx); err != nil {
		logger.Error("graceful API metrics shutdown", "error", err)
	}
}

func validateReadySchema(version uint, dirty bool) error {
	return dbmigrations.RequireCompatibleVersion(version, dirty)
}
