package goratelimit

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewSlidingWindowCounter creates a Sliding Window Counter rate limiter.
// This uses the weighted-counter approximation (~1% error) with O(1) memory per key.
// maxRequests is the maximum requests allowed per window.
// windowSeconds is the window duration in seconds.
// Pass WithRedis for distributed mode; omit for in-memory.
func NewSlidingWindowCounter(maxRequests, windowSeconds int64, opts ...Option) (Limiter, error) {
	if maxRequests <= 0 || windowSeconds <= 0 {
		return nil, fmt.Errorf("goratelimit: maxRequests and windowSeconds must be positive")
	}
	o := applyOptions(opts)

	if o.RedisClient != nil {
		return wrapDryRun(&slidingWindowCounterRedis{
			redis:         o.RedisClient,
			maxRequests:   maxRequests,
			windowSeconds: windowSeconds,
			opts:          o,
		}, o), nil
	}
	return wrapDryRun(&slidingWindowCounterMemory{
		states:        make(map[string]*slidingWindowCounterState),
		maxRequests:   maxRequests,
		windowSeconds: windowSeconds,
		opts:          o,
	}, o), nil
}

// ─── In-Memory ───────────────────────────────────────────────────────────────

type slidingWindowCounterState struct {
	windowStart   time.Time
	previousCount int64
	currentCount  int64
}

type slidingWindowCounterMemory struct {
	mu            sync.Mutex
	states        map[string]*slidingWindowCounterState
	maxRequests   int64
	windowSeconds int64
	opts          *Options
}

func (s *slidingWindowCounterMemory) Allow(ctx context.Context, key string) (*Result, error) {
	return s.AllowN(ctx, key, 1)
}

func (s *slidingWindowCounterMemory) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	maxReq, unlimited := s.opts.resolveLimit(ctx, key, s.maxRequests)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}

	state, ok := s.states[key]
	if !ok {
		state = &slidingWindowCounterState{windowStart: s.opts.now()}
		s.states[key] = state
	}

	now := s.opts.now()
	windowDuration := time.Duration(s.windowSeconds) * time.Second

	for now.Sub(state.windowStart) >= windowDuration {
		state.previousCount = state.currentCount
		state.currentCount = 0
		state.windowStart = state.windowStart.Add(windowDuration)
	}

	elapsedFraction := now.Sub(state.windowStart).Seconds() / float64(s.windowSeconds)
	prevWeight := float64(state.previousCount) * (1 - elapsedFraction)
	estimatedCount := prevWeight + float64(state.currentCount)

	cost := float64(n)
	if estimatedCount+cost <= float64(maxReq) {
		state.currentCount += int64(n)
		newEstimate := prevWeight + float64(state.currentCount)
		remaining := int64(math.Max(0, math.Floor(float64(maxReq)-newEstimate)))
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     maxReq,
		}, nil
	}

	retryAfter := time.Duration(math.Ceil(float64(s.windowSeconds)*(1-elapsedFraction))) * time.Second
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      maxReq,
		RetryAfter: retryAfter,
	}, nil
}

func (s *slidingWindowCounterMemory) Reset(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.states, key)
	s.mu.Unlock()
	return nil
}

// ─── Redis ────────────────────────────────────────────────────────────────────

type slidingWindowCounterRedis struct {
	redis         redis.UniversalClient
	maxRequests   int64
	windowSeconds int64
	opts          *Options
}

func (s *slidingWindowCounterRedis) Allow(ctx context.Context, key string) (*Result, error) {
	return s.AllowN(ctx, key, 1)
}

func (s *slidingWindowCounterRedis) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	maxReq, unlimited := s.opts.resolveLimit(ctx, key, s.maxRequests)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	now := s.opts.now().Unix()
	currentWindow := now / s.windowSeconds
	previousWindow := currentWindow - 1
	elapsed := float64(now%s.windowSeconds) / float64(s.windowSeconds)

	currentKey := s.opts.FormatKeySuffix(key, fmt.Sprintf("%d", currentWindow))
	previousKey := s.opts.FormatKeySuffix(key, fmt.Sprintf("%d", previousWindow))

	prevStr, err := s.redis.Get(ctx, previousKey).Result()
	if err != nil && err != redis.Nil {
		return s.failResult(err, maxReq)
	}
	prevCount, _ := strconv.ParseFloat(prevStr, 64)
	weightedPrev := prevCount * (1 - elapsed)

	currStr, err := s.redis.Get(ctx, currentKey).Result()
	if err != nil && err != redis.Nil {
		return s.failResult(err, maxReq)
	}
	currentCount, _ := strconv.ParseFloat(currStr, 64)

	estimatedCount := weightedPrev + currentCount
	cost := float64(n)

	if estimatedCount+cost > float64(maxReq) {
		retryAfter := int64(math.Ceil(float64(s.windowSeconds) * (1 - elapsed)))
		if retryAfter < 1 {
			retryAfter = 1
		}
		if retryAfter > s.windowSeconds {
			retryAfter = s.windowSeconds
		}
		return &Result{
			Allowed:    false,
			Remaining:  0,
			Limit:      maxReq,
			RetryAfter: time.Duration(retryAfter) * time.Second,
		}, nil
	}

	newCount, err := s.redis.IncrBy(ctx, currentKey, int64(n)).Result()
	if err != nil {
		return s.failResult(err, maxReq)
	}
	if newCount == int64(n) {
		s.redis.Expire(ctx, currentKey, time.Duration(s.windowSeconds*2)*time.Second)
	}

	newEstimate := weightedPrev + float64(newCount)
	remaining := int64(math.Max(0, math.Floor(float64(maxReq)-newEstimate)))

	return &Result{
		Allowed:   true,
		Remaining: remaining,
		Limit:     maxReq,
	}, nil
}

func (s *slidingWindowCounterRedis) Reset(ctx context.Context, key string) error {
	now := s.opts.now().Unix()
	currentWindow := now / s.windowSeconds
	previousWindow := currentWindow - 1
	currentKey := s.opts.FormatKeySuffix(key, fmt.Sprintf("%d", currentWindow))
	previousKey := s.opts.FormatKeySuffix(key, fmt.Sprintf("%d", previousWindow))
	return s.redis.Del(ctx, currentKey, previousKey).Err()
}

func (s *slidingWindowCounterRedis) failResult(err error, limit int64) (*Result, error) {
	if s.opts.FailOpen {
		return &Result{Allowed: true, Remaining: limit - 1, Limit: limit}, nil
	}
	return &Result{Allowed: false, Remaining: 0, Limit: limit}, fmt.Errorf("goratelimit: redis error: %w", err)
}
