package goratelimit

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// ─── Hash Function ────────────────────────────────────────────────────────────

// cmsHash computes an FNV-inspired hash with a per-row seed so each
// row of the sketch behaves as an independent hash function.
func cmsHash(key string, seed uint32, width uint32) uint32 {
	h := seed
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return h % width
}

// ─── Count-Min Sketch ─────────────────────────────────────────────────────────

type countMinSketch struct {
	grid  [][]int64
	depth int
	width int
	seeds []uint32
}

func newCountMinSketch(width, depth int) *countMinSketch {
	grid := make([][]int64, depth)
	seeds := make([]uint32, depth)
	for i := range grid {
		grid[i] = make([]int64, width)
		seeds[i] = uint32(i*2654435761 + 1)
	}
	return &countMinSketch{grid: grid, depth: depth, width: width, seeds: seeds}
}

func (c *countMinSketch) incrementBy(key string, n int64) {
	w := uint32(c.width)
	for i := 0; i < c.depth; i++ {
		col := cmsHash(key, c.seeds[i], w)
		c.grid[i][col] += n
	}
}

func (c *countMinSketch) count(key string) int64 {
	min := int64(math.MaxInt64)
	w := uint32(c.width)
	for i := 0; i < c.depth; i++ {
		col := cmsHash(key, c.seeds[i], w)
		if c.grid[i][col] < min {
			min = c.grid[i][col]
		}
	}
	return min
}

// ─── CMS Rate Limiter ─────────────────────────────────────────────────────────

type cmsLimiter struct {
	mu            sync.Mutex
	current       *countMinSketch
	previous      *countMinSketch
	windowSeconds int64
	windowStart   time.Time
	limit         int64
	width         int
	depth         int
	opts          *Options
}

// NewCMS creates a Count-Min Sketch rate limiter that uses fixed memory
// regardless of the number of unique keys. It approximates per-key counts
// using two rotating sketches for a sliding window effect.
//
// This is an in-memory-only algorithm — no Redis backend is supported.
// Its primary use case is as a fast local pre-filter in front of a precise
// distributed limiter (see [NewPreFilter]).
//
//	limit         — max requests per window
//	windowSeconds — window size in seconds
//	epsilon       — acceptable error rate (e.g. 0.01 = 1 %)
//	delta         — failure probability  (e.g. 0.001 = 0.1 %)
//
// Memory: 2 × ⌈e/ε⌉ × ⌈ln(1/δ)⌉ × 8 bytes.
// Example: ε=0.01, δ=0.001 → ~30 KB fixed.
func NewCMS(limit, windowSeconds int64, epsilon, delta float64, opts ...Option) (Limiter, error) {
	if limit <= 0 || windowSeconds <= 0 {
		return nil, fmt.Errorf("goratelimit: limit and windowSeconds must be positive")
	}
	if epsilon <= 0 || epsilon >= 1 {
		return nil, fmt.Errorf("goratelimit: epsilon must be in (0, 1)")
	}
	if delta <= 0 || delta >= 1 {
		return nil, fmt.Errorf("goratelimit: delta must be in (0, 1)")
	}

	o := applyOptions(opts)
	width := int(math.Ceil(math.E / epsilon))
	depth := int(math.Ceil(math.Log(1 / delta)))

	return wrapDryRun(&cmsLimiter{
		current:       newCountMinSketch(width, depth),
		previous:      newCountMinSketch(width, depth),
		windowSeconds: windowSeconds,
		windowStart:   o.now(),
		limit:         limit,
		width:         width,
		depth:         depth,
		opts:          o,
	}, o), nil
}

// CMSMemoryBytes returns the approximate heap usage of a CMS limiter
// created with the given error parameters. Useful for capacity planning
// without constructing a limiter.
func CMSMemoryBytes(epsilon, delta float64) int {
	width := int(math.Ceil(math.E / epsilon))
	depth := int(math.Ceil(math.Log(1 / delta)))
	return 2 * width * depth * 8
}

func (r *cmsLimiter) Allow(ctx context.Context, key string) (*Result, error) {
	return r.AllowN(ctx, key, 1)
}

func (r *cmsLimiter) AllowN(ctx context.Context, key string, n int) (*Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	limit, unlimited := r.opts.resolveLimit(ctx, key, r.limit)
	if unlimited {
		return &Result{Allowed: true, Remaining: Unlimited, Limit: Unlimited}, nil
	}
	now := r.opts.now()
	windowDuration := time.Duration(r.windowSeconds) * time.Second

	// Rotate sketches when the current window expires.
	if now.Sub(r.windowStart) >= windowDuration {
		r.previous = r.current
		r.current = newCountMinSketch(r.width, r.depth)
		r.windowStart = r.windowStart.Add(windowDuration)

		// More than one full window elapsed — previous is also stale.
		if now.Sub(r.windowStart) >= windowDuration {
			r.previous = newCountMinSketch(r.width, r.depth)
			r.windowStart = now
		}
	}

	elapsedFraction := now.Sub(r.windowStart).Seconds() / float64(r.windowSeconds)

	// Weighted count across both sketches (sliding window counter approach).
	prevCount := float64(r.previous.count(key)) * (1 - elapsedFraction)
	currCount := float64(r.current.count(key))
	estimated := prevCount + currCount
	cost := float64(n)

	if estimated+cost <= float64(limit) {
		r.current.incrementBy(key, int64(n))
		newEstimate := prevCount + float64(r.current.count(key))
		remaining := int64(math.Max(0, math.Floor(float64(limit)-newEstimate)))
		return &Result{
			Allowed:   true,
			Remaining: remaining,
			Limit:     limit,
		}, nil
	}

	retryAfter := time.Duration(math.Ceil(float64(r.windowSeconds)*(1-elapsedFraction))) * time.Second
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return &Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      limit,
		RetryAfter: retryAfter,
	}, nil
}

// Reset is a no-op for CMS. Probabilistic sketches do not support per-key
// removal without affecting other keys. The sliding window naturally ages
// out stale counts.
func (r *cmsLimiter) Reset(_ context.Context, _ string) error {
	return nil
}
