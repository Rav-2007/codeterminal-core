package main

import (
	"container/list"
	"sync"
	"time"
)

// API-key authorization cache.
//
// WHY: measured on the real binary with a production-shaped Supabase latency,
// authorize() and reserveQuota() each cost one full round trip -- 91 ms and
// 91 ms, together 80% of a 228 ms request (docs/LATENCY_BASELINE.md §A.3). Both
// happen before a single byte goes upstream. authorize() re-establishes, on
// every request, a fact that changes approximately never: that this key hash
// maps to this api_keys.id. Caching it removes one of the two round trips
// outright for every warm key.
//
// The security properties are not incidental to the design; they ARE the design,
// because a cache in front of an auth check is the kind of optimisation that
// quietly becomes a vulnerability:
//
//  1. POSITIVE RESULTS ONLY. A failed lookup is never cached. An attacker
//     presenting invalid keys therefore gets no benefit at all -- every attempt
//     still costs a full Supabase round trip and is still subject to the
//     pre-auth rate limiter. The cache cannot be used to amplify credential
//     stuffing, and there is no negative entry to poison.
//
//  2. THE TTL IS THE ENTIRE REVOCATION EXPOSURE. A key revoked in the database
//     keeps working for at most authCacheTTL. That window is the whole cost of
//     this optimisation and it is stated here, in the log at startup, and in
//     SECURITY_MODEL.md -- rather than being discoverable only by reading this
//     file.
//
//  3. THERE IS AN OFF SWITCH. flush() drops every entry, exposed on an
//     admin-authenticated endpoint, so revocation can be made immediate without
//     a redeploy. A 30-second window is a reasonable default; "30 seconds and
//     nothing you can do about it" is not.
//
//  4. KEYED BY HASH, NEVER BY KEY. The map key is the SHA-256 hex authorize()
//     has already computed. The caller's actual API key never enters this
//     structure, so a heap dump yields what the database already stores rather
//     than a set of working credentials.
//
// What is deliberately NOT here: negative caching, per-key TTL jitter, and
// refresh-ahead. Each would buy a little and each would add a way for this to be
// wrong about whether a key is still valid.

// authCacheTTL bounds how long a revoked key keeps working. 30 seconds was the
// founder's call, taken against the measured 91 ms it saves.
const authCacheTTL = 30 * time.Second

// authCacheMaxEntries bounds memory. Entries are small (a hash key and an id),
// so this is generous; its purpose is that the map cannot grow without limit if
// a caller cycles through many distinct valid keys.
const authCacheMaxEntries = 10_000

type authCacheEntry struct {
	hash     string // SHA-256 hex of the API key -- the map key, repeated for eviction
	keyID    string // api_keys.id
	expires  time.Time
	listElem *list.Element
}

// authCache is a TTL + LRU cache of successful key-hash -> api_keys.id lookups.
//
// The LRU list and the map are maintained together under one mutex. A
// sync.Map-plus-atomics design would avoid the lock but cannot maintain
// recency order, which is what makes the bound a bound rather than a
// whoever-arrived-first cutoff.
type authCache struct {
	mu      sync.Mutex
	entries map[string]*authCacheEntry
	lru     *list.List // front = most recently used
	ttl     time.Duration
	max     int

	// now is injectable so the TTL can be tested without sleeping. Production
	// leaves it nil and gets time.Now.
	now func() time.Time
}

func newAuthCache(ttl time.Duration, max int) *authCache {
	return &authCache{
		entries: make(map[string]*authCacheEntry),
		lru:     list.New(),
		ttl:     ttl,
		max:     max,
	}
}

func (c *authCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the cached api_keys.id for a key hash, if one is present and
// unexpired.
//
// An expired entry is deleted rather than returned-and-ignored, so a key that
// stops being used stops occupying space at the moment it is next looked at.
func (c *authCache) get(hash string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[hash]
	if !ok {
		return "", false
	}
	if !c.clock().Before(e.expires) {
		c.removeLocked(e)
		return "", false
	}
	c.lru.MoveToFront(e.listElem)
	return e.keyID, true
}

// put records a SUCCESSFUL lookup. Callers must never call this for a failed
// one -- see property 1 above.
func (c *authCache) put(hash, keyID string) {
	if c == nil || hash == "" || keyID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[hash]; ok {
		e.keyID = keyID
		e.expires = c.clock().Add(c.ttl)
		c.lru.MoveToFront(e.listElem)
		return
	}

	e := &authCacheEntry{hash: hash, keyID: keyID, expires: c.clock().Add(c.ttl)}
	e.listElem = c.lru.PushFront(e)
	c.entries[hash] = e

	for c.lru.Len() > c.max {
		if back := c.lru.Back(); back != nil {
			c.removeLocked(back.Value.(*authCacheEntry))
		}
	}
}

func (c *authCache) removeLocked(e *authCacheEntry) {
	c.lru.Remove(e.listElem)
	delete(c.entries, e.hash)
}

// flush drops every entry. This is the immediate-revocation lever: after it
// returns, the next request for any key pays a full lookup again.
func (c *authCache) flush() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.entries)
	c.entries = make(map[string]*authCacheEntry)
	c.lru.Init()
	return n
}

// len reports the current entry count, for the counters.
func (c *authCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
