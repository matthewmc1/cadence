// Package config loads server configuration from the environment.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr string // listen address, e.g. ":8080"

	// Backend selects the store adapter: "memory" (default, runs anywhere) or
	// "postgres" (requires DatabaseURL).
	Backend     string
	DatabaseURL string
	AutoMigrate bool

	// WebOrigins is the CORS + WebSocket Origin allowlist (host[:port], `*`).
	WebOrigins []string
	// WebURL is where the SPA is served — used to build magic links.
	WebURL string

	// Auth
	CookieName    string
	CookieSecure  bool
	SessionTTL    time.Duration
	LoginTokenTTL time.Duration
	// DevAuth returns the magic link in the API response + logs it. Handy for
	// local dev; turn OFF in production so links are only delivered by email.
	// Default: on for the throwaway memory backend with no mail provider, off
	// as soon as data persists (postgres) or a live mailer is configured.
	DevAuth bool
	// Signup is the raw CADENCE_SIGNUP policy: open | invite | domains:<csv>
	// (httpapi.ParseSignupPolicy validates it). Default: invite on postgres,
	// open on memory — a persistent private host should not hand out
	// workspaces to anyone who can reach it.
	Signup string
	// AdminEmails may always sign in regardless of Signup (bootstraps the
	// first account on a fresh install without a CLI invite).
	AdminEmails []string
	// AuditSalt keys the HMAC behind audit_log.ip_hash. Unset ⇒ a random
	// per-boot key (irreversible, but hashes stop correlating across restarts).
	AuditSalt string

	// Email (magic-link delivery). Provider precedence: SMTP when SMTPHost is
	// set (self-hosted, no third-party egress) → Resend when ResendAPIKey is
	// set → dev mailer that only logs the link. A live provider requires
	// MailFrom; the server refuses to start without it.
	SMTPHost     string
	SMTPPort     int // 587 default (STARTTLS); 465 ⇒ implicit TLS
	SMTPUser     string
	SMTPPass     string
	SMTPStartTLS bool // default true; false only for a trusted local relay
	ResendAPIKey string
	MailFrom     string

	// OutboxRetention bounds the postgres outbox: published-or-not, rows older
	// than this are pruned hourly. Clients offline longer re-bootstrap.
	OutboxRetention time.Duration

	// AI gateway. The browser never talks to a model provider directly; every
	// call goes through the server so policy and egress logging are enforceable.
	OllamaURL string // "" ⇒ Ollama disabled; e.g. http://localhost:11434
	ModelDir  string // "" ⇒ WebLLM fetches weights from the upstream CDN; a dir ⇒ served at /models/
	AIPolicy  string // local (default) | local+cloud (validated + reported; no cloud provider yet)
}

func Load() Config {
	backend := env("CADENCE_BACKEND", "memory")
	persistent := backend == "postgres"
	// Safe defaults follow the deployment shape rather than a flag the operator
	// has to remember: the moment data persists or real mail goes out, dev
	// conveniences (links in API responses, open signup) are off unless asked for.
	liveMail := env("SMTP_HOST", "") != "" || env("RESEND_API_KEY", "") != ""
	devAuthDefault := !persistent && !liveMail
	signupDefault := "open"
	if persistent {
		signupDefault = "invite"
	}
	return Config{
		Addr:          env("CADENCE_ADDR", ":8088"),
		Backend:       backend,
		DatabaseURL:   env("DATABASE_URL", ""),
		AutoMigrate:   envBool("CADENCE_AUTO_MIGRATE", true),
		WebOrigins:    splitCSV(env("CADENCE_WEB_ORIGINS", "localhost:*,127.0.0.1:*")),
		WebURL:        strings.TrimRight(env("CADENCE_WEB_URL", "http://localhost:4173"), "/"),
		CookieName:    env("CADENCE_COOKIE", "cadence_session"),
		CookieSecure:  envBool("CADENCE_COOKIE_SECURE", false),
		SessionTTL:    time.Duration(envInt("CADENCE_SESSION_DAYS", 30)) * 24 * time.Hour,
		LoginTokenTTL: time.Duration(envInt("CADENCE_LOGIN_MINUTES", 15)) * time.Minute,
		DevAuth:       envBool("CADENCE_DEV_AUTH", devAuthDefault),
		Signup:        env("CADENCE_SIGNUP", signupDefault),
		AdminEmails:   splitCSV(env("CADENCE_ADMIN_EMAILS", "")),
		AuditSalt:     env("CADENCE_AUDIT_SALT", ""),
		SMTPHost:      env("SMTP_HOST", ""),
		SMTPPort:      envInt("SMTP_PORT", 587),
		SMTPUser:      env("SMTP_USER", ""),
		SMTPPass:      env("SMTP_PASS", ""),
		SMTPStartTLS:  envBool("SMTP_STARTTLS", true),
		ResendAPIKey:  env("RESEND_API_KEY", ""),
		MailFrom:      env("MAIL_FROM", ""),

		OutboxRetention: time.Duration(envInt("CADENCE_OUTBOX_RETENTION_DAYS", 7)) * 24 * time.Hour,

		OllamaURL: env("OLLAMA_URL", ""),
		ModelDir:  env("CADENCE_MODEL_DIR", ""),
		AIPolicy:  env("CADENCE_AI_POLICY", "local"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
