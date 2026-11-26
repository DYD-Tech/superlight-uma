package server

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter is a per-key token bucket limiter with idle eviction.
type ipLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	entries map[string]*limEntry
	lastGC  time.Time
}

type limEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

const limiterIdle = 10 * time.Minute

func newIPLimiter(perSec float64, burst int) *ipLimiter {
	return &ipLimiter{limit: rate.Limit(perSec), burst: burst, entries: map[string]*limEntry{}, lastGC: time.Now()}
}

func (l *ipLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) > time.Minute {
		for k, e := range l.entries {
			if now.Sub(e.seen) > limiterIdle {
				delete(l.entries, k)
			}
		}
		l.lastGC = now
	}
	e, ok := l.entries[key]
	if !ok {
		e = &limEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}
