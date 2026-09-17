package memory

import (
	"context"
	"fmt"

	"github.com/cadence/server/internal/domain"
)

// PurgeTenant drops every record belonging to the tenant from every map,
// mirroring the postgres adapter's table-by-table delete. Any new
// tenant-scoped map MUST be added here, to CountRows, and to
// storetest.PurgeTables — the conformance suite is what proves the list is
// complete.
func (s *Store) PurgeTenant(_ context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[tenantID]; !ok {
		return domain.ErrNotFound
	}
	for id, o := range s.outputs {
		if o.TenantID == tenantID {
			delete(s.outputs, id)
		}
	}
	for id, sig := range s.signals {
		if sig.TenantID == tenantID {
			s.dropSignal(id) // and its body
		}
	}
	for id, src := range s.sources {
		if src.TenantID == tenantID {
			delete(s.sources, id)
		}
	}
	for id, t := range s.tasks {
		if t.TenantID == tenantID {
			delete(s.tasks, id)
			delete(s.origins, id) // origins hang off the task
		}
	}
	for id, r := range s.requirements {
		if r.TenantID == tenantID {
			delete(s.requirements, id)
		}
	}
	delete(s.audit, tenantID) // the one path that removes audit rows
	for id, p := range s.projects {
		if p.TenantID == tenantID {
			delete(s.projects, id)
		}
	}
	for id, c := range s.clients {
		if c.TenantID == tenantID {
			delete(s.clients, id)
		}
	}
	for id, u := range s.users {
		if u.TenantID == tenantID {
			delete(s.users, id)
		}
	}
	for h, t := range s.apiTokens {
		if t.TenantID == tenantID {
			delete(s.apiTokens, h)
		}
	}
	for h, sess := range s.sessions {
		if sess.TenantID == tenantID {
			delete(s.sessions, h)
		}
	}
	// login tokens have no tenant column; they belong to the tenant's emails
	emails := s.tenantEmails(tenantID)
	for h, lt := range s.loginTokens {
		if emails[lt.email] {
			delete(s.loginTokens, h)
		}
	}
	for email, acc := range s.accounts {
		if acc.TenantID == tenantID {
			delete(s.accounts, email)
		}
	}
	delete(s.tenants, tenantID)
	return nil
}

// tenantEmails is the set of account emails for a tenant. Caller holds s.mu.
func (s *Store) tenantEmails(tenantID string) map[string]bool {
	out := map[string]bool{}
	for email, acc := range s.accounts {
		if acc.TenantID == tenantID {
			out[email] = true
		}
	}
	return out
}

// CountRows reports how many records in `table` belong to the tenant (or, for
// login_tokens, to email). Table names match the Postgres schema so the
// cross-adapter purge suite can enumerate them once. It exists for that suite
// and for operators; it is not part of store.Store.
func (s *Store) CountRows(table, tenantID, email string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	switch table {
	case "tenants":
		if _, ok := s.tenants[tenantID]; ok {
			n = 1
		}
	case "users":
		for _, u := range s.users {
			if u.TenantID == tenantID {
				n++
			}
		}
	case "clients":
		for _, c := range s.clients {
			if c.TenantID == tenantID {
				n++
			}
		}
	case "projects":
		for _, p := range s.projects {
			if p.TenantID == tenantID {
				n++
			}
		}
	case "project_members":
		for _, p := range s.projects {
			if p.TenantID == tenantID {
				n += len(p.Members)
			}
		}
	case "tasks":
		for _, t := range s.tasks {
			if t.TenantID == tenantID {
				n++
			}
		}
	case "requirements":
		for _, r := range s.requirements {
			if r.TenantID == tenantID {
				n++
			}
		}
	case "audit_log":
		n = len(s.audit[tenantID])
	case "sources":
		for _, src := range s.sources {
			if src.TenantID == tenantID {
				n++
			}
		}
	case "signals":
		for _, sig := range s.signals {
			if sig.TenantID == tenantID {
				n++
			}
		}
	case "signal_bodies":
		for _, b := range s.bodies {
			if b.TenantID == tenantID {
				n++
			}
		}
	case "signal_participants":
		for _, sig := range s.signals {
			if sig.TenantID == tenantID {
				n += len(sig.Participants)
			}
		}
	case "work_item_signals":
		for _, byTask := range s.origins {
			for _, o := range byTask {
				if o.TenantID == tenantID {
					n++
				}
			}
		}
	case "outputs":
		for _, o := range s.outputs {
			if o.TenantID == tenantID {
				n++
			}
		}
	case "outbox":
		// no durable event log in memory; nothing to retain or purge
	case "accounts":
		for _, a := range s.accounts {
			if a.TenantID == tenantID {
				n++
			}
		}
	case "login_tokens":
		for _, lt := range s.loginTokens {
			if lt.email == email {
				n++
			}
		}
	case "sessions":
		for _, sess := range s.sessions {
			if sess.TenantID == tenantID {
				n++
			}
		}
	case "api_tokens":
		for _, t := range s.apiTokens {
			if t.TenantID == tenantID {
				n++
			}
		}
	default:
		return 0, fmt.Errorf("memory: CountRows: unknown table %q", table)
	}
	return n, nil
}
