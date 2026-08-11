package provider

import (
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// defaultKeyCapacity is the assumed per-key request capacity before the first
// response header is observed.
const defaultKeyCapacity = 1200

// minRemaining is the capacity threshold below which a key is skipped, so we
// never burn the last requests of a rate-limit window while another key still
// has headroom.
const minRemaining = 100

// KeyPool manages a pool of upstream API keys with weighted round-robin
// selection. It tracks each key's remaining request capacity (from provider
// response headers) and loosely maintains error counts for health awareness.
// Keys are internal state and are never exposed in logs or errors.
type KeyPool struct {
	keys []keyEntry
	mu   sync.Mutex
}

type keyEntry struct {
	key       string
	remaining atomic.Int64
	lastUsed  atomic.Int64
	errors    atomic.Int64
}

// NewKeyPool builds a pool from the given API keys.
func NewKeyPool(apiKeys []string) *KeyPool {
	entries := make([]keyEntry, len(apiKeys))
	for i, k := range apiKeys {
		entries[i] = keyEntry{key: k}
		entries[i].remaining.Store(defaultKeyCapacity)
	}
	return &KeyPool{keys: entries}
}

// Len returns the number of keys in the pool.
func (p *KeyPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

// Select picks the healthiest key (highest remaining capacity, least recently
// used as a tie-breaker). Returns "" when all keys are exhausted.
func (p *KeyPool) Select() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	var bestIdx = -1
	var bestRemaining int64 = -1
	bestLastUsed := time.Now().Unix()

	for i := range p.keys {
		if p.keys[i].key == "" {
			continue
		}
		remaining := p.keys[i].remaining.Load()
		if remaining < minRemaining {
			continue // Skip keys with low capacity.
		}

		lastUsed := p.keys[i].lastUsed.Load()
		if remaining > bestRemaining ||
			(remaining == bestRemaining && lastUsed < bestLastUsed) {
			bestIdx = i
			bestRemaining = remaining
			bestLastUsed = lastUsed
		}
	}

	if bestIdx == -1 {
		return "" // All keys exhausted.
	}

	p.keys[bestIdx].lastUsed.Store(time.Now().Unix())
	return p.keys[bestIdx].key
}

// UpdateFromHeaders reads the provider's rate-limit headers from a response
// and updates the corresponding key's remaining capacity.
func (p *KeyPool) UpdateFromHeaders(key string, headers http.Header) {
	remaining := headers.Get("X-RateLimit-Remaining")
	if remaining == "" {
		return
	}

	rem, err := strconv.ParseInt(remaining, 10, 64)
	if err != nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.keys {
		if p.keys[i].key == key {
			p.keys[i].remaining.Store(rem)
			return
		}
	}
}

// MarkError increments the error counter for a key.
func (p *KeyPool) MarkError(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.keys {
		if p.keys[i].key == key {
			p.keys[i].errors.Add(1)
			return
		}
	}
}

// ErrorCount returns the error count for a key (used in tests/health).
func (p *KeyPool) ErrorCount(key string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.keys {
		if p.keys[i].key == key {
			return p.keys[i].errors.Load()
		}
	}
	return 0
}
