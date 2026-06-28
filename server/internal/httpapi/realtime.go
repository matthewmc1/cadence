package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// handleRealtime upgrades to a WebSocket and streams the tenant's change events
// (server→client push). The subscription is fed by the store's broker —
// in-memory in dev, Postgres LISTEN/NOTIFY in production — so the same socket
// works whether one instance or many are running.
//
// Identity comes from the session cookie (the route is behind auth()), so a
// client can only ever subscribe to its own tenant.
func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
	tenant := TenantID(r.Context())

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Reject cross-site handshakes: only the configured web origins may open
		// a socket (CADENCE_WEB_ORIGINS; defaults to localhost dev ports). This
		// prevents a malicious page from opening a socket and reading events.
		OriginPatterns: s.wsOrigins,
	})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer c.CloseNow()

	// CloseRead drains/ignores client frames and cancels ctx when the socket
	// closes — this connection is push-only.
	ctx := c.CloseRead(r.Context())

	sub, unsubscribe := s.store.Subscribe(tenant)
	defer unsubscribe()

	_ = writeWS(ctx, c, map[string]any{"type": "hello", "tenantId": tenant, "at": time.Now().UTC()})

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if err := writeWS(ctx, c, ev); err != nil {
				return
			}
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func writeWS(ctx context.Context, c *websocket.Conn, v any) error {
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(wctx, c, v)
}
