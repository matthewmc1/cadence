// Command cadence-server is the Cadence API + realtime backend.
//
//	go run ./cmd/cadence-server            # in-memory, runs anywhere
//	CADENCE_BACKEND=postgres DATABASE_URL=postgres://… go run ./cmd/cadence-server
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

	"github.com/cadence/server/internal/config"
	"github.com/cadence/server/internal/httpapi"
	"github.com/cadence/server/internal/mailer"
	"github.com/cadence/server/internal/store"
	"github.com/cadence/server/internal/store/memory"
	"github.com/cadence/server/internal/store/postgres"
)

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := newStore(ctx, cfg, log)
	if err != nil {
		log.Error("store init failed", "backend", cfg.Backend, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ml := mailer.New(cfg.ResendAPIKey, cfg.MailFrom, log)
	log.Info("mailer configured", "provider", mailerName(cfg.ResendAPIKey), "delivers", ml.Live())

	srv := httpapi.New(st, httpapi.Options{
		WebOrigins:    cfg.WebOrigins,
		WebURL:        cfg.WebURL,
		CookieName:    cfg.CookieName,
		CookieSecure:  cfg.CookieSecure,
		SessionTTL:    cfg.SessionTTL,
		LoginTokenTTL: cfg.LoginTokenTTL,
		DevAuth:       cfg.DevAuth,
		Mailer:        ml,
		Log:           log,
	})

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("cadence-server listening", "addr", cfg.Addr, "backend", cfg.Backend)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
}

func mailerName(apiKey string) string {
	if apiKey == "" {
		return "dev (log only)"
	}
	return "resend"
}

func newStore(ctx context.Context, cfg config.Config, log *slog.Logger) (store.Store, error) {
	switch cfg.Backend {
	case "postgres":
		return postgres.Open(ctx, postgres.Config{
			URL:         cfg.DatabaseURL,
			AutoMigrate: cfg.AutoMigrate,
			Log:         log,
		})
	default:
		return memory.New(), nil
	}
}
