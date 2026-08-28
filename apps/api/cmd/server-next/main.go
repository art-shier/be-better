package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/httpapi"
	dbmigrations "dayorder.local/api/internal/migrations"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	handler, err := httpapi.NewRouter(httpapi.RouterOptions{
		Accounts: accounts, Sessions: sessions, AllowedOrigins: configuration.AllowedOrigins, Logger: logger,
		Ready: func(ctx context.Context) error {
			if err := database.Ping(ctx, pool, configuration.Database.HealthTimeout); err != nil {
				return err
			}
			var version int
			var dirty bool
			if err := pool.QueryRow(ctx, "SELECT version, dirty FROM dayorder.schema_migrations LIMIT 1").Scan(&version, &dirty); err != nil {
				return err
			}
			if dirty || version != int(dbmigrations.LatestVersion) {
				return fmt.Errorf("database schema version is not current")
			}
			return nil
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
	go func() {
		logger.Info("DayOrder PostgreSQL API started", "addr", configuration.Address)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("serve PostgreSQL API", "error", serveErr)
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
}
