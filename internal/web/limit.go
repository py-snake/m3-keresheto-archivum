package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// clientIP returns the address of the visitor. Forwarding headers are believed only when the
// direct peer is one of the trusted proxies; otherwise they are ignored (they can be forged).
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := peerAddr(r.RemoteAddr)
	if !peer.IsValid() || !contains(trusted, peer) {
		return peer
	}
	if v := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); v != "" {
		if a, err := netip.ParseAddr(v); err == nil {
			return a.Unmap()
		}
	}
	// X-Forwarded-For: take the right-most address that is not one of our own proxies
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !contains(trusted, a) {
			return a
		}
	}
	return peer
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func contains(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// limiter is a per-client token bucket with bounded memory.
type limiter struct {
	rate, burst float64
	maxEntries  int
	now         func() time.Time

	mu        sync.Mutex
	buckets   map[netip.Addr]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst, maxEntries int, now func() time.Time) *limiter {
	return &limiter{rate: rate, burst: float64(burst), maxEntries: maxEntries, now: now, buckets: make(map[netip.Addr]*bucket)}
}

// key groups IPv6 clients by /64 (one customer usually owns a whole /64).
func key(a netip.Addr) netip.Addr {
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.Addr()
	}
	return a
}

func (l *limiter) allow(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	k := key(a)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[k]
	if !ok {
		if len(l.buckets) >= l.maxEntries {
			l.sweep(now)
		}
		if len(l.buckets) >= l.maxEntries {
			// Memory is bounded: when the table is full of active clients we stop tracking
			// new ones instead of growing. The global in-flight cap still protects the server.
			return true
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[k] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets that would be full again (their client has been quiet).
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Second {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// anonymizer turns an address into a short pseudonym for the logs. The key is random, kept in
// memory only and replaced every day, so the pseudonyms cannot be reversed or linked over days.
type anonymizer struct {
	mu  sync.Mutex
	day string
	key [32]byte
	now func() time.Time
}

func (a *anonymizer) name(ip netip.Addr) string {
	if !ip.IsValid() {
		return "-"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if day := a.now().UTC().Format("2006-01-02"); day != a.day {
		a.day = day
		_, _ = rand.Read(a.key[:])
	}
	mac := hmac.New(sha256.New, a.key[:])
	mac.Write(key(ip).AsSlice())
	return hex.EncodeToString(mac.Sum(nil)[:6])
}
