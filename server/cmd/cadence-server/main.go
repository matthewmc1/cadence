// Command cadence-server is the Cadence API + realtime backend.
//
//	go run ./cmd/cadence-server            # in-memory, runs anywhere
//	CADENCE_BACKEND=postgres DATABASE_URL=postgres://… go run ./cmd/cadence-server
//
// Subcommands (same env as the server):
//
//	cadence-server invite <email>…   # pre-create accounts so they pass CADENCE_SIGNUP=invite
//	cadence-server health            # GET /api/v1/health on CADENCE_ADDR; exit 0 iff ok (Docker HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cadence/server/internal/config"
	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/httpapi"
	"github.com/cadence/server/internal/mailer"
	"github.com/cadence/server/internal/store"
	"github.com/cadence/server/internal/store/memory"
	"github.com/cadence/server/internal/store/postgres"
	"github.com/cadence/server/internal/web"
)

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "invite":
			os.Exit(runInvite(ctx, cfg, log, os.Args[2:]))
		case "health":
			os.Exit(runHealth(cfg))
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q — run with no arguments to serve, or `invite <email>…` / `health`\n", os.Args[1])
			os.Exit(2)
		}
	}

	st, err := newStore(ctx, cfg, log)
	if err != nil {
		log.Error("store init failed", "backend", cfg.Backend, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	mailOpts := mailer.Options{
		SMTPHost:     cfg.SMTPHost,
		SMTPPort:     cfg.SMTPPort,
		SMTPUser:     cfg.SMTPUser,
		SMTPPass:     cfg.SMTPPass,
		SMTPStartTLS: cfg.SMTPStartTLS,
		ResendAPIKey: cfg.ResendAPIKey,
		From:         cfg.MailFrom,
		DevAuth:      cfg.DevAuth,
		Log:          log,
	}
	ml, err := mailer.New(mailOpts)
	if err != nil {
		// Misconfigured live mail (e.g. no MAIL_FROM) is a boot failure, not a
		// surprise at the first sign-in.
		log.Error("mailer init failed", "provider", mailOpts.Provider(), "err", err)
		os.Exit(1)
	}
	log.Info("mailer configured", "provider", mailOpts.Provider(), "delivers", ml.Live())

	// Safe defaults (config.Load picks them by backend + mailer); the two that
	// matter most on a shared host are made loud here.
	signup, err := httpapi.ParseSignupPolicy(cfg.Signup)
	if err != nil {
		log.Error("signup policy invalid", "err", err)
		os.Exit(1)
	}
	log.Info("signup policy", "mode", signup.String(), "admins", len(cfg.AdminEmails))
	if cfg.DevAuth {
		log.Warn("CADENCE_DEV_AUTH is on: magic links are returned in API responses and written to the log — set CADENCE_DEV_AUTH=false on any shared or persistent deployment")
	}
	if signup.Mode == httpapi.SignupInvite && len(cfg.AdminEmails) == 0 && cfg.Backend == "postgres" {
		log.Info("signup is invite-only with no CADENCE_ADMIN_EMAILS — create the first account with `cadence-server invite <email>`")
	}

	// AI gateway config is validated at boot for the same reason as mail: a
	// typo in OLLAMA_URL should fail loudly now, not on the first "Plan my week".
	ollamaURL, err := httpapi.ParseOllamaURL(cfg.OllamaURL)
	if err != nil {
		log.Error("ai gateway config invalid", "err", err)
		os.Exit(1)
	}
	aiPolicy, err := httpapi.ParseAIPolicy(cfg.AIPolicy)
	if err != nil {
		log.Error("ai gateway config invalid", "err", err)
		os.Exit(1)
	}
	if cfg.ModelDir != "" {
		if st, err := os.Stat(cfg.ModelDir); err != nil || !st.IsDir() {
			log.Error("ai gateway config invalid", "err", "CADENCE_MODEL_DIR is not a directory", "dir", cfg.ModelDir)
			os.Exit(1)
		}
	}
	log.Info("ai gateway configured", "policy", aiPolicy, "ollama", ollamaURL != "", "localModels", cfg.ModelDir != "")

	// The web app is embedded when the binary was built after `make web-embed`
	// (the Dockerfile always does); a plain `go run` serves the API only and
	// the Vite dev server fronts the app.
	webFS, embedded := web.Dist()
	if embedded {
		log.Info("serving embedded web app at /")
	} else {
		log.Info("no embedded web app (API only) — `make web-embed` bakes it into the binary")
	}

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
		Signup:        signup,
		AdminEmails:   cfg.AdminEmails,
		AuditSalt:     cfg.AuditSalt,
		Web:           webFS,
		OllamaURL:     ollamaURL,
		ModelDir:      cfg.ModelDir,
		AIPolicy:      aiPolicy,
		// AI calls are audited into the tenant's audit_log (one ai.chat row per
		// call) rather than only to stdout.
		Audit: httpapi.StoreAudit{Store: st, Log: log},
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

// runInvite pre-creates accounts so the addresses pass an invite-only signup
// policy: an invite is nothing more than an account that already exists when
// the magic link is requested. Idempotent; prints one line per address.
func runInvite(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cadence-server invite <email> [<email>…]")
		return 2
	}
	var emails []string
	for _, a := range args {
		e := domain.NormalizeEmail(a)
		if !domain.ValidEmail(e) {
			fmt.Fprintf(os.Stderr, "invite: %q is not an email address\n", a)
			return 2
		}
		emails = append(emails, e)
	}
	if cfg.Backend != "postgres" {
		// The memory store forgets the account the moment this process exits,
		// so an invite there is a no-op that would only look like it worked.
		fmt.Fprintln(os.Stderr, "invite: CADENCE_BACKEND must be postgres (the in-memory store cannot keep an invite)")
		return 1
	}
	st, err := newStore(ctx, cfg, log)
	if err != nil {
		log.Error("store init failed", "backend", cfg.Backend, "err", err)
		return 1
	}
	defer st.Close()
	for _, e := range emails {
		acc, err := st.FindOrCreateAccount(ctx, e)
		if err != nil {
			log.Error("invite failed", "email", mailer.Redact(e), "err", err)
			return 1
		}
		fmt.Printf("invited %s (tenant %s)\n", e, acc.TenantID)
	}
	return 0
}

// runHealth is the container HEALTHCHECK: the final image is distroless (no
// curl/wget), so the binary probes itself. Exit 0 iff /api/v1/health is 200.
func runHealth(cfg config.Config) int {
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health: CADENCE_ADDR %q is not host:port\n", cfg.Addr)
		return 2
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, "health:", err)
		return 1
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
	if res.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health: %d %s\n", res.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}
	return 0
}

func newStore(ctx context.Context, cfg config.Config, log *slog.Logger) (store.Store, error) {
	switch cfg.Backend {
	case "postgres":
		return postgres.Open(ctx, postgres.Config{
			URL:             cfg.DatabaseURL,
			AutoMigrate:     cfg.AutoMigrate,
			Log:             log,
			OutboxRetention: cfg.OutboxRetention,
		})
	default:
		return memory.New(), nil
	}
}
