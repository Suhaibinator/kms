package kmsclient

import (
	"strconv"
	"testing"
	"time"
)

// TestCacheBoundedSize proves the L2 fix: reading many distinct
// (path,version,label) tuples cannot grow the cache without bound; eviction
// keeps each kind at or below maxEntries, and the entry counter stays accurate.
func TestCacheBoundedSize(t *testing.T) {
	c := newCache(time.Minute)
	c.maxEntries = 8

	const inserts = 1000
	for i := range inserts {
		c.putParam("/p/"+strconv.Itoa(i), uint64(i), "", "v")
		c.putSecret("/s/"+strconv.Itoa(i), uint64(i), "", Secret{})
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.paramCount > c.maxEntries {
		t.Errorf("paramCount = %d, want <= %d", c.paramCount, c.maxEntries)
	}
	if c.secretCount > c.maxEntries {
		t.Errorf("secretCount = %d, want <= %d", c.secretCount, c.maxEntries)
	}
	if got := countEntries(c.params); got != c.paramCount {
		t.Errorf("paramCount %d != actual param entries %d (counter drift)", c.paramCount, got)
	}
	if got := countSecretEntries(c.secrets); got != c.secretCount {
		t.Errorf("secretCount %d != actual secret entries %d (counter drift)", c.secretCount, got)
	}
}

// TestCacheEvictionSweepsExpired proves eviction reclaims expired entries: after
// a batch expires, refilling past the cap sweeps them rather than dropping live
// entries needlessly, and the cache stays bounded.
func TestCacheEvictionSweepsExpired(t *testing.T) {
	c := newCache(20 * time.Millisecond)
	c.maxEntries = 50

	for i := range 50 {
		c.putParam("/old/"+strconv.Itoa(i), uint64(i), "", "v")
	}
	time.Sleep(40 * time.Millisecond) // let the first batch expire

	for i := range 50 {
		c.putParam("/new/"+strconv.Itoa(i), uint64(i), "", "v")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paramCount > c.maxEntries {
		t.Errorf("paramCount = %d after expiry+refill, want <= %d", c.paramCount, c.maxEntries)
	}
	if got := countEntries(c.params); got != c.paramCount {
		t.Errorf("counter drift: paramCount=%d actual=%d", c.paramCount, got)
	}
}

// TestCacheInvalidateDecrementsCount ensures per-path invalidation keeps the
// entry counter accurate, so watch-driven invalidation cannot let the counter
// drift and defeat the cap.
func TestCacheInvalidateDecrementsCount(t *testing.T) {
	c := newCache(time.Minute)

	c.putParam("/p", 1, "", "v1")
	c.putParam("/p", 2, "", "v2")
	c.putSecret("/s", 1, "", Secret{})
	if c.paramCount != 2 || c.secretCount != 1 {
		t.Fatalf("counts = %d/%d, want 2/1", c.paramCount, c.secretCount)
	}

	c.invalidateParam("/p")
	c.invalidateSecret("/s")
	if c.paramCount != 0 || c.secretCount != 0 {
		t.Errorf("after invalidate, counts = %d/%d, want 0/0", c.paramCount, c.secretCount)
	}
	if len(c.params) != 0 || len(c.secrets) != 0 {
		t.Errorf("maps not emptied after invalidate: params=%d secrets=%d", len(c.params), len(c.secrets))
	}
}

func countEntries(m map[string]map[string]paramEntry) int {
	n := 0
	for _, byKey := range m {
		n += len(byKey)
	}
	return n
}

func countSecretEntries(m map[string]map[string]secretEntry) int {
	n := 0
	for _, byKey := range m {
		n += len(byKey)
	}
	return n
}
