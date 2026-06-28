package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateLoginToken(ctx context.Context, tokenHash, email string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO login_tokens (token_hash, email, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, email, expiresAt)
	return err
}

func (s *Store) ConsumeLoginToken(ctx context.Context, tokenHash string) (string, error) {
	var email string
	err := s.pool.QueryRow(ctx, `
		UPDATE login_tokens SET consumed_at = now()
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING email`, tokenHash).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return email, err
}

func (s *Store) FindOrCreateAccount(ctx context.Context, email string) (domain.Account, error) {
	var acc domain.Account
	err := s.pool.QueryRow(ctx,
		`SELECT email, tenant_id::text, user_id::text FROM accounts WHERE email = $1`, email).
		Scan(&acc.Email, &acc.TenantID, &acc.UserID)
	if err == nil {
		return acc, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, err
	}

	// first sign-in: create a personal tenant + user + account
	tenantID := domain.NewID()
	userID := domain.NewID()
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`,
			tenantID, domain.TenantNameForEmail(email)); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO users (tenant_id, id, email) VALUES ($1, $2, $3)`,
			tenantID, userID, email); e != nil {
			return e
		}
		// accounts is non-RLS; safe to write in the same tx
		_, e := tx.Exec(ctx, `INSERT INTO accounts (email, tenant_id, user_id) VALUES ($1, $2, $3)`,
			email, tenantID, userID)
		return e
	})
	if err != nil {
		return domain.Account{}, err
	}
	return domain.Account{Email: email, TenantID: tenantID, UserID: userID}, nil
}

func (s *Store) GetUser(ctx context.Context, tenantID, userID string) (domain.User, error) {
	var email string
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return e
	})
	if err != nil {
		return domain.User{}, err
	}
	return domain.DeriveUser(userID, tenantID, email), nil
}

func (s *Store) CreateSession(ctx context.Context, tokenHash string, sess domain.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, tenant_id, email, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		tokenHash, sess.UserID, sess.TenantID, sess.Email, sess.ExpiresAt)
	return err
}

func (s *Store) GetSession(ctx context.Context, tokenHash string) (domain.Session, error) {
	var sess domain.Session
	err := s.pool.QueryRow(ctx, `
		SELECT user_id::text, tenant_id::text, email, expires_at
		FROM sessions WHERE token_hash = $1 AND expires_at > now()`, tokenHash).
		Scan(&sess.UserID, &sess.TenantID, &sess.Email, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	return sess, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) CreateAPIToken(ctx context.Context, tokenHash string, tok domain.APIToken) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO api_tokens (token_hash, id, tenant_id, user_id, name, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		tokenHash, tok.ID, tok.TenantID, tok.UserID, tok.Name, tok.CreatedAt)
	return err
}

func (s *Store) ResolveAPIToken(ctx context.Context, tokenHash string) (domain.APIToken, error) {
	var t domain.APIToken
	err := s.pool.QueryRow(ctx, `
		UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1
		RETURNING id::text, tenant_id::text, user_id::text, name, created_at, last_used_at`, tokenHash).
		Scan(&t.ID, &t.TenantID, &t.UserID, &t.Name, &t.CreatedAt, &t.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.APIToken{}, domain.ErrNotFound
	}
	return t, err
}

func (s *Store) ListAPITokens(ctx context.Context, tenantID, userID string) ([]domain.APIToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, created_at, last_used_at FROM api_tokens
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY created_at`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.APIToken{}
	for rows.Next() {
		var t domain.APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIToken(ctx context.Context, tenantID, userID, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1 AND tenant_id = $2 AND user_id = $3`, id, tenantID, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
