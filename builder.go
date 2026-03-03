package goratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/krishna-kudari/ratelimit/store"
)

type algorithm int

const (
	algoNone algorithm = iota
	algoFixedWindow
	algoSlidingWindow
	algoSlidingWindowCounter
	algoTokenBucket
	algoLeakyBucket
	algoGCRA
	algoCMS
)

// Builder provides a fluent API for constructing a Limiter.
//
//	limiter, err := goratelimit.NewBuilder().
//	    FixedWindow(100, 60*time.Second).
//	    Redis(client).
//	    HashTag().
//	    Build()
type Builder struct {
	algo algorithm
	opts []Option

	// window-based (fixed, sliding, sliding counter)
	maxRequests   int64
	windowSeconds int64

	// token bucket
	tbCapacity   int64
	tbRefillRate int64

	// leaky bucket
	lbCapacity int64
	lbLeakRate int64
	lbMode     LeakyBucketMode

	// gcra
	gcraRate  int64
	gcraBurst int64

	// cms
	cmsLimit      int64
	cmsWindowSecs int64
	cmsEpsilon    float64
	cmsDelta      float64
}

// NewBuilder returns a new Builder with default options.
func NewBuilder() *Builder {
	return &Builder{}
}

// ─── Algorithm selectors ─────────────────────────────────────────────────────

// FixedWindow configures a Fixed Window algorithm.
// maxRequests is the limit per window. window is the window duration.
func (b *Builder) FixedWindow(maxRequests int64, window time.Duration) *Builder {
	b.algo = algoFixedWindow
	b.maxRequests = maxRequests
	b.windowSeconds = int64(window.Seconds())
	return b
}

// SlidingWindow configures a Sliding Window Log algorithm.
// maxRequests is the limit per window. window is the window duration.
// Stores every request timestamp; for high throughput prefer SlidingWindowCounter.
func (b *Builder) SlidingWindow(maxRequests int64, window time.Duration) *Builder {
	b.algo = algoSlidingWindow
	b.maxRequests = maxRequests
	b.windowSeconds = int64(window.Seconds())
	return b
}

// SlidingWindowCounter configures a Sliding Window Counter algorithm.
// maxRequests is the limit per window. window is the window duration.
// Uses weighted-counter approximation with O(1) memory per key.
func (b *Builder) SlidingWindowCounter(maxRequests int64, window time.Duration) *Builder {
	b.algo = algoSlidingWindowCounter
	b.maxRequests = maxRequests
	b.windowSeconds = int64(window.Seconds())
	return b
}

// TokenBucket configures a Token Bucket algorithm.
// capacity is the burst size. refillRate is tokens added per second.
func (b *Builder) TokenBucket(capacity, refillRate int64) *Builder {
	b.algo = algoTokenBucket
	b.tbCapacity = capacity
	b.tbRefillRate = refillRate
	return b
}

// LeakyBucket configures a Leaky Bucket algorithm.
// capacity is the bucket size. leakRate is tokens leaked per second.
// mode selects Policing (hard reject) or Shaping (queue with delay).
func (b *Builder) LeakyBucket(capacity, leakRate int64, mode LeakyBucketMode) *Builder {
	b.algo = algoLeakyBucket
	b.lbCapacity = capacity
	b.lbLeakRate = leakRate
	b.lbMode = mode
	return b
}

// GCRA configures a Generic Cell Rate Algorithm limiter.
// rate is sustained requests per second. burst is the maximum burst.
func (b *Builder) GCRA(rate, burst int64) *Builder {
	b.algo = algoGCRA
	b.gcraRate = rate
	b.gcraBurst = burst
	return b
}

// CMS configures a Count-Min Sketch rate limiter.
// limit is the max requests per window. window is the window duration.
// epsilon is the acceptable error rate (e.g. 0.01).
// delta is the failure probability (e.g. 0.001).
// This algorithm is in-memory only; Redis options are ignored.
func (b *Builder) CMS(limit int64, window time.Duration, epsilon, delta float64) *Builder {
	b.algo = algoCMS
	b.cmsLimit = limit
	b.cmsWindowSecs = int64(window.Seconds())
	b.cmsEpsilon = epsilon
	b.cmsDelta = delta
	return b
}

// ─── Option setters ──────────────────────────────────────────────────────────

// Redis sets the Redis backend. Accepts any redis.UniversalClient.
func (b *Builder) Redis(client redis.UniversalClient) *Builder {
	b.opts = append(b.opts, WithRedis(client))
	return b
}

// Store sets a custom store.Store backend.
func (b *Builder) Store(s store.Store) *Builder {
	b.opts = append(b.opts, WithStore(s))
	return b
}

// KeyPrefix sets the prefix prepended to all storage keys.
func (b *Builder) KeyPrefix(prefix string) *Builder {
	b.opts = append(b.opts, WithKeyPrefix(prefix))
	return b
}

// HashTag enables Redis Cluster hash-tag wrapping on keys.
func (b *Builder) HashTag() *Builder {
	b.opts = append(b.opts, WithHashTag())
	return b
}

// FailOpen sets the fail-open/fail-closed behavior when the backend is unreachable.
func (b *Builder) FailOpen(v bool) *Builder {
	b.opts = append(b.opts, WithFailOpen(v))
	return b
}

// DryRun enables dry-run mode: never deny, log when a request would have been denied.
func (b *Builder) DryRun(dryRun bool) *Builder {
	b.opts = append(b.opts, WithDryRun(dryRun))
	return b
}

// DryRunLogFunc sets the logger called when dry run would have denied a request.
func (b *Builder) DryRunLogFunc(fn func(key string, result *Result)) *Builder {
	b.opts = append(b.opts, WithDryRunLogFunc(fn))
	return b
}

// LimitFunc sets a dynamic per-key limit resolver.
// The function is called on every Allow/AllowN with context and key.
// Return the limit, goratelimit.Unlimited for no limit, or <= 0 to use the default.
func (b *Builder) LimitFunc(fn func(ctx context.Context, key string) int64) *Builder {
	b.opts = append(b.opts, WithLimitFunc(fn))
	return b
}

// ─── Build ───────────────────────────────────────────────────────────────────

// Build validates the configuration and returns the configured Limiter.
func (b *Builder) Build() (Limiter, error) {
	switch b.algo {
	case algoFixedWindow:
		return NewFixedWindow(b.maxRequests, b.windowSeconds, b.opts...)
	case algoSlidingWindow:
		return NewSlidingWindow(b.maxRequests, b.windowSeconds, b.opts...)
	case algoSlidingWindowCounter:
		return NewSlidingWindowCounter(b.maxRequests, b.windowSeconds, b.opts...)
	case algoTokenBucket:
		return NewTokenBucket(b.tbCapacity, b.tbRefillRate, b.opts...)
	case algoLeakyBucket:
		return NewLeakyBucket(b.lbCapacity, b.lbLeakRate, b.lbMode, b.opts...)
	case algoGCRA:
		return NewGCRA(b.gcraRate, b.gcraBurst, b.opts...)
	case algoCMS:
		return NewCMS(b.cmsLimit, b.cmsWindowSecs, b.cmsEpsilon, b.cmsDelta, b.opts...)
	default:
		return nil, fmt.Errorf("goratelimit: no algorithm selected; call FixedWindow, SlidingWindow, TokenBucket, LeakyBucket, GCRA, or CMS before Build")
	}
}
