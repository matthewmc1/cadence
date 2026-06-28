package domain

import (
	"hash/fnv"
	"strings"
	"time"
)

// Account maps an email to its (personal) tenant + user. Queryable before a
// tenant context exists, so it lives outside RLS.
type Account struct {
	Email    string
	TenantID string
	UserID   string
}

// Session is an authenticated cookie session.
type Session struct {
	UserID    string
	TenantID  string
	Email     string
	ExpiresAt time.Time
}

// APIToken is a personal access token for programmatic clients (Bearer auth).
type APIToken struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"-"`
	UserID     string     `json:"-"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

var avatarColors = []string{"#C2743D", "#7E93A6", "#94A08A", "#A85F2C", "#8FA0AE", "#B9A98F", "#6E869C"}

// DeriveUser builds a User from just an email — name, initial and avatar colour
// are computed, never stored. (We persist only the email.)
func DeriveUser(id, tenantID, email string) User {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	name := titleize(local)
	initial := "?"
	if len(local) > 0 {
		initial = strings.ToUpper(local[:1])
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(email)))
	color := avatarColors[int(h.Sum32())%len(avatarColors)]
	return User{ID: id, TenantID: tenantID, Email: email, Name: name, Initial: initial, Color: color}
}

// TenantNameForEmail is the default name of a new personal tenant.
func TenantNameForEmail(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	return titleize(local) + "'s workspace"
}

func titleize(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ", "-", " ", "+", " ").Replace(s)
	parts := strings.Fields(s)
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	if len(parts) == 0 {
		return "You"
	}
	return strings.Join(parts, " ")
}

// NormalizeEmail lowercases and trims an email for storage/lookup.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidEmail is a permissive shape check (real validation is the magic link).
func ValidEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return false
	}
	return strings.IndexByte(email[at+1:], '.') > 0 && !strings.ContainsAny(email, " \t\n")
}
