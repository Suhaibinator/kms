// Package ratelimit implements a bounded per-key token-bucket limiter shared
// by the HTTP login/failed-authentication throttle and the per-identity
// budgets on rate-limited gRPC operations.
//
// The limiter is in-memory and process-local: every server instance keeps its
// own buckets, so a horizontally scaled deployment enforces the configured
// budget per instance rather than globally. That is the intended v1 scope.
package ratelimit

import (
	"sync"
	"time"
)

// DefaultMaxBuckets caps the number of tracked keys.
const DefaultMaxBuckets = 65536

// Limiter is a per-key token bucket. It refills at rate tokens per second up
// to burst tokens and is safe for concurrent use.
//
// The bucket map is bounded: a caller cannot exhaust memory by presenting an
// unbounded set of distinct keys. When the map reaches maxBuckets, refilled
// (idle) buckets are swept before a new one is admitted; if none can be swept,
// the new event is denied rather than growing the map without limit.
type Limiter struct {
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

// New builds a limiter allowing burst immediate events, refilling at
// perMinute events per minute.
func New(perMinute, burst float64) *Limiter {
	return &Limiter{
		buckets:    make(map[string]*bucket),
		rate:       perMinute / 60.0,
		burst:      burst,
		maxBuckets: DefaultMaxBuckets,
		now:        time.Now,
	}
}

// SetNow replaces the clock. It exists for tests.
func (l *Limiter) SetNow(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// Allow reports whether an event for key may proceed, consuming one token
// when it can. An empty key is treated as a single shared bucket.
func (l *Limiter) Allow(key string) bool {
	return l.Take(key, 1)
}

// Take atomically consumes n tokens for key, or nothing at all. It reports
// whether the tokens were available. A non-positive n always succeeds without
// consuming anything.
func (l *Limiter) Take(key string, n float64) bool {
	if n <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.lookup(key)
	if !ok {
		return false
	}
	if b.tokens >= n {
		b.tokens -= n
		return true
	}
	return false
}

// lookup returns the refilled bucket for key, creating it when absent. It
// reports false when the map is full of actively-throttled keys and no bucket
// can be admitted. Caller holds mu.
func (l *Limiter) lookup(key string) (*bucket, bool) {
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.maxBuckets && l.sweep(now) == 0 {
			// Map is full of actively-throttled keys; refuse rather than grow.
			return nil, false
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
		return b, true
	}
	l.refill(b, now)
	return b, true
}

// Refund returns one previously reserved token. The HTTP transport reserves
// before credential verification so an exhausted bucket avoids the expensive
// authentication path entirely, then refunds on success so successful
// requests do not consume the failed-auth budget.
func (l *Limiter) Refund(key string) {
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
func (l *Limiter) refill(b *bucket, now time.Time) {
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
func (l *Limiter) sweep(now time.Time) int {
	removed := 0
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
			removed++
		}
	}
	return removed
}
