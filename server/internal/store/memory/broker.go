package memory

import (
	"sync"

	"github.com/cadence/server/internal/domain"
)

// broker is an in-process, per-tenant fan-out. Each subscriber gets a buffered
// channel; publish is non-blocking so one slow consumer can never stall a
// write. (The Postgres adapter swaps this for LISTEN/NOTIFY across instances.)
type broker struct {
	mu   sync.Mutex
	next int
	subs map[string]map[int]chan domain.Event
}

func newBroker() *broker {
	return &broker{subs: make(map[string]map[int]chan domain.Event)}
}

func (b *broker) subscribe(tenantID string) (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, 64)
	b.mu.Lock()
	id := b.next
	b.next++
	if b.subs[tenantID] == nil {
		b.subs[tenantID] = make(map[int]chan domain.Event)
	}
	b.subs[tenantID][id] = ch
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			present := false
			if m := b.subs[tenantID]; m != nil {
				if _, ok := m[id]; ok {
					present = true
					delete(m, id)
					if len(m) == 0 {
						delete(b.subs, tenantID)
					}
				}
			}
			b.mu.Unlock()
			// Only close if we still owned the channel — close() may already have
			// closed it during shutdown, and double-closing panics.
			if present {
				close(ch)
			}
		})
	}
	return ch, cancel
}

func (b *broker) publish(ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs[ev.TenantID] {
		select {
		case ch <- ev:
		default:
			// Consumer is behind; drop. A real deployment recovers missed
			// events via the outbox cursor on reconnect.
		}
	}
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.subs {
		for _, ch := range m {
			close(ch)
		}
	}
	b.subs = make(map[string]map[int]chan domain.Event)
}
