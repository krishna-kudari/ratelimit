package goratelimit

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// LeakyBucketMode defines the operating mode of a leaky bucket limiter.
type LeakyBucketMode string

const (
	// Policing mode drops requests that exceed capacity (hard rejection).
	Policing LeakyBucketMode = "policing"
	// Shaping mode queues requests and assigns a processing delay.
	Shaping LeakyBucketMode = "shaping"
)

// LeakyBucketResult extends Result with shaping-specific delay information.
type LeakyBucketResult struct {
	*Result
	Delay time.Duration // For shaping mode: how long to wait before processing.
}

// NewLeakyBucket creates a Leaky Bucket rate limiter.
// capacity is the bucket size. leakRate is tokens leaked per second.
// mode selects Policing (hard reject) or Shaping (queue with delay).
// Pass WithRedis for distributed mode; omit for in-memory.
func NewLeakyBucket(capacity, leakRate int64, mode LeakyBucketMode, opts ...Option) (Limiter, error) {
	if capacity <= 0 || leakRate <= 0 {
		return nil, fmt.Errorf("goratelimit: capacity and leakRate must be positive")
	}
	o := applyOptions(opts)

	if o.RedisClient != nil {
		return &leakyBucketRedis{
			redis:    o.RedisClient,
			capacity: capacity,
			leakRate: leakRate,
			mode:     mode,
			opts:     o,
		}, nil
	}
	return &leakyBucketMemory{
		states:   make(map[string]*leakyBucketState),
		capacity: float64(capacity),
		leakRate: float64(leakRate),
		limit:    capacity,
		mode:     mode,
		opts:     o,
	}, nil
}

// ─── In-Memory ───────────────────────────────────────────────────────────────

type leakyBucketState struct {
	// policing
	level    float64
	lastLeak time.Time
	// shaping
	nextFree time.Time
}

type leakyBucketMemory struct {
	mu       sync.Mutex
	states   map[string]*leakyBucketState
	capacity float64
	leakRate float64
	limit    int64
	mode     LeakyBucketMode
	opts     *Options
}

func (l *leakyBucketMemory) getState(key string) *leakyBucketState {
	state, ok := l.states[key]
	if !ok {
		now := l.opts.now()
		state = &leakyBucketState{lastLeak: now, nextFree: now}
		l.states[key] = state
	}
	return state
}

func (l *leakyBucketMemory) Allow(ctx context.Context, key string) (*Result, error) {
	return l.AllowN(ctx, key, 1)
}

func (l *leakyBucketMemory) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	limit, unlimited := l.opts.resolveLimit(ctx, key, l.limit)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	cap := float64(limit)

	if l.mode == Shaping {
		return l.allowShaping(key, n, cap)
	}
	return l.allowPolicing(key, n, cap)
}

func (l *leakyBucketMemory) allowPolicing(key string, n int, cap float64) (*Result, error) {
	state := l.getState(key)
	limit := int64(cap)
	now := l.opts.now()

	elapsed := now.Sub(state.lastLeak).Seconds()
	leaked := elapsed * l.leakRate
	state.level = math.Max(0, state.level-leaked)
	state.lastLeak = now

	cost := float64(n)
	if state.level+cost <= cap {
		state.level += cost
		remaining := int64(math.Max(0, math.Floor(cap-state.level)))
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     limit,
		}, nil
	}

	retryAfter := time.Duration(math.Ceil(cost/l.leakRate) * float64(time.Second))
	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      limit,
		RetryAfter: retryAfter,
	}, nil
}

func (l *leakyBucketMemory) allowShaping(key string, n int, cap float64) (*Result, error) {
	state := l.getState(key)
	limit := int64(cap)
	now := l.opts.now()

	if state.nextFree.Before(now) {
		state.nextFree = now
	}

	delayDuration := state.nextFree.Sub(now).Seconds()
	queueDepth := delayDuration * l.leakRate
	cost := float64(n)

	if queueDepth+cost <= cap {
		delay := time.Duration(delayDuration * float64(time.Second))
		state.nextFree = state.nextFree.Add(time.Duration(cost / l.leakRate * float64(time.Second)))
		queueDepth += cost
		remaining := int64(math.Max(0, math.Floor(cap-queueDepth)))
		return &Result{
			Allowed:    true,
			Remaining:  remaining,
			Limit:      limit,
			RetryAfter: delay,
		}, nil
	}

	return &Result{
		Allowed:   false,
		Remaining: 0,
		Limit:     limit,
	}, nil
}

func (l *leakyBucketMemory) Reset(ctx context.Context, key string) error {
	l.mu.Lock()
	delete(l.states, key)
	l.mu.Unlock()
	return nil
}

// ─── Redis ────────────────────────────────────────────────────────────────────

var luaPolicing = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local leak_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local cost = tonumber(ARGV[4])

local data = redis.call('HGETALL', key)
local level = 0
local last_leak = now

if #data > 0 then
  local fields = {}
  for i = 1, #data, 2 do
    fields[data[i]] = data[i + 1]
  end
  level = tonumber(fields['level']) or 0
  last_leak = tonumber(fields['last_leak']) or now
end

local elapsed = now - last_leak
local leaked = elapsed * leak_rate
level = math.max(0, level - leaked)

local allowed = 0
local remaining = math.max(0, math.floor(capacity - level))
local retry_after = 0

if level + cost <= capacity then
  level = level + cost
  remaining = math.max(0, math.floor(capacity - level))
  allowed = 1
else
  retry_after = math.ceil(cost / leak_rate)
end

redis.call('HSET', key, 'level', tostring(level), 'last_leak', tostring(now))
redis.call('EXPIRE', key, math.ceil(capacity / leak_rate) + 1)

return { allowed, remaining, retry_after }
`)

var luaShaping = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local leak_rate = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local cost = tonumber(ARGV[4])

local data = redis.call('HGETALL', key)
local next_free = now

if #data > 0 then
  local fields = {}
  for i = 1, #data, 2 do
    fields[data[i]] = data[i + 1]
  end
  next_free = tonumber(fields['next_free']) or now
end

if next_free < now then
  next_free = now
end

local delay = next_free - now
local queue_depth = delay * leak_rate

local allowed = 0
local remaining = math.max(0, math.floor(capacity - queue_depth))
local delay_ms = 0

if queue_depth + cost <= capacity then
  delay_ms = math.floor(delay * 1000)
  next_free = next_free + (cost / leak_rate)
  allowed = 1
  queue_depth = queue_depth + cost
  remaining = math.max(0, math.floor(capacity - queue_depth))
end

redis.call('HSET', key, 'next_free', tostring(next_free))
redis.call('EXPIRE', key, math.ceil(capacity / leak_rate) + 1)

return { allowed, remaining, delay_ms }
`)

type leakyBucketRedis struct {
	redis    redis.UniversalClient
	capacity int64
	leakRate int64
	mode     LeakyBucketMode
	opts     *Options
}

func (l *leakyBucketRedis) Allow(ctx context.Context, key string) (*Result, error) {
	return l.AllowN(ctx, key, 1)
}

func (l *leakyBucketRedis) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	cap, unlimited := l.opts.resolveLimit(ctx, key, l.capacity)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	fullKey := l.opts.FormatKey(key)
	now := float64(l.opts.now().UnixNano()) / 1e9

	script := luaPolicing
	if l.mode == Shaping {
		script = luaShaping
	}

	result, err := script.Run(ctx, l.redis, []string{fullKey},
		cap,
		l.leakRate,
		now,
		n,
	).Int64Slice()
	if err != nil {
		if l.opts.FailOpen {
			return &Result{Allowed: true, Remaining: cap - 1, Limit: cap}, nil
		}
		return &Result{Allowed: false, Remaining: 0, Limit: cap}, fmt.Errorf("goratelimit: redis error: %w", err)
	}

	allowed := result[0] == 1
	remaining := result[1]

	r := &Result{
		Allowed:   allowed,
		Remaining: remaining,
		Limit:     cap,
	}

	if l.mode == Policing && !allowed {
		retryAfterSec := result[2]
		r.RetryAfter = time.Duration(retryAfterSec) * time.Second
	}
	if l.mode == Shaping && allowed {
		delayMs := result[2]
		r.RetryAfter = time.Duration(delayMs) * time.Millisecond
	}

	return r, nil
}

func (l *leakyBucketRedis) Reset(ctx context.Context, key string) error {
	fullKey := l.opts.FormatKey(key)
	return l.redis.Del(ctx, fullKey).Err()
}
