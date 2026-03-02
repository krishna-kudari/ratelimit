// All seven algorithms — direct usage, AllowN, Reset, PreFilter, and Builder API.
// Run: go run ./examples/basic/
package main

import (
	"context"
	"fmt"
	"time"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

func main() {
	ctx := context.Background()

	// ── Fixed Window ────────────────────────────────────────────
	fmt.Println("=== Fixed Window (max=3, window=5s) ===")
	fw, _ := goratelimit.NewFixedWindow(3, 5)
	for i := 1; i <= 5; i++ {
		r, _ := fw.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── Sliding Window Log ──────────────────────────────────────
	fmt.Println("\n=== Sliding Window Log (max=3, window=5s) ===")
	sw, _ := goratelimit.NewSlidingWindow(3, 5)
	for i := 1; i <= 5; i++ {
		r, _ := sw.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── Sliding Window Counter ──────────────────────────────────
	fmt.Println("\n=== Sliding Window Counter (max=3, window=5s) ===")
	swc, _ := goratelimit.NewSlidingWindowCounter(3, 5)
	for i := 1; i <= 5; i++ {
		r, _ := swc.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── Token Bucket ────────────────────────────────────────────
	fmt.Println("\n=== Token Bucket (capacity=5, refill=2/s) ===")
	tb, _ := goratelimit.NewTokenBucket(5, 2)
	for i := 1; i <= 8; i++ {
		r, _ := tb.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── Leaky Bucket — Policing ─────────────────────────────────
	fmt.Println("\n=== Leaky Bucket — Policing (capacity=3, leak=1/s) ===")
	lbp, _ := goratelimit.NewLeakyBucket(3, 1, goratelimit.Policing)
	for i := 1; i <= 5; i++ {
		r, _ := lbp.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── Leaky Bucket — Shaping ──────────────────────────────────
	fmt.Println("\n=== Leaky Bucket — Shaping (capacity=3, leak=1/s) ===")
	lbs, _ := goratelimit.NewLeakyBucket(3, 1, goratelimit.Shaping)
	for i := 1; i <= 5; i++ {
		r, _ := lbs.Allow(ctx, "user:1")
		status := "allowed"
		if !r.Allowed {
			status = "denied"
		} else if r.RetryAfter > 0 {
			status = fmt.Sprintf("queued +%v", r.RetryAfter)
		}
		fmt.Printf("  req %d: %s  remaining=%d\n", i, status, r.Remaining)
	}

	// ── GCRA ────────────────────────────────────────────────────
	fmt.Println("\n=== GCRA (rate=2/s, burst=4) ===")
	gcra, _ := goratelimit.NewGCRA(2, 4)
	for i := 1; i <= 6; i++ {
		r, _ := gcra.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── Count-Min Sketch ───────────────────────────────────────
	fmt.Println("\n=== Count-Min Sketch (limit=5, window=10s, ε=0.01, δ=0.001) ===")
	cms, _ := goratelimit.NewCMS(5, 10, 0.01, 0.001)
	for i := 1; i <= 7; i++ {
		r, _ := cms.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}
	fmt.Printf("  memory: %d bytes (fixed)\n", goratelimit.CMSMemoryBytes(0.01, 0.001))

	// ── PreFilter (CMS + GCRA) ─────────────────────────────────
	fmt.Println("\n=== PreFilter — CMS guards GCRA ===")
	local, _ := goratelimit.NewCMS(10, 60, 0.01, 0.001)
	precise, _ := goratelimit.NewGCRA(5, 5)
	pf := goratelimit.NewPreFilter(local, precise)
	for i := 1; i <= 7; i++ {
		r, _ := pf.Allow(ctx, "user:1")
		fmt.Printf("  req %d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	// ── AllowN (batch) ──────────────────────────────────────────
	fmt.Println("\n=== AllowN — consume 3 tokens at once ===")
	tb2, _ := goratelimit.NewTokenBucket(10, 1)
	r, _ := tb2.AllowN(ctx, "user:1", 3)
	fmt.Printf("  AllowN(3): allowed=%-5v remaining=%d\n", r.Allowed, r.Remaining)
	r, _ = tb2.AllowN(ctx, "user:1", 8)
	fmt.Printf("  AllowN(8): allowed=%-5v remaining=%d\n", r.Allowed, r.Remaining)

	// ── Reset ───────────────────────────────────────────────────
	fmt.Println("\n=== Reset — clear state for a key ===")
	fw2, _ := goratelimit.NewFixedWindow(2, 60)
	fw2.Allow(ctx, "user:1")
	fw2.Allow(ctx, "user:1")
	r, _ = fw2.Allow(ctx, "user:1")
	fmt.Printf("  before reset: allowed=%v remaining=%d\n", r.Allowed, r.Remaining)
	fw2.Reset(ctx, "user:1")
	r, _ = fw2.Allow(ctx, "user:1")
	fmt.Printf("  after  reset: allowed=%v remaining=%d\n", r.Allowed, r.Remaining)

	// ── Builder API ─────────────────────────────────────────────
	fmt.Println("\n=== Builder API ===")
	limiter, _ := goratelimit.NewBuilder().
		SlidingWindowCounter(10, 30*time.Second).
		Build()
	r, _ = limiter.Allow(ctx, "user:1")
	fmt.Printf("  allowed=%v remaining=%d limit=%d\n", r.Allowed, r.Remaining, r.Limit)
}
