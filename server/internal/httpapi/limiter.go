package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// authLimiter blunts abuse of the public, unauthenticated magic-link endpoint —
// which now sends real email, so it is an email-bombing and provider
// cost-amplification target. Two independent gates:
//
//   - per-IP request cap: one source can't rotate through many victim addresses
//   - per-email cooldown: a single address can't be flooded with links
//
// It is in-process (per instance) — a deliberately lightweight mitigation, not a
// distributed quota. Behind a load balancer, scope per-instance limits down or
// add an edge limit; do not trust X-Forwarded-For here (it is spoofable).
type authLimiter struct {
	mu       sync.Mutex
	perEmail map[string]time.Time   // email -> last accepted send
	ipHits   map[string][]time.Time // ip -> request times within the window
	cooldown time.Duration
	ipWindow time.Duration
	ipMax    int
	swept    time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{
		perEmail: map[string]time.Time{},
		ipHits:   map[string][]time.Time{},
		cooldown: 60 * time.Second,
		ipWindow: time.Minute,
		ipMax:    8,
	}
}

// allowIP records a request from ip and reports whether it is within the cap.
func (l *authLimiter) allowIP(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	cut := now.Add(-l.ipWindow)
	kept := l.ipHits[ip][:0]
	for _, t := range l.ipHits[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.ipMax {
		l.ipHits[ip] = kept
		return false
	}
	l.ipHits[ip] = append(kept, now)
	return true
}

// emailInCooldown reports whether a link was sent to email too recently to send
// another. It does not record — call recordEmailSent only after a successful send
// so a transient provider failure never locks a real user out.
func (l *authLimiter) emailInCooldown(email string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	last, ok := l.perEmail[email]
	return ok && now.Sub(last) < l.cooldown
}

func (l *authLimiter) recordEmailSent(email string, now time.Time) {
	l.mu.Lock()
	l.perEmail[email] = now
	l.mu.Unlock()
}

// sweep drops expired entries so the maps don't grow without bound. Cheap and
// amortized: runs at most once per window.
func (l *authLimiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.ipWindow {
		return
	}
	l.swept = now
	ipCut := now.Add(-l.ipWindow)
	for ip, hits := range l.ipHits {
		kept := hits[:0]
		for _, t := range hits {
			if t.After(ipCut) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.ipHits, ip)
		} else {
			l.ipHits[ip] = kept
		}
	}
	emailCut := now.Add(-l.cooldown)
	for e, t := range l.perEmail {
		if t.Before(emailCut) {
			delete(l.perEmail, e)
		}
	}
}

// clientIP is the peer address (host only). We intentionally use RemoteAddr and
// not X-Forwarded-For: trusting a client-supplied header would let an attacker
// spoof a fresh IP per request and defeat the cap.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
