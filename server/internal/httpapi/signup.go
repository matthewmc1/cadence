package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cadence/server/internal/domain"
)

// Signup modes (CADENCE_SIGNUP). A private deployment defaults to invite: the
// magic-link endpoint is public, and without a policy anyone who can reach the
// host can mint themselves a workspace.
const (
	SignupOpen    = "open"    // any well-formed email may sign in (first sign-in creates a workspace)
	SignupInvite  = "invite"  // only existing accounts (`cadence-server invite <email>`) and admins
	SignupDomains = "domains" // invite, plus any address at a listed domain
)

// SignupPolicy is the parsed CADENCE_SIGNUP value.
type SignupPolicy struct {
	Mode    string
	Domains []string // lower-case, only for SignupDomains
}

// ParseSignupPolicy accepts `open`, `invite` or `domains:<csv>` ("" ⇒ invite).
func ParseSignupPolicy(s string) (SignupPolicy, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == SignupInvite:
		return SignupPolicy{Mode: SignupInvite}, nil
	case s == SignupOpen:
		return SignupPolicy{Mode: SignupOpen}, nil
	case strings.HasPrefix(s, SignupDomains+":"):
		var domains []string
		for _, d := range strings.Split(strings.TrimPrefix(s, SignupDomains+":"), ",") {
			d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
			if d == "" {
				continue
			}
			if strings.ContainsAny(d, " /@:") || !strings.Contains(d, ".") {
				return SignupPolicy{}, fmt.Errorf("CADENCE_SIGNUP: %q is not a domain", d)
			}
			domains = append(domains, d)
		}
		if len(domains) == 0 {
			return SignupPolicy{}, errors.New("CADENCE_SIGNUP: domains: needs at least one domain, e.g. domains:example.com")
		}
		return SignupPolicy{Mode: SignupDomains, Domains: domains}, nil
	}
	return SignupPolicy{}, fmt.Errorf("CADENCE_SIGNUP: %q is not open, invite or domains:<csv>", s)
}

func (p SignupPolicy) String() string {
	if p.Mode == SignupDomains {
		return SignupDomains + ":" + strings.Join(p.Domains, ",")
	}
	return orStr(p.Mode, SignupInvite)
}

// signupAllowed decides whether email may sign in under the current policy.
// Existing accounts and CADENCE_ADMIN_EMAILS always may — an invite is simply
// a pre-created account, and admins bootstrap themselves on first sign-in.
func (s *Server) signupAllowed(ctx context.Context, email string) (bool, error) {
	if s.signup.Mode == SignupOpen || s.adminEmails[email] {
		return true, nil
	}
	if s.signup.Mode == SignupDomains {
		// the same split ValidEmail used, so the policy and the shape check
		// can never disagree about which domain an address belongs to
		if dom := domain.EmailDomain(email); dom != "" {
			for _, d := range s.signup.Domains {
				if dom == d {
					return true, nil
				}
			}
		}
	}
	_, err := s.store.FindAccount(ctx, email)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, domain.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// adminSet normalises CADENCE_ADMIN_EMAILS into a lookup set; malformed
// entries are dropped rather than silently granting a typo access.
func adminSet(emails []string) map[string]bool {
	set := map[string]bool{}
	for _, e := range emails {
		e = domain.NormalizeEmail(e)
		if domain.ValidEmail(e) {
			set[e] = true
		}
	}
	return set
}
