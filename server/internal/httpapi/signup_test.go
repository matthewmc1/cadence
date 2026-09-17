package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/cadence/server/internal/store/memory"
)

func TestParseSignupPolicy(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "invite",
		"invite":                        "invite",
		"open":                          "open",
		" domains:Example.com, @b.org ": "domains:example.com,b.org",
	} {
		p, err := ParseSignupPolicy(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if p.String() != want {
			t.Fatalf("%q → %q, want %q", in, p.String(), want)
		}
	}
	for _, bad := range []string{"anyone", "domains:", "domains:not a domain", "domains:localhost"} {
		if _, err := ParseSignupPolicy(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// Invite mode: unknown addresses get the same 200 and no link; pre-created
// accounts, admins and (in domains mode) listed domains get a link.
func TestSignupPolicyGatesMagicLinks(t *testing.T) {
	st := memory.New()
	if _, err := st.FindOrCreateAccount(context.Background(), "invited@example.com"); err != nil {
		t.Fatal(err)
	}

	request := func(s *Server, email string) (int, map[string]any) {
		body := strings.NewReader(`{"email":"` + email + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/request", body)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.0.0." + string(rune('1'+len(email)%9)) + ":1234" // spread the per-IP limiter
		rr := httptest.NewRecorder()
		s.handleAuthRequest(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	hasLink := func(out map[string]any) bool { _, ok := out["devLink"]; return ok }

	invite := New(st, Options{DevAuth: true, Signup: SignupPolicy{Mode: SignupInvite}, AdminEmails: []string{"Admin@Example.com"}})
	for _, tc := range []struct {
		email string
		link  bool
	}{
		{"invited@example.com", true},
		{"admin@example.com", true},
		{"stranger@example.com", false},
	} {
		code, out := request(invite, tc.email)
		if code != http.StatusOK || out["ok"] != true {
			t.Fatalf("%s: %d %v (every address must get the same 200)", tc.email, code, out)
		}
		if hasLink(out) != tc.link {
			t.Fatalf("%s: link issued = %v, want %v", tc.email, hasLink(out), tc.link)
		}
	}

	domains := New(st, Options{DevAuth: true, Signup: SignupPolicy{Mode: SignupDomains, Domains: []string{"corp.test"}}})
	if _, out := request(domains, "new@corp.test"); !hasLink(out) {
		t.Fatal("listed domain refused")
	}
	if _, out := request(domains, "new@other.test"); hasLink(out) {
		t.Fatal("unlisted domain admitted")
	}
	if _, out := request(domains, "invited@example.com"); !hasLink(out) {
		t.Fatal("existing account refused under domains policy")
	}

	open := New(st, Options{DevAuth: true, Signup: SignupPolicy{Mode: SignupOpen}})
	if _, out := request(open, "anyone@anywhere.test"); !hasLink(out) {
		t.Fatal("open policy withheld a link")
	}
}

// liveMailer is a provider that "delivers": Live() is true, so the handler
// takes the production path (no devLink, everything detached).
type liveMailer struct{ sent chan string }

func (m *liveMailer) Live() bool { return true }
func (m *liveMailer) SendMagicLink(_ context.Context, to, _ string) error {
	m.sent <- to
	return nil
}

// TestAuthRequestKeepsBothPathsTheSameShape: under invite policy the only
// difference between an admitted address and a withheld one is whether an
// account exists, so the two must be indistinguishable from outside — same
// body, and the same work done before the answer. Every step only an admitted
// address reaches (the token insert, the account lookup, the audit append, the
// send) happens after the response, or its cost is the enumeration oracle.
func TestAuthRequestKeepsBothPathsTheSameShape(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	acc, err := st.FindOrCreateAccount(ctx, "invited@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mail := &liveMailer{sent: make(chan string, 4)}
	s := New(st, Options{Signup: SignupPolicy{Mode: SignupInvite}, Mailer: mail})

	ask := func(email, ip string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/request", strings.NewReader(`{"email":"`+email+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip + ":1234"
		rr := httptest.NewRecorder()
		s.handleAuthRequest(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	admittedCode, admitted := ask("invited@example.com", "10.1.0.1")
	withheldCode, withheld := ask("stranger@example.com", "10.1.0.2")
	if admittedCode != withheldCode || admittedCode != http.StatusOK {
		t.Fatalf("codes = %d and %d", admittedCode, withheldCode)
	}
	if len(admitted) != len(withheld) || admitted["ok"] != true || withheld["ok"] != true {
		t.Fatalf("bodies differ in shape: %v vs %v", admitted, withheld)
	}
	for k := range admitted {
		if _, ok := withheld[k]; !ok {
			t.Errorf("admitted body carries %q and the withheld one does not", k)
		}
	}
	if _, leaked := admitted["devLink"]; leaked {
		t.Error("a live provider must never hand the link back in the response")
	}

	// the detached work still happens: exactly one send, for the admitted
	// address, and its audit row lands under that account's tenant
	select {
	case to := <-mail.sent:
		if to != "invited@example.com" {
			t.Fatalf("sent to %q", to)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the admitted address never got its link")
	}
	deadline := time.Now().Add(2 * time.Second)
	issued := 0
	for time.Now().Before(deadline) {
		page, err := st.ListAudit(ctx, acc.TenantID, store.Page{})
		if err != nil {
			t.Fatal(err)
		}
		issued = 0
		for _, e := range page.Items {
			if e.Kind == domain.AuditLoginIssued {
				issued++
			}
		}
		if issued > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if issued != 1 {
		t.Errorf("auth.login.issued rows = %d, want 1", issued)
	}
	select {
	case to := <-mail.sent:
		t.Fatalf("the withheld address was mailed after all: %q", to)
	default:
	}
}

// A token minted before the policy tightened must not still create a workspace.
func TestSignupPolicyRecheckedOnVerify(t *testing.T) {
	st := memory.New()
	open := New(st, Options{DevAuth: true, Signup: SignupPolicy{Mode: SignupOpen}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/request", strings.NewReader(`{"email":"late@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	open.handleAuthRequest(rr, req)
	var out struct {
		DevLink string `json:"devLink"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	tok := out.DevLink[strings.LastIndex(out.DevLink, "=")+1:]
	if tok == "" {
		t.Fatalf("no dev link in %s", rr.Body.String())
	}

	invite := New(st, Options{Signup: SignupPolicy{Mode: SignupInvite}})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/verify", strings.NewReader(`{"token":"`+tok+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	invite.handleAuthVerify(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("verify under invite policy: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := st.FindAccount(context.Background(), "late@example.com"); err == nil {
		t.Fatal("account was created despite the policy")
	}
}
