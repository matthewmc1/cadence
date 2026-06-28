package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fanout is a per-tenant in-process subscriber registry. On a single instance
// it carries events; across instances each instance runs its own listener and
// its own fanout, all fed by the same Postgres NOTIFY channel.
type fanout struct {
	mu   sync.Mutex
	next int
	subs map[string]map[int]chan domain.Event
}

func newFanout() *fanout { return &fanout{subs: make(map[string]map[int]chan domain.Event)} }

func (f *fanout) subscribe(tenantID string) (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, 64)
	f.mu.Lock()
	id := f.next
	f.next++
	if f.subs[tenantID] == nil {
		f.subs[tenantID] = make(map[int]chan domain.Event)
	}
	f.subs[tenantID][id] = ch
	f.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			f.mu.Lock()
			present := false
			if m := f.subs[tenantID]; m != nil {
				if _, ok := m[id]; ok {
					present = true
					delete(m, id)
					if len(m) == 0 {
						delete(f.subs, tenantID)
					}
				}
			}
			f.mu.Unlock()
			// Only close if we still owned the channel — close() may already have
			// closed it during shutdown, and double-closing panics.
			if present {
				close(ch)
			}
		})
	}
}

func (f *fanout) publish(ev domain.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs[ev.TenantID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (f *fanout) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.subs {
		for _, ch := range m {
			close(ch)
		}
	}
	f.subs = make(map[string]map[int]chan domain.Event)
}

// listen runs a dedicated connection that LISTENs on cadence_events and feeds
// the fanout. It reconnects with backoff until ctx is cancelled. Because every
// instance LISTENs, a write on any instance reaches subscribers on all of them.
func listen(ctx context.Context, pool *pgxpool.Pool, f *fanout, log *slog.Logger) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := listenOnce(ctx, pool, f); err != nil && ctx.Err() == nil {
			log.Warn("listener dropped, reconnecting", "err", err, "in", backoff.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func listenOnce(ctx context.Context, pool *pgxpool.Pool, f *fanout) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN cadence_events"); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		// n.Payload is the outbox row id; fetch the full event payload by id so
		// delivery is independent of the 8000-byte NOTIFY limit.
		var raw []byte
		if err := conn.QueryRow(ctx, `SELECT payload FROM outbox WHERE id = $1`, n.Payload).Scan(&raw); err != nil {
			continue // row gone or unreadable; skip
		}
		var ev domain.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue // skip malformed payloads
		}
		f.publish(ev)
	}
}
