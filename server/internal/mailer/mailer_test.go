package mailer

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ---- selection --------------------------------------------------------------

func TestNewSelectsProvider(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string // concrete type name
	}{
		{"nothing configured → dev", Options{}, "*mailer.logMailer"},
		{"resend key → resend", Options{ResendAPIKey: "re_x", From: "Cadence <c@example.com>"}, "*mailer.resendMailer"},
		{"smtp host → smtp", Options{SMTPHost: "mail.example.com", From: "Cadence <c@example.com>"}, "*mailer.smtpMailer"},
		{"smtp beats resend", Options{SMTPHost: "mail.example.com", ResendAPIKey: "re_x", From: "c@example.com"}, "*mailer.smtpMailer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Log = quiet
			m, err := New(tc.opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := typeName(m); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			if wantLive := tc.want != "*mailer.logMailer"; m.Live() != wantLive {
				t.Fatalf("Live() = %v, want %v", m.Live(), wantLive)
			}
			if tc.opts.Provider() != strings.TrimSuffix(strings.TrimPrefix(tc.want, "*mailer."), "Mailer") && !(tc.want == "*mailer.logMailer" && tc.opts.Provider() == "dev") {
				t.Fatalf("Provider() = %q disagrees with %s", tc.opts.Provider(), tc.want)
			}
		})
	}
}

func TestNewLiveRequiresFrom(t *testing.T) {
	for _, opts := range []Options{
		{SMTPHost: "mail.example.com"},
		{ResendAPIKey: "re_x"},
		{SMTPHost: "mail.example.com", From: "   "},
	} {
		opts.Log = quiet
		if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "MAIL_FROM") {
			t.Fatalf("%+v: expected a MAIL_FROM error, got %v", opts, err)
		}
	}
	// Malformed sender is caught at boot, not at first sign-in.
	if _, err := New(Options{SMTPHost: "h", From: "not an address", Log: quiet}); err == nil {
		t.Fatal("expected an error for a malformed MAIL_FROM")
	}
	// The dev mailer never needs a sender.
	if _, err := New(Options{Log: quiet}); err != nil {
		t.Fatalf("dev mailer should not require MAIL_FROM: %v", err)
	}
}

func TestNewSMTPValidation(t *testing.T) {
	if _, err := New(Options{SMTPHost: "h", From: "a@b.c", SMTPUser: "u", Log: quiet}); err == nil {
		t.Fatal("expected error when SMTP_USER is set without SMTP_PASS")
	}
	if _, err := New(Options{SMTPHost: "h", From: "a@b.c", SMTPPort: 70000, Log: quiet}); err == nil {
		t.Fatal("expected error for an out-of-range port")
	}
	m, err := New(Options{SMTPHost: "h", From: "a@b.c", Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	sm := m.(*smtpMailer)
	if sm.port != 587 || sm.implicitTLS {
		t.Fatalf("default port should be 587 with STARTTLS semantics, got port=%d implicit=%v", sm.port, sm.implicitTLS)
	}
	m, _ = New(Options{SMTPHost: "h", From: "a@b.c", SMTPPort: 465, Log: quiet})
	if !m.(*smtpMailer).implicitTLS {
		t.Fatal("port 465 should imply implicit TLS")
	}
}

// ---- redaction --------------------------------------------------------------

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"matthew@gmail.com":  "m…@gmail.com",
		"a@b.co":             "a…@b.co",
		"  Élodie@ex.fr ":    "É…@ex.fr",
		"":                   "",
		"nobody":             "n…",
		"@weird.example":     "@…", // no local part: nothing to keep before the "…"
		"first.last@x.y.org": "f…@x.y.org",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRedactTextScrubsProviderReplies: the failure path is where the address
// leaks — servers quote the recipient back at us in the rejection, and that
// text is logged next to a carefully redacted "email" field.
func TestRedactTextScrubsProviderReplies(t *testing.T) {
	cases := map[string]string{
		"550 5.1.1 <user@example.com>: Recipient address rejected":                                                           "550 5.1.1 <u…@example.com>: Recipient address rejected",
		`resend: status 403: {"message":"You can only send testing emails to your own email address (matthew@example.com)"}`: `resend: status 403: {"message":"You can only send testing emails to your own email address (m…@example.com)"}`,
		"connect smtp.example.com:587: connection refused":                                                                   "connect smtp.example.com:587: connection refused",
		"two: a@b.co and first.last@x.y.org":                                                                                 "two: a…@b.co and f…@x.y.org",
	}
	for in, want := range cases {
		if got := RedactText(in); got != want {
			t.Errorf("RedactText(%q)\n  = %q\nwant %q", in, got, want)
		}
	}
	if got := RedactErr(nil); got != "" {
		t.Errorf("RedactErr(nil) = %q", got)
	}
	if got := RedactErr(errors.New("RCPT TO: 550 <a@b.co> rejected")); strings.Contains(got, "a@b.co") {
		t.Errorf("RedactErr kept the address: %q", got)
	}
}

// ---- message formatting -----------------------------------------------------

func TestBuildMessage(t *testing.T) {
	from := &mail.Address{Name: "Cadence", Address: "cadence@example.com"}
	to := &mail.Address{Address: "someone@example.org"}
	link := "https://cadence.example.com/auth?token=" + strings.Repeat("Ab3_", 60) // deliberately > 76 chars
	when := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	raw, err := buildMessage(from, to, magicLinkSubject, magicLinkText(link), magicLinkHTML(link), when)
	if err != nil {
		t.Fatal(err)
	}

	// Strict wire format: CRLF line endings, nothing over the RFC 5321 limit.
	if strings.Contains(strings.ReplaceAll(string(raw), "\r\n", ""), "\n") {
		t.Fatal("message contains a bare LF")
	}
	for i, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line %d is %d bytes (> 998)", i, len(line))
		}
	}

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for k, want := range map[string]string{
		"From":         `"Cadence" <cadence@example.com>`,
		"To":           "<someone@example.org>",
		"Subject":      magicLinkSubject,
		"Date":         "Sun, 06 Sep 2026 12:00:00 +0000",
		"MIME-Version": "1.0",
	} {
		if got := msg.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if id := msg.Header.Get("Message-ID"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
		t.Errorf("Message-ID %q should be <random@sender-domain>", id)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q (%v)", msg.Header.Get("Content-Type"), err)
	}
	// multipart.Reader decodes quoted-printable parts transparently (and drops
	// the header), so check the wire form here and read the decoded body below.
	if n := strings.Count(string(raw), "Content-Transfer-Encoding: quoted-printable\r\n"); n != 2 {
		t.Errorf("expected 2 quoted-printable parts, found %d", n)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), link) {
			t.Errorf("part %s does not contain the link intact", p.Header.Get("Content-Type"))
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Fatalf("parts = %v, want text/plain then text/html", types)
	}
}

// ---- SMTP transport against an in-process server ---------------------------

func TestSMTPStartTLSWithAuth(t *testing.T) {
	srv := startFakeSMTP(t, fakeSMTPConfig{tls: testCert(t), authMechs: "PLAIN LOGIN"})
	m := newTestSMTP(t, srv, Options{SMTPUser: "cadence", SMTPPass: "s3cret", SMTPStartTLS: true})

	if err := m.SendMagicLink(context.Background(), "Some One <someone@example.org>", "https://x/auth?token=abc"); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := srv.wait(t)
	if !got.tlsUsed {
		t.Error("session was not upgraded with STARTTLS")
	}
	if got.auth != "PLAIN "+base64.StdEncoding.EncodeToString([]byte("\x00cadence\x00s3cret")) {
		t.Errorf("AUTH = %q", got.auth)
	}
	if got.mailFrom != "<cadence@example.com>" {
		t.Errorf("MAIL FROM = %q", got.mailFrom)
	}
	if got.rcptTo != "<someone@example.org>" {
		t.Errorf("RCPT TO = %q (display name must be stripped for the envelope)", got.rcptTo)
	}
	if !strings.Contains(got.data, "token=3Dabc") { // "=" is QP-escaped
		t.Errorf("DATA does not contain the link:\n%s", got.data)
	}
	if !strings.Contains(got.data, "To: \"Some One\" <someone@example.org>\r\n") {
		t.Errorf("To header missing/unexpected:\n%s", got.data)
	}
	if !got.quit {
		t.Error("client did not QUIT")
	}
}

func TestSMTPLoginAuthFallback(t *testing.T) {
	srv := startFakeSMTP(t, fakeSMTPConfig{tls: testCert(t), authMechs: "LOGIN"})
	m := newTestSMTP(t, srv, Options{SMTPUser: "cadence", SMTPPass: "s3cret", SMTPStartTLS: true})
	if err := m.SendMagicLink(context.Background(), "someone@example.org", "https://x/auth?token=abc"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := srv.wait(t); got.auth != "LOGIN cadence s3cret" {
		t.Errorf("AUTH = %q", got.auth)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	srv := startFakeSMTP(t, fakeSMTPConfig{tls: testCert(t), implicit: true})
	m := newTestSMTP(t, srv, Options{SMTPStartTLS: true})
	m.implicitTLS = true // stands in for port 465, which a test can't bind
	if err := m.SendMagicLink(context.Background(), "someone@example.org", "https://x/auth?token=abc"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := srv.wait(t); !got.tlsUsed || got.startTLSCmd {
		t.Errorf("expected TLS from the first byte and no STARTTLS command; got tls=%v startTLS=%v", got.tlsUsed, got.startTLSCmd)
	}
}

func TestSMTPRefusesMissingStartTLS(t *testing.T) {
	// Server offers no STARTTLS: with the default (STARTTLS on) we must fail
	// closed rather than fall back to plaintext.
	srv := startFakeSMTP(t, fakeSMTPConfig{})
	m := newTestSMTP(t, srv, Options{SMTPStartTLS: true})
	err := m.SendMagicLink(context.Background(), "someone@example.org", "https://x/auth?token=abc")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected a STARTTLS error, got %v", err)
	}
	if got := srv.wait(t); got.mailFrom != "" {
		t.Errorf("MAIL FROM was sent over plaintext: %q", got.mailFrom)
	}
}

func TestSMTPPlaintextLocalRelay(t *testing.T) {
	// Explicit opt-out for a relay on localhost.
	srv := startFakeSMTP(t, fakeSMTPConfig{})
	m := newTestSMTP(t, srv, Options{SMTPStartTLS: false})
	if err := m.SendMagicLink(context.Background(), "someone@example.org", "https://x/auth?token=abc"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := srv.wait(t); got.tlsUsed || got.rcptTo != "<someone@example.org>" {
		t.Errorf("unexpected session: %+v", got)
	}
}

func TestSMTPRejectsHeaderInjection(t *testing.T) {
	// Nothing should even be dialled: point at a closed port.
	m, err := New(Options{SMTPHost: "127.0.0.1", SMTPPort: 1, From: "c@example.com", Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	err = m.SendMagicLink(context.Background(), "someone@example.org\r\nBcc: victim@example.net", "https://x")
	if err == nil || !strings.Contains(err.Error(), "invalid recipient") {
		t.Fatalf("expected recipient validation error, got %v", err)
	}
}

// ---- helpers ----------------------------------------------------------------

func typeName(v any) string {
	switch v.(type) {
	case *logMailer:
		return "*mailer.logMailer"
	case *resendMailer:
		return "*mailer.resendMailer"
	case *smtpMailer:
		return "*mailer.smtpMailer"
	}
	return "?"
}

// newTestSMTP builds an smtpMailer aimed at the fake server, trusting its
// self-signed certificate.
func newTestSMTP(t *testing.T, srv *fakeSMTP, opts Options) *smtpMailer {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(srv.ln.Addr().String())
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	opts.SMTPHost = host
	opts.SMTPPort = port
	opts.From = "Cadence <cadence@example.com>"
	opts.Log = quiet
	m, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	sm := m.(*smtpMailer)
	if srv.cfg.tls != nil {
		pool := x509.NewCertPool()
		pool.AddCert(srv.cfg.tls.Certificates[0].Leaf)
		sm.tlsConfig = &tls.Config{ServerName: host, RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return sm
}

// testCert mints a self-signed certificate for 127.0.0.1 / localhost.
func testCert(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cadence-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	cert.Leaf, _ = x509.ParseCertificate(der)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

type fakeSMTPConfig struct {
	tls       *tls.Config // non-nil ⇒ STARTTLS advertised (or implicit TLS when implicit is set)
	implicit  bool
	authMechs string // advertised AUTH mechanisms; "" ⇒ none
}

type smtpSession struct {
	tlsUsed, startTLSCmd, quit bool
	auth, mailFrom, rcptTo     string
	data                       string
}

// fakeSMTP is the smallest server that satisfies net/smtp for one session.
type fakeSMTP struct {
	ln   net.Listener
	cfg  fakeSMTPConfig
	done chan smtpSession
}

func startFakeSMTP(t *testing.T, cfg fakeSMTPConfig) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &fakeSMTP{ln: ln, cfg: cfg, done: make(chan smtpSession, 1)}
	go s.serveOne()
	return s
}

func (s *fakeSMTP) wait(t *testing.T) smtpSession {
	t.Helper()
	select {
	case sess := <-s.done:
		return sess
	case <-time.After(5 * time.Second):
		t.Fatal("fake SMTP server saw no session")
		return smtpSession{}
	}
}

func (s *fakeSMTP) serveOne() {
	var sess smtpSession
	defer func() { s.done <- sess }()

	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if s.cfg.implicit {
		conn = tls.Server(conn, s.cfg.tls)
		sess.tlsUsed = true
	}

	var mu sync.Mutex
	r := bufio.NewReader(conn)
	reply := func(lines ...string) {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			io.WriteString(conn, l+"\r\n")
		}
	}
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			ext := []string{"250-fake"}
			if s.cfg.tls != nil && !sess.tlsUsed {
				ext = append(ext, "250-STARTTLS")
			}
			if s.cfg.authMechs != "" {
				ext = append(ext, "250-AUTH "+s.cfg.authMechs)
			}
			ext = append(ext, "250 OK") // no 8BITMIME: keeps MAIL FROM free of BODY= params
			reply(ext...)
		case cmd == "STARTTLS":
			sess.startTLSCmd = true
			reply("220 go ahead")
			conn = tls.Server(conn, s.cfg.tls)
			r = bufio.NewReader(conn)
			sess.tlsUsed = true
		case strings.HasPrefix(cmd, "AUTH PLAIN "):
			sess.auth = line[len("AUTH "):]
			reply("235 ok")
		case cmd == "AUTH LOGIN":
			reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
			u, _ := r.ReadString('\n')
			reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			p, _ := r.ReadString('\n')
			ub, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(u))
			pb, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
			sess.auth = "LOGIN " + string(ub) + " " + string(pb)
			reply("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			sess.mailFrom = strings.TrimSpace(line[len("MAIL FROM:"):])
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			sess.rcptTo = strings.TrimSpace(line[len("RCPT TO:"):])
			reply("250 ok")
		case cmd == "DATA":
			reply("354 end with <CRLF>.<CRLF>")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			sess.data = b.String()
			reply("250 queued")
		case cmd == "QUIT":
			sess.quit = true
			reply("221 bye")
			return
		default:
			reply("500 unknown: " + line)
		}
	}
}
