// Advanced features — dynamic limits, fail-open, local cache, Prometheus, custom keys.
// Run: go run ./examples/advanced/
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/cache"
	"github.com/krishna-kudari/ratelimit/metrics"
	"github.com/krishna-kudari/ratelimit/middleware"
)

func main() {
	ctx := context.Background()

	// ── Dynamic per-key limits ──────────────────────────────────
	// Premium users get 100 req/min, free users get 5.
	fmt.Println("=== Dynamic Per-Key Limits ===")
	limiter, _ := goratelimit.NewFixedWindow(5, 60,
		goratelimit.WithLimitFunc(func(key string) int64 {
			if key == "premium:user" {
				return 100
			}
			return 0 // fallback to default (5)
		}),
	)

	r, _ := limiter.Allow(ctx, "premium:user")
	fmt.Printf("  premium: allowed=%v remaining=%d limit=%d\n", r.Allowed, r.Remaining, r.Limit)
	r, _ = limiter.Allow(ctx, "free:user")
	fmt.Printf("  free:    allowed=%v remaining=%d limit=%d\n", r.Allowed, r.Remaining, r.Limit)

	// ── Fail-open vs fail-closed ────────────────────────────────
	// Default: fail-open (allow on backend errors).
	// Flip to fail-closed for strict enforcement.
	fmt.Println("\n=== Fail-Open (default) ===")
	_, _ = goratelimit.NewTokenBucket(10, 1, goratelimit.WithFailOpen(true))
	fmt.Println("  backend down → requests allowed")

	fmt.Println("\n=== Fail-Closed ===")
	_, _ = goratelimit.NewTokenBucket(10, 1, goratelimit.WithFailOpen(false))
	fmt.Println("  backend down → requests denied")

	// ── Local cache (L1) ────────────────────────────────────────
	// Hot keys counted in-process first, cutting backend round-trips.
	fmt.Println("\n=== Local Cache (L1) ===")
	base, _ := goratelimit.NewTokenBucket(100, 10)
	cached := cache.New(base,
		cache.WithTTL(200*time.Millisecond),
		cache.WithMaxKeys(10000),
	)
	r, _ = cached.Allow(ctx, "user:1")
	fmt.Printf("  cached: allowed=%v remaining=%d\n", r.Allowed, r.Remaining)

	// ── Prometheus metrics ──────────────────────────────────────
	// Wrap any limiter — request counts, latency histograms, error counters.
	fmt.Println("\n=== Prometheus Metrics ===")
	collector := metrics.NewCollector(
		metrics.WithNamespace("myapp"),
	)
	instrumented := metrics.Wrap(base, metrics.TokenBucket, collector)
	r, _ = instrumented.Allow(ctx, "user:1")
	fmt.Printf("  instrumented: allowed=%v remaining=%d\n", r.Allowed, r.Remaining)
	fmt.Println("  → expose via promhttp.Handler() on /metrics")

	// ── Custom key prefix ───────────────────────────────────────
	fmt.Println("\n=== Custom Key Prefix ===")
	_, _ = goratelimit.NewFixedWindow(10, 60,
		goratelimit.WithKeyPrefix("api:v2"),
	)
	fmt.Println("  keys stored as: api:v2:<key>")

	// ── Key extraction strategies (middleware) ───────────────────
	fmt.Println("\n=== Key Extraction (middleware) ===")
	mwLimiter, _ := goratelimit.NewTokenBucket(10, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// By IP (most common)
	_ = middleware.RateLimit(mwLimiter, middleware.KeyByIP)(handler)
	fmt.Println("  KeyByIP          → rate limit per client IP")

	// By header (API key, auth token)
	_ = middleware.RateLimit(mwLimiter, middleware.KeyByHeader("X-API-Key"))(handler)
	fmt.Println("  KeyByHeader      → rate limit per API key")

	// By path + IP (separate limits per endpoint)
	_ = middleware.RateLimit(mwLimiter, middleware.KeyByPathAndIP)(handler)
	fmt.Println("  KeyByPathAndIP   → rate limit per endpoint per IP")

	// Custom key function
	_ = middleware.RateLimit(mwLimiter, func(r *http.Request) string {
		return r.Header.Get("X-User-ID")
	})(handler)
	fmt.Println("  custom func      → rate limit per user ID")

	// ── CMS PreFilter (DDoS mitigation) ────────────────────────
	// CMS runs locally in nanoseconds. Only normal-looking traffic
	// escalates to the precise limiter (which may hit Redis).
	fmt.Println("\n=== CMS PreFilter (DDoS Pattern) ===")
	sketch, _ := goratelimit.NewCMS(
		100,   // 100 req / window
		60,    // 60-second window
		0.01,  // 1% error rate
		0.001, // 0.1% failure probability
	)
	precise, _ := goratelimit.NewGCRA(10, 20) // precise in-memory GCRA
	prefilter := goratelimit.NewPreFilter(sketch, precise)
	r, _ = prefilter.Allow(ctx, "suspect-ip")
	fmt.Printf("  prefilter: allowed=%v remaining=%d\n", r.Allowed, r.Remaining)
	fmt.Printf("  CMS memory: %d bytes (fixed, regardless of unique keys)\n",
		goratelimit.CMSMemoryBytes(0.01, 0.001))

	// ── Builder API with all options ────────────────────────────
	fmt.Println("\n=== Builder API ===")
	built, _ := goratelimit.NewBuilder().
		TokenBucket(100, 10).
		KeyPrefix("api").
		FailOpen(true).
		Build()
	r, _ = built.Allow(ctx, "user:1")
	fmt.Printf("  builder: allowed=%v remaining=%d limit=%d\n", r.Allowed, r.Remaining, r.Limit)
}
