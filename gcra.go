package goratelimit

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewGCRA creates a GCRA (Generic Cell Rate Algorithm) rate limiter.
// rate is the sustained request rate per second. burst is the maximum burst size.
// Pass WithRedis for distributed mode; omit for in-memory.
func NewGCRA(rate, burst int64, opts ...Option) (Limiter, error) {
	if rate <= 0 || burst <= 0 {
		return nil, fmt.Errorf("goratelimit: rate and burst must be positive")
	}
	o := applyOptions(opts)
	emissionInterval := 1.0 / float64(rate)
	burstAllowance := float64(burst-1) * emissionInterval

	if o.RedisClient != nil {
		return wrapDryRun(&gcraRedis{
			redis:            o.RedisClient,
			emissionInterval: emissionInterval,
			burstAllowance:   burstAllowance,
			burst:            burst,
			opts:             o,
		}, o), nil
	}
	return wrapDryRun(&gcraMemory{
		states:           make(map[string]*gcraState),
		emissionInterval: emissionInterval,
		burstAllowance:   burstAllowance,
		burst:            burst,
		opts:             o,
	}, o), nil
}

// ─── In-Memory ───────────────────────────────────────────────────────────────

type gcraState struct {
	tat float64
}

type gcraMemory struct {
	mu               sync.Mutex
	states           map[string]*gcraState
	emissionInterval float64
	burstAllowance   float64
	burst            int64
	opts             *Options
}

func (g *gcraMemory) Allow(ctx context.Context, key string) (*Result, error) {
	return g.AllowN(ctx, key, 1)
}

func (g *gcraMemory) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	burst, unlimited := g.opts.resolveLimit(ctx, key, g.burst)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	burstAllowance := float64(burst-1) * g.emissionInterval

	state, ok := g.states[key]
	if !ok {
		state = &gcraState{}
		g.states[key] = state
	}

	now := float64(g.opts.now().UnixNano()) / 1e9
	tat := math.Max(state.tat, now)
	increment := g.emissionInterval * float64(n)
	newTAT := tat + increment
	diff := newTAT - now

	if diff <= burstAllowance+g.emissionInterval {
		state.tat = newTAT
		remaining := int64(math.Floor((burstAllowance - diff + g.emissionInterval) / g.emissionInterval))
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     burst,
		}, nil
	}

	retryAfter := time.Duration(math.Ceil(diff-burstAllowance) * float64(time.Second))
	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      burst,
		RetryAfter: retryAfter,
	}, nil
}

func (g *gcraMemory) Reset(ctx context.Context, key string) error {
	g.mu.Lock()
	delete(g.states, key)
	g.mu.Unlock()
	return nil
}

// ─── Redis ────────────────────────────────────────────────────────────────────

var gcraScript = redis.NewScript(`
local key = KEYS[1]
local emission_interval = tonumber(ARGV[1])
local burst_allowance = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local increment = tonumber(ARGV[4])

local tat = tonumber(redis.call('GET', key)) or now
tat = math.max(tat, now)

local new_tat = tat + increment
local diff = new_tat - now

if diff <= burst_allowance + emission_interval then
    redis.call('SET', key, tostring(new_tat))
    redis.call('EXPIRE', key, math.ceil(burst_allowance + emission_interval) + 1)
    local remaining = math.floor((burst_allowance - diff + emission_interval) / emission_interval)
    return { 1, remaining, 0 }
else
    local retry_after = math.ceil(diff - burst_allowance)
    return { 0, 0, retry_after }
end
`)

type gcraRedis struct {
	redis            redis.UniversalClient
	emissionInterval float64
	burstAllowance   float64
	burst            int64
	opts             *Options
}

func (g *gcraRedis) Allow(ctx context.Context, key string) (*Result, error) {
	return g.AllowN(ctx, key, 1)
}

func (g *gcraRedis) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	burst, unlimited := g.opts.resolveLimit(ctx, key, g.burst)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	fullKey := g.opts.FormatKey(key)
	burstAllowance := float64(burst-1) * g.emissionInterval
	now := float64(g.opts.now().UnixNano()) / 1e9
	increment := g.emissionInterval * float64(n)

	result, err := gcraScript.Run(ctx, g.redis, []string{fullKey},
		g.emissionInterval,
		burstAllowance,
		now,
		increment,
	).Int64Slice()
	if err != nil {
		if g.opts.FailOpen {
			return &Result{Allowed: true, Remaining: burst - 1, Limit: burst}, nil
		}
		return &Result{Allowed: false, Remaining: 0, Limit: burst}, fmt.Errorf("goratelimit: redis error: %w", err)
	}

	allowed := result[0] == 1
	remaining := result[1]
	retryAfterSec := result[2]

	return &Result{
		Allowed:    allowed,
		Remaining:  remaining,
		Limit:      burst,
		RetryAfter: time.Duration(retryAfterSec) * time.Second,
	}, nil
}

func (g *gcraRedis) Reset(ctx context.Context, key string) error {
	fullKey := g.opts.FormatKey(key)
	return g.redis.Del(ctx, fullKey).Err()
}
