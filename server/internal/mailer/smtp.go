package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

// smtpMailer delivers through any SMTP server using only the standard library.
//
// Transport security: port 465 means implicit TLS (the socket is TLS from the
// first byte); every other port starts in plaintext and is upgraded with
// STARTTLS when startTLS is set (the default). With startTLS on, a server that
// doesn't advertise the extension is an error rather than a silent downgrade —
// SMTP_STARTTLS=false is an explicit opt-out for a trusted relay on localhost
// or a private network.
type smtpMailer struct {
	host        string
	port        int
	user, pass  string
	startTLS    bool
	implicitTLS bool

	from *mail.Address // parsed MAIL_FROM: .Address for the envelope, .String() for the header
	log  *slog.Logger

	// tlsConfig overrides the default (ServerName = host, system roots). Tests
	// point it at a self-signed root; production leaves it nil.
	tlsConfig   *tls.Config
	dialTimeout time.Duration
	helloName   string
}

func newSMTPMailer(opts Options, from *mail.Address, log *slog.Logger) (*smtpMailer, error) {
	port := opts.SMTPPort
	if port == 0 {
		port = 587
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("mailer: SMTP_PORT %d is out of range", port)
	}
	if (opts.SMTPUser == "") != (opts.SMTPPass == "") {
		return nil, errors.New("mailer: SMTP_USER and SMTP_PASS must be set together")
	}
	hello, _ := os.Hostname()
	if hello == "" {
		hello = "localhost"
	}
	return &smtpMailer{
		host:        opts.SMTPHost,
		port:        port,
		user:        opts.SMTPUser,
		pass:        opts.SMTPPass,
		startTLS:    opts.SMTPStartTLS,
		implicitTLS: port == 465,
		from:        from,
		log:         log,
		dialTimeout: 10 * time.Second,
		helloName:   hello,
	}, nil
}

func (m *smtpMailer) Live() bool { return true }

func (m *smtpMailer) SendMagicLink(ctx context.Context, to, link string) error {
	// Parse rather than trust: `to` ends up in both the envelope and a header,
	// and a stray CR/LF would otherwise let a caller smuggle extra headers.
	rcpt, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("smtp: invalid recipient: %w", err)
	}
	msg, err := buildMessage(m.from, rcpt, magicLinkSubject, magicLinkText(link), magicLinkHTML(link), time.Now())
	if err != nil {
		return err
	}
	if err := m.deliver(ctx, rcpt.Address, msg); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	m.log.Info("magic link email sent", "provider", "smtp", "to", Redact(rcpt.Address), "host", m.host)
	return nil
}

// deliver runs one SMTP session: connect (TLS or STARTTLS), authenticate if
// credentials are set, hand over the message, QUIT.
func (m *smtpMailer) deliver(ctx context.Context, rcpt string, msg []byte) error {
	addr := net.JoinHostPort(m.host, strconv.Itoa(m.port))
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second {
		deadline = time.Now().Add(30 * time.Second)
	}

	dialer := &net.Dialer{Timeout: m.dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	// net/smtp isn't context-aware; a socket deadline bounds every step below.
	_ = conn.SetDeadline(deadline)
	if m.implicitTLS {
		conn = tls.Client(conn, m.tlsClientConfig())
	}

	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("greeting from %s: %w", addr, err)
	}
	defer c.Close()

	if err := c.Hello(m.helloName); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}
	if !m.implicitTLS && m.startTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS (set SMTP_STARTTLS=false only for a trusted local relay)")
		}
		if err := c.StartTLS(m.tlsClientConfig()); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if m.user != "" {
		if err := c.Auth(m.auth(c)); err != nil {
			return fmt.Errorf("AUTH: %w", err)
		}
	}
	if err := c.Mail(m.from.Address); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(rcpt); err != nil {
		// Servers echo the address in a rejection ("550 5.1.1 <a@b.com>:
		// Recipient address rejected"), so this reply is scrubbed at the source
		// rather than trusted to whoever logs it.
		return fmt.Errorf("RCPT TO: %s", RedactText(err.Error()))
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	return c.Quit()
}

func (m *smtpMailer) tlsClientConfig() *tls.Config {
	if m.tlsConfig != nil {
		return m.tlsConfig
	}
	return &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}
}

// auth picks a mechanism the server advertises. PLAIN is universal; LOGIN is
// the legacy one some hosted relays still insist on; CRAM-MD5 is a last
// resort. All three refuse to run over an unencrypted, non-local connection.
func (m *smtpMailer) auth(c *smtp.Client) smtp.Auth {
	_, mechs := c.Extension("AUTH")
	switch {
	case strings.Contains(mechs, "PLAIN"):
		return smtp.PlainAuth("", m.user, m.pass, m.host)
	case strings.Contains(mechs, "LOGIN"):
		return &loginAuth{user: m.user, pass: m.pass}
	case strings.Contains(mechs, "CRAM-MD5"):
		return smtp.CRAMMD5Auth(m.user, m.pass)
	default:
		// Try PLAIN anyway; the server's rejection is a clearer error than
		// guessing wrong here.
		return smtp.PlainAuth("", m.user, m.pass, m.host)
	}
}

// loginAuth implements the (non-standard but common) AUTH LOGIN exchange:
// the server challenges for the username, then the password.
type loginAuth struct {
	user, pass string
	step       int
}

func (a *loginAuth) Start(s *smtp.ServerInfo) (string, []byte, error) {
	if !s.TLS && !isLocalhost(s.Name) {
		return "", nil, errors.New("refusing LOGIN auth over an unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.user), nil
	case 2:
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected LOGIN challenge")
}

func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

// buildMessage renders an RFC 5322 multipart/alternative message (text +
// HTML), quoted-printable encoded so no line exceeds the SMTP limit regardless
// of how long the link or the inline-styled HTML gets.
func buildMessage(from, to *mail.Address, subject, text, htmlBody string, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	mp := multipart.NewWriter(&buf)

	hdr := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", to.String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", subject))
	hdr("Date", now.Format(time.RFC1123Z))
	hdr("Message-ID", messageID(from.Address))
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", `multipart/alternative; boundary="`+mp.Boundary()+`"`)
	buf.WriteString("\r\n")

	// Plain text first: clients pick the last part they can render, so HTML
	// wins where supported and the text part is the fallback.
	for _, part := range []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", text},
		{"text/html; charset=utf-8", htmlBody},
	} {
		w, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mp.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// messageID mints a unique <random@domain> id; the domain is the sender's so
// receivers don't flag the message for a mismatched origin.
func messageID(fromAddr string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	domain := "localhost"
	if at := strings.LastIndex(fromAddr, "@"); at >= 0 && at < len(fromAddr)-1 {
		domain = fromAddr[at+1:]
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">"
}
