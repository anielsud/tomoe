package calendar

import (
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

// providerCache is a small in-memory TTL cache for Provider.ListEvents
// results. Keyed by (providerName, from, to). Two enrichment calls in the
// same wall-clock window reuse one round trip to the provider.
type providerCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
}

type cacheKey struct {
	provider string
	from     time.Time
	to       time.Time
}

type cacheEntry struct {
	events    []calendar.Event
	expiresAt time.Time
}

func newProviderCache(ttl time.Duration) *providerCache {
	return &providerCache{
		ttl:     ttl,
		entries: make(map[cacheKey]cacheEntry),
	}
}

// get returns cached events and true when the entry exists and is unexpired.
func (c *providerCache) get(provider string, from, to time.Time) ([]calendar.Event, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[cacheKey{provider, from, to}]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, cacheKey{provider, from, to})
		return nil, false
	}
	// Return a defensive copy so downstream scoring can't mutate cached state.
	out := make([]calendar.Event, len(entry.events))
	copy(out, entry.events)
	return out, true
}

func (c *providerCache) set(provider string, from, to time.Time, events []calendar.Event) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := make([]calendar.Event, len(events))
	copy(stored, events)
	c.entries[cacheKey{provider, from, to}] = cacheEntry{
		events:    stored,
		expiresAt: time.Now().Add(c.ttl),
	}
}
