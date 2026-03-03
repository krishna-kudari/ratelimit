// Package cache provides an L1 in-process cache that wraps any Limiter.
//
// At scale, even Redis adds 0.5–2ms per request. The LocalCache sits in front
// of the backend limiter and serves most checks locally (~50ns) by caching
// results and tracking local request counts between syncs.
//
//	Request → L1 (in-process, ~50ns) → L2 (Redis, ~1ms) → Decision
//
// Usage:
//
//	baseLimiter, _ := goratelimit.NewGCRA(1000, 50, goratelimit.WithRedis(client))
//	limiter := cache.New(baseLimiter, cache.WithTTL(100*time.Millisecond))
//	// limiter implements goratelimit.Limiter
//	result, err := limiter.Allow(ctx, "user:123")
package cache

import (
	"context"
	"sync"
	"time"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// CacheOption configures the LocalCache.
type CacheOption func(*cacheConfig)

type cacheConfig struct {
	ttl     time.Duration
	maxKeys int
}

// WithTTL sets the cache entry TTL. After this duration, the next request
// for that key will sync with the backend. Lower values = more accurate,
// higher values = less Redis load. Default: 100ms.
func WithTTL(ttl time.Duration) CacheOption {
	return func(c *cacheConfig) { c.ttl = ttl }
}

// WithMaxKeys sets the maximum number of cached keys. When exceeded, the
// oldest entries are evicted. Default: 100000.
func WithMaxKeys(maxKeys int) CacheOption {
	return func(c *cacheConfig) { c.maxKeys = maxKeys }
}

// LocalCache is an L1 in-process cache that wraps any Limiter.
// It implements goratelimit.Limiter so it can be used as a drop-in replacement.
//
// On each Allow/AllowN call:
//  1. Cache hit + remaining quota → serve locally (sub-microsecond)
//  2. Cache hit + quota exhausted → sync with backend
//  3. Cache miss or expired → sync with backend
//
// Denied results are cached until RetryAfter expires, preventing
// thundering herd on the backend for rate-limited keys.
type LocalCache struct {
	inner   goratelimit.Limiter
	config  cacheConfig
	mu      sync.Mutex
	entries map[string]cacheEntry
	closeCh chan struct{}
	closed  bool
}

type cacheEntry struct {
	result    goratelimit.Result
	localUsed int64
	fetchedAt time.Time
}

// New wraps an existing Limiter with a local cache layer.
func New(inner goratelimit.Limiter, opts ...CacheOption) *LocalCache {
	cfg := cacheConfig{
		ttl:     100 * time.Millisecond,
		maxKeys: 100000,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	lc := &LocalCache{
		inner:   inner,
		config:  cfg,
		entries: make(map[string]cacheEntry),
		closeCh: make(chan struct{}),
	}
	go lc.evictionLoop()
	return lc
}

// Allow checks whether a single request for key should be allowed.
func (lc *LocalCache) Allow(ctx context.Context, key string) (goratelimit.Result, error) {
	return lc.AllowN(ctx, key, 1)
}

// AllowN checks whether n requests for key should be allowed.
func (lc *LocalCache) AllowN(ctx context.Context, key string, n int) (goratelimit.Result, error) {
	lc.mu.Lock()

	e, ok := lc.entries[key]
	if ok && !lc.isExpired(&e) {
		// Cached denial — don't hammer the backend
		if !e.result.Allowed {
			lc.mu.Unlock()
			return e.result, nil
		}

		// Cached allow — check if local quota remains
		cost := int64(n)
		if e.result.Remaining-e.localUsed >= cost {
			e.localUsed += cost
			r := goratelimit.Result{
				Allowed:   true,
				Remaining: e.result.Remaining - e.localUsed,
				Limit:     e.result.Limit,
				ResetAt:   e.result.ResetAt,
			}
			lc.entries[key] = e
			lc.mu.Unlock()
			return r, nil
		}
		// Local quota exhausted — need to sync
	}
	lc.mu.Unlock()

	// Cache miss, expired, or local quota exhausted → sync with backend
	result, err := lc.inner.AllowN(ctx, key, n)
	if err != nil {
		return goratelimit.Result{}, err
	}

	lc.mu.Lock()
	lc.entries[key] = cacheEntry{
		result:    result,
		localUsed: 0,
		fetchedAt: time.Now(),
	}
	lc.evictIfOverCapacity()
	lc.mu.Unlock()

	return result, nil
}

// Reset clears rate limit state for key in both cache and backend.
func (lc *LocalCache) Reset(ctx context.Context, key string) error {
	lc.mu.Lock()
	delete(lc.entries, key)
	lc.mu.Unlock()
	return lc.inner.Reset(ctx, key)
}

// Close stops the background eviction goroutine.
func (lc *LocalCache) Close() {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !lc.closed {
		lc.closed = true
		close(lc.closeCh)
	}
}

// Stats returns current cache statistics.
func (lc *LocalCache) Stats() CacheStats {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return CacheStats{
		Keys: len(lc.entries),
	}
}

// CacheStats holds cache statistics.
type CacheStats struct {
	Keys int
}

func (lc *LocalCache) isExpired(e *cacheEntry) bool {
	ttl := lc.config.ttl

	// For denied results, use min(ttl, retryAfter) so we re-check
	// when the backend might allow again.
	if !e.result.Allowed && e.result.RetryAfter > 0 && e.result.RetryAfter < ttl {
		ttl = e.result.RetryAfter
	}

	return time.Since(e.fetchedAt) >= ttl
}

func (lc *LocalCache) evictIfOverCapacity() {
	if len(lc.entries) <= lc.config.maxKeys {
		return
	}
	// Evict oldest entries to get back under capacity
	var oldestKey string
	var oldestTime time.Time
	for k, e := range lc.entries {
		if oldestKey == "" || e.fetchedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.fetchedAt
		}
	}
	if oldestKey != "" {
		delete(lc.entries, oldestKey)
	}
}

func (lc *LocalCache) evictionLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			lc.evictExpired()
		case <-lc.closeCh:
			return
		}
	}
}

func (lc *LocalCache) evictExpired() {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for k, e := range lc.entries {
		if lc.isExpired(&e) {
			delete(lc.entries, k)
		}
	}
}
