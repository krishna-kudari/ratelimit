package goratelimit

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewSlidingWindow creates a Sliding Window Log rate limiter.
// maxRequests is the maximum requests allowed per window.
// windowSeconds is the window duration in seconds.
// Note: this algorithm stores every request timestamp and has O(n) memory per key.
// For high-throughput keys, prefer NewSlidingWindowCounter.
// Pass WithRedis for distributed mode; omit for in-memory.
func NewSlidingWindow(maxRequests, windowSeconds int64, opts ...Option) (Limiter, error) {
	if maxRequests <= 0 || windowSeconds <= 0 {
		return nil, fmt.Errorf("goratelimit: maxRequests and windowSeconds must be positive")
	}
	o := applyOptions(opts)

	if o.RedisClient != nil {
		return &slidingWindowRedis{
			redis:         o.RedisClient,
			maxRequests:   maxRequests,
			windowSeconds: windowSeconds,
			opts:          o,
		}, nil
	}
	return &slidingWindowMemory{
		states:        make(map[string]*slidingWindowState),
		maxRequests:   maxRequests,
		windowSeconds: windowSeconds,
		opts:          o,
	}, nil
}

// ─── In-Memory ───────────────────────────────────────────────────────────────

type slidingWindowState struct {
	timestamps []time.Time
}

type slidingWindowMemory struct {
	mu            sync.Mutex
	states        map[string]*slidingWindowState
	maxRequests   int64
	windowSeconds int64
	opts          *Options
}

func (s *slidingWindowMemory) Allow(ctx context.Context, key string) (*Result, error) {
	return s.AllowN(ctx, key, 1)
}

func (s *slidingWindowMemory) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	maxReq, unlimited := s.opts.resolveLimit(ctx, key, s.maxRequests)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}

	state, ok := s.states[key]
	if !ok {
		state = &slidingWindowState{}
		s.states[key] = state
	}

	now := s.opts.now()
	windowDuration := time.Duration(s.windowSeconds) * time.Second

	// Evict expired timestamps
	cutoff := 0
	for cutoff < len(state.timestamps) && now.Sub(state.timestamps[cutoff]) > windowDuration {
		cutoff++
	}
	state.timestamps = state.timestamps[cutoff:]

	cost := int64(n)
	if int64(len(state.timestamps))+cost <= maxReq {
		for i := 0; i < n; i++ {
			state.timestamps = append(state.timestamps, now)
		}
		remaining := maxReq - int64(len(state.timestamps))
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     maxReq,
		}, nil
	}

	var retryAfter time.Duration
	if len(state.timestamps) > 0 {
		oldest := state.timestamps[0]
		expiresAt := oldest.Add(windowDuration)
		retryAfter = time.Until(expiresAt)
		if retryAfter < 0 {
			retryAfter = 0
		}
	}

	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      maxReq,
		RetryAfter: retryAfter,
	}, nil
}

func (s *slidingWindowMemory) Reset(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.states, key)
	s.mu.Unlock()
	return nil
}

// ─── Redis ────────────────────────────────────────────────────────────────────

type slidingWindowRedis struct {
	redis         redis.UniversalClient
	maxRequests   int64
	windowSeconds int64
	opts          *Options
}

func (s *slidingWindowRedis) Allow(ctx context.Context, key string) (*Result, error) {
	return s.AllowN(ctx, key, 1)
}

func (s *slidingWindowRedis) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	maxReq, unlimited := s.opts.resolveLimit(ctx, key, s.maxRequests)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	fullKey := s.opts.FormatKey(key)
	now := s.opts.now().UnixMilli()
	windowStart := now - s.windowSeconds*1000

	// Remove expired entries
	err := s.redis.ZRemRangeByScore(ctx, fullKey, "0", fmt.Sprintf("%d", windowStart)).Err()
	if err != nil {
		return s.failResult(err, maxReq)
	}

	count, err := s.redis.ZCard(ctx, fullKey).Result()
	if err != nil {
		return s.failResult(err, maxReq)
	}

	cost := int64(n)
	if count+cost <= maxReq {
		pipe := s.redis.Pipeline()
		for i := 0; i < n; i++ {
			member := fmt.Sprintf("%d:%d", now, rand.Int63())
			pipe.ZAdd(ctx, fullKey, redis.Z{Score: float64(now), Member: member})
		}
		pipe.Expire(ctx, fullKey, time.Duration(s.windowSeconds)*time.Second)
		if _, err := pipe.Exec(ctx); err != nil {
			return s.failResult(err, maxReq)
		}
		remaining := maxReq - count - cost
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     maxReq,
		}, nil
	}

	// Denied — compute retryAfter from oldest entry
	retryAfter := time.Duration(s.windowSeconds) * time.Second
	oldest, err := s.redis.ZRangeWithScores(ctx, fullKey, 0, 0).Result()
	if err == nil && len(oldest) > 0 {
		oldestMs := int64(oldest[0].Score)
		expiresAt := oldestMs + s.windowSeconds*1000
		retryMs := expiresAt - now
		if retryMs > 0 && retryMs <= s.windowSeconds*1000 {
			retryAfter = time.Duration(retryMs) * time.Millisecond
		}
	}

	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      maxReq,
		RetryAfter: retryAfter,
	}, nil
}

func (s *slidingWindowRedis) Reset(ctx context.Context, key string) error {
	fullKey := s.opts.FormatKey(key)
	return s.redis.Del(ctx, fullKey).Err()
}

func (s *slidingWindowRedis) failResult(err error, limit int64) (*Result, error) {
	if s.opts.FailOpen {
		return &Result{Allowed: true, Remaining: limit - 1, Limit: limit}, nil
	}
	return &Result{Allowed: false, Remaining: 0, Limit: limit}, fmt.Errorf("goratelimit: redis error: %w", err)
}
