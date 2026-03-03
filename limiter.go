package goratelimit

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/krishna-kudari/ratelimit/store"
)

// Unlimited is the sentinel value for no rate limit. Return it from LimitFunc
// to allow the key without consuming quota (e.g. trusted users, internal services).
const Unlimited int64 = -1

// Limiter is the core interface for all rate limiting algorithms.
// All implementations (in-memory and Redis-backed) satisfy this interface,
// making algorithms swappable without changing caller code.
type Limiter interface {
	// Allow checks whether a single request identified by key should be allowed.
	Allow(ctx context.Context, key string) (*Result, error)

	// AllowN checks whether n requests identified by key should be allowed.
	AllowN(ctx context.Context, key string, n int) (*Result, error)

	// Reset clears all rate limit state for the given key.
	Reset(ctx context.Context, key string) error
}

// Result holds the outcome of a rate limit check.
type Result struct {
	Allowed    bool
	Remaining  int64
	Limit      int64
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Options configures behavior shared across all algorithm implementations.
type Options struct {
	// Store is the pluggable backend for rate limit state.
	// Takes precedence over RedisClient if both are set.
	Store store.Store

	// RedisClient is a Redis connection for distributed rate limiting.
	// Accepts *redis.Client, *redis.ClusterClient, *redis.Ring, or any
	// redis.UniversalClient implementation.
	RedisClient redis.UniversalClient

	// KeyPrefix is prepended to all storage keys.
	// Default: "ratelimit".
	KeyPrefix string

	// FailOpen controls behavior when the backend is unreachable.
	// If true (default), requests are allowed on errors.
	// If false, requests are denied on errors.
	FailOpen bool

	// HashTag enables Redis Cluster hash-tag wrapping of user keys.
	// When true, keys are formatted as "prefix:{key}" instead of "prefix:key",
	// ensuring all keys for the same logical entity route to the same slot.
	// This is required for Sliding Window Counter (multi-key) and recommended
	// for any Redis Cluster deployment.
	HashTag bool

	// LimitFunc dynamically resolves the rate limit for each key.
	// Called with the request context (e.g. from middleware) so limits can depend on
	// user plan, JWT claims, or other context values. Returns the effective limit
	// (maxRequests / capacity / burst). Return Unlimited for no limit; return <= 0
	// (other than Unlimited) to use the construction-time default.
	LimitFunc func(ctx context.Context, key string) int64

	// Clock provides the current time. If nil, time.Now is used.
	// Inject a FakeClock in tests to advance time without time.Sleep.
	Clock Clock

	// DryRun, when true, never denies: Allow/AllowN always return Allowed=true,
	// but when a request would have been denied, the optional DryRunLogFunc is
	// called (or log.Printf with [DRYRUN] prefix if nil) so operators can see
	// what would be rate limited.
	DryRun bool

	// DryRunLogFunc is called when DryRun is true and a request would have been
	// denied. If nil, log.Printf("[DRYRUN] would deny key=...") is used.
	DryRunLogFunc func(key string, result *Result)
}

// Option is a functional option for configuring a Limiter.
type Option func(*Options)

// WithStore configures the limiter to use a custom store.Store backend.
// This takes precedence over WithRedis if both are set.
func WithStore(s store.Store) Option {
	return func(o *Options) { o.Store = s }
}

// WithRedis configures the limiter to use Redis as its backing store.
// Accepts any redis.UniversalClient: *redis.Client (standalone),
// *redis.ClusterClient (cluster), *redis.Ring (ring), or sentinel.
// When set, the limiter operates in distributed mode.
func WithRedis(client redis.UniversalClient) Option {
	return func(o *Options) { o.RedisClient = client }
}

// WithKeyPrefix sets the prefix prepended to all storage keys.
// Default: "ratelimit".
func WithKeyPrefix(prefix string) Option {
	return func(o *Options) { o.KeyPrefix = prefix }
}

// WithFailOpen controls behavior when the backend is unreachable.
// If true (default), requests are allowed on errors.
// If false, requests are denied on errors.
func WithFailOpen(failOpen bool) Option {
	return func(o *Options) { o.FailOpen = failOpen }
}

// WithHashTag enables Redis Cluster hash-tag wrapping.
// Keys become "prefix:{key}" so all keys for a given user route
// to the same Redis Cluster slot. Required for multi-key algorithms
// (Sliding Window Counter) in Cluster mode.
func WithHashTag() Option {
	return func(o *Options) { o.HashTag = true }
}

// WithLimitFunc sets a dynamic limit resolver. The function is called on
// every Allow/AllowN with the request context and key. Use context for plan-based
// limits (e.g. ctx.Value("plan")). Return the effective limit, Unlimited for
// no limit, or <= 0 (other than Unlimited) to use the construction-time default.
func WithLimitFunc(fn func(ctx context.Context, key string) int64) Option {
	return func(o *Options) { o.LimitFunc = fn }
}

// WithClock sets the clock used for time. In tests, pass a FakeClock and call
// Advance to simulate elapsed time without time.Sleep.
func WithClock(clock Clock) Option {
	return func(o *Options) { o.Clock = clock }
}

// WithDryRun enables dry-run mode: the limiter never denies; when a request
// would have been denied, DryRunLogFunc is called (or [DRYRUN] is logged).
// Use for safe production rollout to observe what would be rate limited.
func WithDryRun(dryRun bool) Option {
	return func(o *Options) { o.DryRun = dryRun }
}

// WithDryRunLogFunc sets the logger called when DryRun is true and a request
// would have been denied. If nil, log.Printf with [DRYRUN] prefix is used.
func WithDryRunLogFunc(fn func(key string, result *Result)) Option {
	return func(o *Options) { o.DryRunLogFunc = fn }
}

func defaultOptions() *Options {
	return &Options{
		KeyPrefix: "ratelimit",
		FailOpen:  true,
	}
}

func applyOptions(opts []Option) *Options {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// now returns the current time from the option's clock, or time.Now if no clock is set.
func (o *Options) now() time.Time {
	if o != nil && o.Clock != nil {
		return o.Clock.Now()
	}
	return time.Now()
}

// resolveLimit returns the dynamic limit for key and whether the key is unlimited.
// When unlimited is true, the caller should allow without updating state.
func (o *Options) resolveLimit(ctx context.Context, key string, defaultLimit int64) (limit int64, unlimited bool) {
	if o.LimitFunc != nil {
		v := o.LimitFunc(ctx, key)
		if v == Unlimited {
			return 0, true
		}
		if v > 0 {
			return v, false
		}
	}
	return defaultLimit, false
}

// FormatKey builds a storage key. With HashTag enabled the user key is
// wrapped in {}: "prefix:{key}" so all derived keys for the same user
// land on the same Redis Cluster slot.
func (o *Options) FormatKey(key string) string {
	if o.HashTag {
		return o.KeyPrefix + ":{" + key + "}"
	}
	return o.KeyPrefix + ":" + key
}

// FormatKeySuffix builds a storage key with an additional suffix.
// "prefix:{key}:suffix" (hash-tag) or "prefix:key:suffix" (plain).
func (o *Options) FormatKeySuffix(key, suffix string) string {
	if o.HashTag {
		return o.KeyPrefix + ":{" + key + "}:" + suffix
	}
	return o.KeyPrefix + ":" + key + ":" + suffix
}

// dryRunLimiter wraps a Limiter and converts denials into allows when DryRun is true,
// logging what would have been blocked.
type dryRunLimiter struct {
	inner Limiter
	opts  *Options
}

func (d *dryRunLimiter) Allow(ctx context.Context, key string) (*Result, error) {
	return d.allowN(ctx, key, 1)
}

func (d *dryRunLimiter) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	return d.allowN(ctx, key, n)
}

func (d *dryRunLimiter) allowN(ctx context.Context, key string, n int) (*Result, error) {
	result, err := d.inner.AllowN(ctx, key, n)
	if err != nil {
		return nil, err
	}
	if result.Allowed {
		return result, nil
	}
	if d.opts.DryRunLogFunc != nil {
		d.opts.DryRunLogFunc(key, result)
	} else {
		log.Printf("[DRYRUN] would deny key=%s limit=%d remaining=%d retry_after=%v",
			key, result.Limit, result.Remaining, result.RetryAfter)
	}
	return &Result{
		Allowed:   true,
		Remaining: result.Remaining,
		Limit:     result.Limit,
		ResetAt:   result.ResetAt,
	}, nil
}

func (d *dryRunLimiter) Reset(ctx context.Context, key string) error {
	return d.inner.Reset(ctx, key)
}

// wrapDryRun returns a limiter that applies dry-run behavior when opts.DryRun is true.
func wrapDryRun(inner Limiter, opts *Options) Limiter {
	if opts != nil && opts.DryRun {
		return &dryRunLimiter{inner: inner, opts: opts}
	}
	return inner
}
