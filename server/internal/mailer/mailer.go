// Package mailer delivers the passwordless magic-link sign-in email. A live
// Resend-backed sender is used when an API key is configured; otherwise a dev
// mailer just logs the link so local runs work with no email provider.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Mailer sends the sign-in link to a user's email.
type Mailer interface {
	// SendMagicLink delivers the sign-in link to `to`. Returns nil once the
	// provider has accepted the message for delivery.
	SendMagicLink(ctx context.Context, to, link string) error
	// Live reports whether this mailer actually delivers email (a live provider)
	// vs. only logging it (the dev fallback). The auth handler uses this to
	// decide whether it is safe to also hand the link back in the API response.
	Live() bool
}

// New returns a Resend mailer when apiKey is set, else a dev mailer that only
// logs the link (so `go run ./cmd/cadence-server` works with zero config).
func New(apiKey, from string, log *slog.Logger) Mailer {
	if log == nil {
		log = slog.Default()
	}
	if apiKey == "" {
		return &logMailer{log: log}
	}
	if from == "" {
		from = defaultFrom
	}
	return &resendMailer{
		apiKey: apiKey,
		from:   from,
		log:    log,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

const defaultFrom = "Cadence <onboarding@resend.dev>"

// ---- dev / no-op mailer ----------------------------------------------------

type logMailer struct{ log *slog.Logger }

func (m *logMailer) Live() bool { return false }
func (m *logMailer) SendMagicLink(_ context.Context, to, link string) error {
	m.log.Info("magic link (dev mailer — no email provider configured)", "to", to, "link", link)
	return nil
}

// ---- Resend mailer ---------------------------------------------------------

type resendMailer struct {
	apiKey string
	from   string
	log    *slog.Logger
	http   *http.Client
}

func (m *resendMailer) Live() bool { return true }

func (m *resendMailer) SendMagicLink(ctx context.Context, to, link string) error {
	payload := map[string]any{
		"from":    m.from,
		"to":      []string{to},
		"subject": "Your Cadence sign-in link",
		"html":    magicLinkHTML(link),
		"text":    magicLinkText(link),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("resend: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var out struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		m.log.Info("magic link email sent", "to", to, "messageId", out.ID)
		return nil
	}
	// Surface the provider's error (e.g. unverified domain) without leaking the key.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("resend: status %d: %s", resp.StatusCode, string(b))
}

// magicLinkHTML is a small, self-contained branded email. The link is
// html-escaped defensively even though the token is URL-safe base64.
func magicLinkHTML(link string) string {
	safe := html.EscapeString(link)
	return `<!doctype html><html><body style="margin:0;background:#F4EFE6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <div style="max-width:460px;margin:0 auto;padding:40px 24px;">
    <div style="background:#FFFDF9;border:1px solid #E7DFD1;border-radius:18px;padding:34px;">
      <div style="font-size:20px;font-weight:600;letter-spacing:-0.01em;color:#211E18;margin-bottom:6px;">Cadence</div>
      <h1 style="font-size:22px;line-height:1.25;margin:16px 0 8px;color:#211E18;">Sign in to your workspace</h1>
      <p style="color:#5B554B;font-size:15px;line-height:1.55;margin:0 0 24px;">Click the button below to sign in — no password needed. This link can be used once and expires shortly.</p>
      <a href="` + safe + `" style="display:inline-block;background:#C2743D;color:#ffffff;text-decoration:none;font-weight:600;font-size:15px;padding:12px 22px;border-radius:10px;">Sign in &rarr;</a>
      <p style="color:#8A8577;font-size:13px;line-height:1.55;margin:26px 0 0;">If you didn't request this, you can safely ignore this email.</p>
    </div>
  </div>
</body></html>`
}

func magicLinkText(link string) string {
	return "Sign in to Cadence:\n" + link + "\n\nThis link can be used once and expires shortly. If you didn't request this, you can ignore this email."
}
