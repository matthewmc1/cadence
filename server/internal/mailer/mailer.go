// Package mailer delivers the passwordless magic-link sign-in email.
//
// Delivery is provider-agnostic behind the Mailer interface. Selection order:
//
//  1. SMTP  — when SMTP_HOST is set. Talks to any mail server (your own
//     Postfix, a Mailcow box, a relay) with STARTTLS or implicit TLS, so a
//     self-hosted Cadence never has to hand user emails to a third party.
//  2. Resend — when RESEND_API_KEY is set and no SMTP host is configured.
//     Kept as an optional hosted convenience.
//  3. Dev    — otherwise. Nothing leaves the process; the link is logged so
//     `go run ./cmd/cadence-server` works with zero config.
//
// A live provider (1 or 2) requires MAIL_FROM: there is no sensible default
// sender for someone else's domain, and refusing at boot beats a bounced
// sign-in email at 2am.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
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

// Options selects and configures a Mailer. Zero-value Options yields the dev
// mailer. See the package comment for precedence.
type Options struct {
	// SMTP transport. Host set ⇒ SMTP is used.
	SMTPHost     string
	SMTPPort     int // default 587; 465 implies implicit TLS
	SMTPUser     string
	SMTPPass     string
	SMTPStartTLS bool // upgrade the connection with STARTTLS (default true in config)

	// Resend transport (used only when SMTPHost is empty).
	ResendAPIKey string

	// From is the sender in RFC 5322 form, e.g. `Cadence <cadence@example.com>`.
	// Required for a live mailer; the dev mailer falls back to defaultFrom.
	From string

	// DevAuth mirrors config.DevAuth. When false the dev mailer redacts the
	// link from its log line too — a bearer credential shouldn't sit in logs
	// just because no provider is configured.
	DevAuth bool

	Log *slog.Logger
}

// Provider names the transport an Options would select ("smtp", "resend" or
// "dev"). Used for the startup log line.
func (o Options) Provider() string {
	switch {
	case o.SMTPHost != "":
		return "smtp"
	case o.ResendAPIKey != "":
		return "resend"
	default:
		return "dev"
	}
}

// defaultFrom is the nominal sender when no MAIL_FROM is configured. Only the
// dev mailer (which never sends) runs in that state; live mailers must set
// MAIL_FROM explicitly — there is no third-party sender to fall back to.
const defaultFrom = "Cadence <cadence@localhost>"

// New builds the Mailer selected by opts. It fails fast — rather than at the
// first sign-in — when a live provider is configured without a usable From
// address or with a malformed SMTP setup.
func New(opts Options) (Mailer, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	provider := opts.Provider()
	if provider == "dev" {
		return &logMailer{log: log, devAuth: opts.DevAuth}, nil
	}

	if strings.TrimSpace(opts.From) == "" {
		return nil, fmt.Errorf("mailer: %s is configured but MAIL_FROM is empty — set it to the sender address (e.g. `Cadence <cadence@your.domain>`)", provider)
	}
	fromAddr, err := mail.ParseAddress(opts.From)
	if err != nil {
		return nil, fmt.Errorf("mailer: MAIL_FROM %q is not a valid address: %w", opts.From, err)
	}

	switch provider {
	case "smtp":
		return newSMTPMailer(opts, fromAddr, log)
	case "resend":
		return &resendMailer{
			apiKey: opts.ResendAPIKey,
			from:   opts.From,
			log:    log,
			http:   &http.Client{Timeout: 10 * time.Second},
		}, nil
	}
	return nil, errors.New("mailer: unreachable provider selection")
}

// Dev returns the log-only mailer. Handy for tests and for httpapi's default
// when no Mailer is injected.
func Dev(log *slog.Logger, devAuth bool) Mailer {
	if log == nil {
		log = slog.Default()
	}
	return &logMailer{log: log, devAuth: devAuth}
}

// Redact shortens an email for logs: first character + "…@" + domain, so
// `matthew@gmail.com` logs as `m…@gmail.com`. Enough to correlate a support
// request against the logs without the logs becoming a mailing list.
func Redact(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		// No local part / no "@": keep only the first rune.
		return firstRune(email) + "…"
	}
	return firstRune(email[:at]) + "…" + email[at:]
}

// reEmail matches an address inside free text. Deliberately loose: it is a
// redactor, and over-matching costs a log line some detail while under-matching
// costs a user their privacy.
var reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// RedactText runs Redact over every address in s. Providers quote the
// recipient back at us on the failure path — `550 5.1.1 <user@example.com>:
// Recipient address rejected`, or Resend's "you can only send testing emails
// to your own email address (…)" — so a provider error is exactly as sensitive
// as the address itself and must never reach a log unscrubbed.
func RedactText(s string) string {
	return reEmail.ReplaceAllStringFunc(s, Redact)
}

// RedactErr is RedactText over an error's message, as a log value. A nil error
// is the empty string (nothing to say).
func RedactErr(err error) string {
	if err == nil {
		return ""
	}
	return RedactText(err.Error())
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// ---- dev / no-op mailer ----------------------------------------------------

type logMailer struct {
	log     *slog.Logger
	devAuth bool
}

func (m *logMailer) Live() bool { return false }
func (m *logMailer) SendMagicLink(_ context.Context, to, link string) error {
	if m.devAuth {
		// Dev convenience: the full link is the whole point of this mailer.
		m.log.Info("magic link (dev mailer — no email provider configured)", "to", Redact(to), "link", link)
		return nil
	}
	m.log.Warn("magic link NOT delivered: no email provider configured and CADENCE_DEV_AUTH=false — set SMTP_HOST or RESEND_API_KEY", "to", Redact(to))
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
		"subject": magicLinkSubject,
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
		m.log.Info("magic link email sent", "provider", "resend", "to", Redact(to), "messageId", out.ID)
		return nil
	}
	// Surface the provider's error (e.g. unverified domain) without leaking the
	// key — or the recipient: the sandbox/unverified-domain message quotes the
	// account's own address back at us.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("resend: status %d: %s", resp.StatusCode, RedactText(string(b)))
}

// ---- message content -------------------------------------------------------

const magicLinkSubject = "Your Cadence sign-in link"

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
