package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dayorder.local/api/internal/httpapi"
	"dayorder.local/api/internal/store"
)

func main() {
	addr := flag.String("addr", envOr("DAYORDER_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	dbPath := flag.String("db", envOr("DAYORDER_DB_PATH", filepath.Join("data", "dayorder.db")), "SQLite database path")
	webDir := flag.String("web-dir", os.Getenv("DAYORDER_WEB_DIR"), "optional built web app directory")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	storage, err := store.Open(*dbPath)
	if err != nil {
		logger.Error("open storage", "error", err)
		os.Exit(1)
	}
	defer storage.Close()

	api := httpapi.New(storage, httpapi.Options{
		AllowedOrigins: splitCSV(envOr("DAYORDER_ALLOWED_ORIGINS", "http://127.0.0.1:5173,http://localhost:5173")),
		Logger:         logger,
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	if *webDir != "" {
		spa, err := newSPAHandler(*webDir)
		if err != nil {
			logger.Error("configure web assets", "error", err)
			os.Exit(1)
		}
		mux.Handle("/", spa)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"name":"DayOrder API","health":"/api/v1/health"}`))
		})
	}

	server := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		logger.Info("DayOrder server started", "addr", "http://"+*addr, "database", *dbPath, "webDir", *webDir)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "error", err)
			os.Exit(1)
		}
	}()

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-shutdownContext.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown", "error", err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func newSPAHandler(root string) (http.Handler, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(absolute, "index.html")
	if _, err = os.Stat(indexPath); err != nil {
		return nil, err
	}
	files := http.FileServer(http.Dir(absolute))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleaned := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		candidate := filepath.Join(absolute, cleaned)
		relative, relErr := filepath.Rel(absolute, candidate)
		if relErr != nil || strings.HasPrefix(relative, "..") {
			http.NotFound(w, r)
			return
		}
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			if strings.Contains(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexPath)
	}), nil
}
