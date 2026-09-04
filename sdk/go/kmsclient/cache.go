package kmsclient

import (
	"strconv"
	"sync"
	"time"
)

// defaultMaxCacheEntries bounds the number of cached parameter entries.
const defaultMaxCacheEntries = 4096

// cache is an optional in-memory read cache for parameters. Secret plaintext
// is deliberately never retained because live protection can change without
// producing a new version.
//
// A nil *cache is a no-op cache: all reads miss and writes are dropped. This
// keeps the hot path branch-free when CacheTTL is 0.
type cache struct {
	ttl        time.Duration
	maxEntries int

	mu         sync.Mutex
	params     map[string]map[string]paramEntry
	paramCount int
}

type paramEntry struct {
	value   string
	expires time.Time
}

func newCache(ttl time.Duration) *cache {
	if ttl <= 0 {
		return nil
	}
	return &cache{
		ttl:        ttl,
		maxEntries: defaultMaxCacheEntries,
		params:     make(map[string]map[string]paramEntry),
	}
}

func subKey(version uint64, label string) string {
	return strconv.FormatUint(version, 10) + "\x00" + label
}

func (c *cache) getParam(path string, version uint64, label string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byKey, ok := c.params[path]
	if !ok {
		return "", false
	}
	e, ok := byKey[subKey(version, label)]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.value, true
}

func (c *cache) putParam(path string, version uint64, label, value string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byKey := c.params[path]
	if byKey == nil {
		byKey = make(map[string]paramEntry)
		c.params[path] = byKey
	}
	sk := subKey(version, label)
	if _, exists := byKey[sk]; !exists {
		c.paramCount++
	}
	byKey[sk] = paramEntry{value: value, expires: time.Now().Add(c.ttl)}
	if c.paramCount > c.maxEntries {
		c.evictParamsLocked()
	}
}

// evictParamsLocked brings paramCount back within maxEntries: it first sweeps
// expired entries, then evicts arbitrary entries (Go map iteration order is
// unspecified) until under the cap.
func (c *cache) evictParamsLocked() {
	now := time.Now()
	for path, byKey := range c.params {
		for sk, e := range byKey {
			if now.After(e.expires) {
				delete(byKey, sk)
				c.paramCount--
			}
		}
		if len(byKey) == 0 {
			delete(c.params, path)
		}
	}
	for c.paramCount > c.maxEntries {
		if !c.evictOneParamLocked() {
			return
		}
	}
}

func (c *cache) evictOneParamLocked() bool {
	for path, byKey := range c.params {
		for sk := range byKey {
			delete(byKey, sk)
			c.paramCount--
			if len(byKey) == 0 {
				delete(c.params, path)
			}
			return true
		}
		delete(c.params, path)
	}
	return false
}

// invalidateParam drops every cached view of a parameter path.
func (c *cache) invalidateParam(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if byKey, ok := c.params[path]; ok {
		c.paramCount -= len(byKey)
		delete(c.params, path)
	}
}
