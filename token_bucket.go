package goratelimit

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewTokenBucket creates a Token Bucket rate limiter.
// capacity is the maximum number of tokens (burst size).
// refillRate is the number of tokens added per second.
// Pass WithRedis for distributed mode; omit for in-memory.
func NewTokenBucket(capacity, refillRate int64, opts ...Option) (Limiter, error) {
	if capacity <= 0 || refillRate <= 0 {
		return nil, validationErr("capacity and refillRate must be positive",
			"Use positive integers, e.g. NewTokenBucket(10, 5).")
	}
	o := applyOptions(opts)

	if o.RedisClient != nil {
		return wrapOptions(&tokenBucketRedis{
			redis:      o.RedisClient,
			capacity:   capacity,
			refillRate: refillRate,
			opts:       o,
		}, o), nil
	}
	return wrapOptions(&tokenBucketMemory{
		states:     make(map[string]*tokenBucketState),
		capacity:   capacity,
		refillRate: refillRate,
		opts:       o,
	}, o), nil
}

// ─── In-Memory ───────────────────────────────────────────────────────────────

type tokenBucketState struct {
	tokens     float64
	lastRefill time.Time
}

type tokenBucketMemory struct {
	mu         sync.Mutex
	states     map[string]*tokenBucketState
	capacity   int64
	refillRate int64
	opts       *Options
}

func (t *tokenBucketMemory) Allow(ctx context.Context, key string) (Result, error) {
	return t.AllowN(ctx, key, 1)
}

func (t *tokenBucketMemory) AllowN(ctx context.Context, key string, n int) (Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cap, unlimited := t.opts.resolveLimit(ctx, key, t.capacity)
	if unlimited {
		return Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}

	state, ok := t.states[key]
	if !ok {
		state = &tokenBucketState{
			tokens:     float64(cap),
			lastRefill: t.opts.now(),
		}
		t.states[key] = state
	}

	now := t.opts.now()
	elapsed := now.Sub(state.lastRefill).Seconds()
	state.tokens = math.Min(float64(cap), state.tokens+elapsed*float64(t.refillRate))
	state.lastRefill = now

	cost := float64(n)
	if state.tokens >= cost {
		state.tokens -= cost
		remaining := int64(math.Floor(state.tokens))
		return Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     cap,
		}, nil
	}

	deficit := cost - state.tokens
	retryAfter := time.Duration(math.Ceil(deficit/float64(t.refillRate)) * float64(time.Second))
	return Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      cap,
		RetryAfter: retryAfter,
	}, nil
}

func (t *tokenBucketMemory) Reset(ctx context.Context, key string) error {
	t.mu.Lock()
	delete(t.states, key)
	t.mu.Unlock()
	return nil
}

// ─── Redis ────────────────────────────────────────────────────────────────────

var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local max_tokens = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local cost = tonumber(ARGV[4])

local data = redis.call('HGETALL', key)
local tokens = max_tokens
local last_refill = now

if #data > 0 then
  local fields = {}
  for i = 1, #data, 2 do
    fields[data[i]] = data[i + 1]
  end
  tokens = tonumber(fields['tokens']) or max_tokens
  last_refill = tonumber(fields['last_refill']) or now
end

local elapsed = now - last_refill
tokens = math.min(max_tokens, tokens + elapsed * refill_rate)

local allowed = 0
local remaining = math.floor(tokens)
local retry_after = 0

if tokens >= cost then
  tokens = tokens - cost
  remaining = math.floor(tokens)
  allowed = 1
else
  local deficit = cost - tokens
  retry_after = math.ceil(deficit / refill_rate)
end

redis.call('HSET', key, 'tokens', tostring(tokens), 'last_refill', tostring(now))
redis.call('EXPIRE', key, math.ceil(max_tokens / refill_rate) + 1)

return { allowed, remaining, retry_after }
`)

type tokenBucketRedis struct {
	redis      redis.UniversalClient
	capacity   int64
	refillRate int64
	opts       *Options
}

func (t *tokenBucketRedis) Allow(ctx context.Context, key string) (Result, error) {
	return t.AllowN(ctx, key, 1)
}

func (t *tokenBucketRedis) AllowN(ctx context.Context, key string, n int) (Result, error) {
	cap, unlimited := t.opts.resolveLimit(ctx, key, t.capacity)
	if unlimited {
		return Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	fullKey := t.opts.FormatKey(key)
	now := float64(t.opts.now().UnixNano()) / 1e9

	result, err := tokenBucketScript.Run(ctx, t.redis, []string{fullKey},
		cap,
		t.refillRate,
		now,
		n,
	).Int64Slice()
	if err != nil {
		if t.opts.FailOpen {
			return Result{Allowed: true, Remaining: cap - 1, Limit: cap}, nil
		}
		return Result{Allowed: false, Remaining: 0, Limit: cap}, redisErr(err, t.opts)
	}

	allowed := result[0] == 1
	remaining := result[1]
	retryAfterSec := result[2]

	return Result{
		Allowed:    allowed,
		Remaining:  remaining,
		Limit:      cap,
		RetryAfter: time.Duration(retryAfterSec) * time.Second,
	}, nil
}

func (t *tokenBucketRedis) Reset(ctx context.Context, key string) error {
	fullKey := t.opts.FormatKey(key)
	return t.redis.Del(ctx, fullKey).Err()
}
