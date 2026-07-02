package paramstore

import (
	"strconv"
	"sync"
	"time"
)

// defaultMaxCacheEntries bounds the number of cached entries per kind
// (parameters, secrets) so a long-lived client that reads many distinct
// (path,version,label) tuples cannot grow the cache without limit (defect L2).
const defaultMaxCacheEntries = 4096

// cache is an optional in-memory read cache for parameters and secrets. It is
// keyed by path, then by a (version,label) subkey so that different reads of
// the same path do not collide. Invalidation is per-path, which lets watch
// events cheaply drop every cached view of a changed path.
//
// A nil *cache is a no-op cache: all reads miss and writes are dropped. This
// keeps the hot path branch-free when CacheTTL is 0.
type cache struct {
	ttl        time.Duration
	maxEntries int

	mu          sync.Mutex
	params      map[string]map[string]paramEntry
	secrets     map[string]map[string]secretEntry
	paramCount  int
	secretCount int
}

type paramEntry struct {
	value   string
	expires time.Time
}

type secretEntry struct {
	secret  Secret
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
		secrets:    make(map[string]map[string]secretEntry),
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

func (c *cache) getSecret(path string, version uint64, label string) (Secret, bool) {
	if c == nil {
		return Secret{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byKey, ok := c.secrets[path]
	if !ok {
		return Secret{}, false
	}
	e, ok := byKey[subKey(version, label)]
	if !ok || time.Now().After(e.expires) {
		return Secret{}, false
	}
	return e.secret, true
}

func (c *cache) putSecret(path string, version uint64, label string, s Secret) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byKey := c.secrets[path]
	if byKey == nil {
		byKey = make(map[string]secretEntry)
		c.secrets[path] = byKey
	}
	sk := subKey(version, label)
	if _, exists := byKey[sk]; !exists {
		c.secretCount++
	}
	byKey[sk] = secretEntry{secret: s, expires: time.Now().Add(c.ttl)}
	if c.secretCount > c.maxEntries {
		c.evictSecretsLocked()
	}
}

// evictSecretsLocked mirrors evictParamsLocked for the secret cache.
func (c *cache) evictSecretsLocked() {
	now := time.Now()
	for path, byKey := range c.secrets {
		for sk, e := range byKey {
			if now.After(e.expires) {
				delete(byKey, sk)
				c.secretCount--
			}
		}
		if len(byKey) == 0 {
			delete(c.secrets, path)
		}
	}
	for c.secretCount > c.maxEntries {
		if !c.evictOneSecretLocked() {
			return
		}
	}
}

func (c *cache) evictOneSecretLocked() bool {
	for path, byKey := range c.secrets {
		for sk := range byKey {
			delete(byKey, sk)
			c.secretCount--
			if len(byKey) == 0 {
				delete(c.secrets, path)
			}
			return true
		}
		delete(c.secrets, path)
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

// invalidateSecret drops every cached view of a secret path.
func (c *cache) invalidateSecret(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if byKey, ok := c.secrets[path]; ok {
		c.secretCount -= len(byKey)
		delete(c.secrets, path)
	}
}
