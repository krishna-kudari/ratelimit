package goratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewFixedWindow creates a Fixed Window rate limiter.
// maxRequests is the maximum requests allowed per window.
// windowSeconds is the window duration in seconds.
// Pass WithRedis for distributed mode; omit for in-memory.
func NewFixedWindow(maxRequests, windowSeconds int64, opts ...Option) (Limiter, error) {
	if maxRequests <= 0 || windowSeconds <= 0 {
		return nil, fmt.Errorf("goratelimit: maxRequests and windowSeconds must be positive")
	}
	o := applyOptions(opts)

	if o.RedisClient != nil {
		return wrapDryRun(&fixedWindowRedis{
			redis:         o.RedisClient,
			maxRequests:   maxRequests,
			windowSeconds: windowSeconds,
			opts:          o,
		}, o), nil
	}
	return wrapDryRun(&fixedWindowMemory{
		states:        make(map[string]*fixedWindowState),
		maxRequests:   maxRequests,
		windowSeconds: windowSeconds,
		opts:          o,
	}, o), nil
}

// ─── In-Memory ───────────────────────────────────────────────────────────────

type fixedWindowState struct {
	requests    int64
	windowStart time.Time
}

type fixedWindowMemory struct {
	mu            sync.Mutex
	states        map[string]*fixedWindowState
	maxRequests   int64
	windowSeconds int64
	opts          *Options
}

func (f *fixedWindowMemory) Allow(ctx context.Context, key string) (*Result, error) {
	return f.AllowN(ctx, key, 1)
}

func (f *fixedWindowMemory) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	maxReq, unlimited := f.opts.resolveLimit(ctx, key, f.maxRequests)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}

	state, ok := f.states[key]
	if !ok {
		state = &fixedWindowState{windowStart: f.opts.now()}
		f.states[key] = state
	}

	now := f.opts.now()
	windowDuration := time.Duration(f.windowSeconds) * time.Second
	if now.Sub(state.windowStart) >= windowDuration {
		state.windowStart = now
		state.requests = 0
	}

	cost := int64(n)
	if state.requests+cost <= maxReq {
		state.requests += cost
		remaining := maxReq - state.requests
		resetAt := state.windowStart.Add(windowDuration)
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     maxReq,
			ResetAt:   resetAt,
		}, nil
	}

	resetAt := state.windowStart.Add(windowDuration)
	retryAfter := resetAt.Sub(now)
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      maxReq,
		ResetAt:    resetAt,
		RetryAfter: retryAfter,
	}, nil
}

func (f *fixedWindowMemory) Reset(ctx context.Context, key string) error {
	f.mu.Lock()
	delete(f.states, key)
	f.mu.Unlock()
	return nil
}

// ─── Redis ────────────────────────────────────────────────────────────────────

var fixedWindowScript = redis.NewScript(`
local key = KEYS[1]
local max_requests = tonumber(ARGV[1])
local window_seconds = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])

local count = redis.call('GET', key)
if not count then
  count = 0
else
  count = tonumber(count)
end

if count + cost <= max_requests then
  local new_count = redis.call('INCRBY', key, cost)
  if new_count == cost and count == 0 then
    redis.call('EXPIRE', key, window_seconds)
  end
  local remaining = max_requests - new_count
  local ttl = redis.call('TTL', key)
  return { 1, remaining, ttl }
end

local ttl = redis.call('TTL', key)
if ttl < 0 then
  ttl = window_seconds
end
return { 0, 0, ttl }
`)

type fixedWindowRedis struct {
	redis         redis.UniversalClient
	maxRequests   int64
	windowSeconds int64
	opts          *Options
}

func (f *fixedWindowRedis) Allow(ctx context.Context, key string) (*Result, error) {
	return f.AllowN(ctx, key, 1)
}

func (f *fixedWindowRedis) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	maxReq, unlimited := f.opts.resolveLimit(ctx, key, f.maxRequests)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	fullKey := f.opts.FormatKey(key)
	result, err := fixedWindowScript.Run(ctx, f.redis, []string{fullKey},
		maxReq,
		f.windowSeconds,
		n,
	).Int64Slice()
	if err != nil {
		if f.opts.FailOpen {
			return &Result{Allowed: true, Remaining: maxReq - 1, Limit: maxReq}, nil
		}
		return &Result{Allowed: false, Remaining: 0, Limit: maxReq}, fmt.Errorf("goratelimit: redis error: %w", err)
	}

	allowed := result[0] == 1
	remaining := result[1]
	ttlSec := result[2]

	resetAt := f.opts.now().Add(time.Duration(ttlSec) * time.Second)
	var retryAfter time.Duration
	if !allowed {
		retryAfter = time.Duration(ttlSec) * time.Second
	}

	return &Result{
		Allowed:    allowed,
		Remaining:  remaining,
		Limit:      maxReq,
		ResetAt:    resetAt,
		RetryAfter: retryAfter,
	}, nil
}

func (f *fixedWindowRedis) Reset(ctx context.Context, key string) error {
	fullKey := f.opts.FormatKey(key)
	return f.redis.Del(ctx, fullKey).Err()
}
