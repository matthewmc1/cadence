package memory

import (
	"context"
	"time"

	"github.com/cadence/server/internal/domain"
)

func (s *Store) CreateLoginToken(_ context.Context, tokenHash, email string, expiresAt time.Time) error {
	s.mu.Lock()
	s.loginTokens[tokenHash] = loginToken{email: email, expiresAt: expiresAt}
	s.mu.Unlock()
	return nil
}

func (s *Store) ConsumeLoginToken(_ context.Context, tokenHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.loginTokens[tokenHash]
	if !ok || t.consumed || time.Now().After(t.expiresAt) {
		return "", domain.ErrNotFound
	}
	t.consumed = true
	s.loginTokens[tokenHash] = t
	return t.email, nil
}

func (s *Store) FindOrCreateAccount(_ context.Context, email string) (domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if acc, ok := s.accounts[email]; ok {
		return acc, nil
	}
	now := time.Now().UTC()
	tenantID := domain.NewID()
	userID := domain.NewID()
	s.tenants[tenantID] = domain.Tenant{ID: tenantID, Name: domain.TenantNameForEmail(email), CreatedAt: now}
	s.users[userID] = domain.DeriveUser(userID, tenantID, email)
	acc := domain.Account{Email: email, TenantID: tenantID, UserID: userID}
	s.accounts[email] = acc
	return acc, nil
}

func (s *Store) GetUser(_ context.Context, tenantID, userID string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[userID]
	if !ok || u.TenantID != tenantID {
		return domain.User{}, domain.ErrNotFound
	}
	return domain.DeriveUser(u.ID, u.TenantID, u.Email), nil
}

func (s *Store) CreateSession(_ context.Context, tokenHash string, sess domain.Session) error {
	s.mu.Lock()
	s.sessions[tokenHash] = sess
	s.mu.Unlock()
	return nil
}

func (s *Store) GetSession(_ context.Context, tokenHash string) (domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[tokenHash]
	if !ok || time.Now().After(sess.ExpiresAt) {
		return domain.Session{}, domain.ErrNotFound
	}
	return sess, nil
}

func (s *Store) DeleteSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	delete(s.sessions, tokenHash)
	s.mu.Unlock()
	return nil
}
