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
	DevAuth bool

	// Email (magic-link delivery). When ResendAPIKey is empty, links are only
	// logged (dev mailer). MailFrom must be a Resend-verified sender in prod.
	ResendAPIKey string
	MailFrom     string
}

func Load() Config {
	return Config{
		Addr:          env("CADENCE_ADDR", ":8088"),
		Backend:       env("CADENCE_BACKEND", "memory"),
		DatabaseURL:   env("DATABASE_URL", ""),
		AutoMigrate:   envBool("CADENCE_AUTO_MIGRATE", true),
		WebOrigins:    splitCSV(env("CADENCE_WEB_ORIGINS", "localhost:*,127.0.0.1:*")),
		WebURL:        strings.TrimRight(env("CADENCE_WEB_URL", "http://localhost:4173"), "/"),
		CookieName:    env("CADENCE_COOKIE", "cadence_session"),
		CookieSecure:  envBool("CADENCE_COOKIE_SECURE", false),
		SessionTTL:    time.Duration(envInt("CADENCE_SESSION_DAYS", 30)) * 24 * time.Hour,
		LoginTokenTTL: time.Duration(envInt("CADENCE_LOGIN_MINUTES", 15)) * time.Minute,
		DevAuth:       envBool("CADENCE_DEV_AUTH", true),
		ResendAPIKey:  env("RESEND_API_KEY", ""),
		MailFrom:      env("MAIL_FROM", ""),
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
