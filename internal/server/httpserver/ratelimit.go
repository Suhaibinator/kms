package httpserver

import (
	"sync"
	"time"
)

// rateLimiter is a per-key token-bucket limiter used to throttle login and
// failed-authentication attempts by client IP (plan 18.4). It refills at rate
// tokens per second up to burst tokens. It is safe for concurrent use.
//
// The bucket map is bounded: a caller cannot exhaust memory by presenting an
// unbounded set of distinct keys. When the map reaches maxBuckets, refilled
// (idle) buckets are swept before a new one is admitted; if none can be swept,
// the new event is denied rather than growing the map without limit.
type rateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	rate       float64 // tokens added per second
	burst      float64 // maximum tokens
	maxBuckets int
	now        func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// defaultMaxBuckets caps the number of tracked keys.
const defaultMaxBuckets = 65536

// newRateLimiter builds a limiter allowing burst immediate events, refilling at
// perMinute events per minute.
func newRateLimiter(perMinute, burst float64) *rateLimiter {
	return &rateLimiter{
		buckets:    make(map[string]*bucket),
		rate:       perMinute / 60.0,
		burst:      burst,
		maxBuckets: defaultMaxBuckets,
		now:        time.Now,
	}
}

// allow reports whether an event for key may proceed, consuming a token when it
// can. An empty key is treated as a single shared bucket.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.maxBuckets && l.sweep(now) == 0 {
			// Map is full of actively-throttled keys; refuse rather than grow.
			return false
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		l.refill(b, now)
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// refund returns one previously reserved token after successful
// authentication. Reserving before credential verification means an exhausted
// bucket avoids the expensive authentication path entirely, while successful
// requests do not consume the failed-auth budget.
func (l *rateLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		return
	}
	l.refill(b, l.now())
	b.tokens++
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
}

// refill updates one bucket to now. Caller holds mu.
func (l *rateLimiter) refill(b *bucket, now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
}

// sweep drops buckets that have refilled to full (i.e. the key has been idle
// long enough to no longer be throttled), reclaiming space. Caller holds mu.
// Returns the number of buckets removed.
func (l *rateLimiter) sweep(now time.Time) int {
	removed := 0
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
			removed++
		}
	}
	return removed
}
