package goratelimit

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// ─── Serial: allowed path ─────────────────────────────────────────────────────

func BenchmarkFixedWindow(b *testing.B) {
	l, err := NewFixedWindow(1<<62, 3600)
	if err != nil {
		b.Fatalf("NewFixedWindow: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkSlidingWindow(b *testing.B) {
	l, err := NewSlidingWindow(1<<62, 3600)
	if err != nil {
		b.Fatalf("NewSlidingWindow: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkSlidingWindowCounter(b *testing.B) {
	l, err := NewSlidingWindowCounter(1<<62, 3600)
	if err != nil {
		b.Fatalf("NewSlidingWindowCounter: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkTokenBucket(b *testing.B) {
	l, err := NewTokenBucket(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewTokenBucket: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkLeakyBucket_Policing(b *testing.B) {
	l, err := NewLeakyBucket(1<<62, 1<<62, Policing)
	if err != nil {
		b.Fatalf("NewLeakyBucket: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkLeakyBucket_Shaping(b *testing.B) {
	l, err := NewLeakyBucket(1<<62, 1<<62, Shaping)
	if err != nil {
		b.Fatalf("NewLeakyBucket: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkGCRA(b *testing.B) {
	l, err := NewGCRA(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkCMS(b *testing.B) {
	l, err := NewCMS(1<<62, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	benchAllow(b, l)
}

func BenchmarkPreFilter(b *testing.B) {
	local, err := NewCMS(1<<62, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	precise, err := NewGCRA(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	l := NewPreFilter(local, precise)
	benchAllow(b, l)
}

// ─── Serial: denied path ──────────────────────────────────────────────────────
// Critical — denied path has different perf characteristics than allowed

func BenchmarkFixedWindow_Denied(b *testing.B) {
	l, err := NewFixedWindow(1, 3600)
	if err != nil {
		b.Fatalf("NewFixedWindow: %v", err)
	}
	ctx := context.Background()
	l.Allow(ctx, "k")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "k")
	}
}

func BenchmarkGCRA_Denied(b *testing.B) {
	l, err := NewGCRA(1, 1)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	ctx := context.Background()
	l.Allow(ctx, "k")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "k")
	}
}

func BenchmarkTokenBucket_Denied(b *testing.B) {
	l, err := NewTokenBucket(1, 1)
	if err != nil {
		b.Fatalf("NewTokenBucket: %v", err)
	}
	ctx := context.Background()
	l.Allow(ctx, "k")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "k")
	}
}

// ─── Parallel: contended single key ──────────────────────────────────────────

func BenchmarkFixedWindow_Parallel(b *testing.B) {
	l, err := NewFixedWindow(1<<62, 3600)
	if err != nil {
		b.Fatalf("NewFixedWindow: %v", err)
	}
	benchAllowParallel(b, l, "shared")
}

func BenchmarkSlidingWindowCounter_Parallel(b *testing.B) {
	l, err := NewSlidingWindowCounter(1<<62, 3600)
	if err != nil {
		b.Fatalf("NewSlidingWindowCounter: %v", err)
	}
	benchAllowParallel(b, l, "shared")
}

func BenchmarkTokenBucket_Parallel(b *testing.B) {
	l, err := NewTokenBucket(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewTokenBucket: %v", err)
	}
	benchAllowParallel(b, l, "shared")
}

func BenchmarkLeakyBucket_Parallel(b *testing.B) {
	l, err := NewLeakyBucket(1<<62, 1<<62, Policing)
	if err != nil {
		b.Fatalf("NewLeakyBucket: %v", err)
	}
	benchAllowParallel(b, l, "shared")
}

func BenchmarkGCRA_Parallel(b *testing.B) {
	l, err := NewGCRA(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	benchAllowParallel(b, l, "shared")
}

func BenchmarkCMS_Parallel(b *testing.B) {
	l, err := NewCMS(1<<62, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	benchAllowParallel(b, l, "shared")
}

func BenchmarkPreFilter_Parallel(b *testing.B) {
	local, err := NewCMS(1<<62, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	precise, err := NewGCRA(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	l := NewPreFilter(local, precise)
	benchAllowParallel(b, l, "shared")
}

// ─── Parallel: distinct keys (high cardinality) ───────────────────────────────

func BenchmarkTokenBucket_DistinctKeys(b *testing.B) {
	l, err := NewTokenBucket(1000, 100)
	if err != nil {
		b.Fatalf("NewTokenBucket: %v", err)
	}
	benchAllowParallelDistinct(b, l)
}

func BenchmarkFixedWindow_DistinctKeys(b *testing.B) {
	l, err := NewFixedWindow(1000, 3600)
	if err != nil {
		b.Fatalf("NewFixedWindow: %v", err)
	}
	benchAllowParallelDistinct(b, l)
}

func BenchmarkGCRA_DistinctKeys(b *testing.B) {
	l, err := NewGCRA(1000, 100)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	benchAllowParallelDistinct(b, l)
}

func BenchmarkCMS_DistinctKeys(b *testing.B) {
	l, err := NewCMS(1000, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	benchAllowParallelDistinct(b, l)
}

// ─── Window size sensitivity ──────────────────────────────────────────────────

func BenchmarkSlidingWindow_WindowSizes(b *testing.B) {
	for _, w := range []struct {
		name string
		secs int64
	}{
		{"1s", 1}, {"1m", 60}, {"1h", 3600}, {"24h", 86400},
	} {
		b.Run(w.name, func(b *testing.B) {
			l, err := NewSlidingWindow(1<<62, w.secs)
			if err != nil {
				b.Fatalf("NewSlidingWindow: %v", err)
			}
			benchAllow(b, l)
		})
	}
}

// ─── AllowN ───────────────────────────────────────────────────────────────────

func BenchmarkTokenBucket_AllowN(b *testing.B) {
	for _, n := range []int{1, 5, 10} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			l, err := NewTokenBucket(1<<62, 1<<62)
			if err != nil {
				b.Fatalf("NewTokenBucket: %v", err)
			}
			ctx := context.Background()
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = l.AllowN(ctx, "k", n)
			}
		})
	}
}

// ─── PreFilter: CMS actually blocking ────────────────────────────────────────

func BenchmarkPreFilter_CMSBlocks(b *testing.B) {
	local, err := NewCMS(10, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	precise, err := NewGCRA(1<<62, 1<<62)
	if err != nil {
		b.Fatalf("NewGCRA: %v", err)
	}
	l := NewPreFilter(local, precise)
	ctx := context.Background()

	// exhaust CMS limit so all subsequent calls are blocked by CMS
	for i := 0; i < 100; i++ {
		l.Allow(ctx, "hot-key")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "hot-key")
	}
}

// ─── CMS: high cardinality and fixed memory ───────────────────────────────────
// Shows what CMS enables — fixed memory regardless of unique keys.

func BenchmarkCMS_MillionUniqueKeys(b *testing.B) {
	l, err := NewCMS(1<<62, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			key := fmt.Sprintf("ip:%d", i%1_000_000)
			_, _ = l.Allow(ctx, key)
			i++
		}
	})
}

// BenchmarkCMS_MemoryFixed proves CMS memory stays constant regardless of
// unique keys seen (vs map-based limiters that grow per key).
func BenchmarkCMS_MemoryFixed(b *testing.B) {
	l, err := NewCMS(1<<62, 3600, 0.01, 0.001)
	if err != nil {
		b.Fatalf("NewCMS: %v", err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, fmt.Sprintf("unique-key-%d", i))
	}
}

// ─── Correctness under concurrency ───────────────────────────────────────────
// Not performance — proves atomicity guarantees hold under load

func TestCorrectness_ExactAllowedCount(t *testing.T) {
	const (
		limit      = 100
		goroutines = 500
	)

	cases := []struct {
		name    string
		limiter func() (Limiter, error)
	}{
		{"FixedWindow", func() (Limiter, error) { return NewFixedWindow(limit, 3600) }},
		{"TokenBucket", func() (Limiter, error) { return NewTokenBucket(limit, limit) }},
		{"GCRA", func() (Limiter, error) { return NewGCRA(limit, limit) }},
		{"CMS", func() (Limiter, error) { return NewCMS(limit, 3600, 0.01, 0.001) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := tc.limiter()
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			var allowed, denied atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})

			for i := 0; i < goroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					res, _ := l.Allow(ctx, "key")
					if res.Allowed {
						allowed.Add(1)
					} else {
						denied.Add(1)
					}
				}()
			}

			close(start)
			wg.Wait()

			if allowed.Load() != limit {
				t.Errorf("expected exactly %d allowed, got %d (denied: %d)",
					limit, allowed.Load(), denied.Load())
			}
		})
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func benchAllow(b *testing.B, l Limiter) {
	b.Helper()
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "k")
	}
}

func benchAllowParallel(b *testing.B, l Limiter, key string) {
	b.Helper()
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = l.Allow(ctx, key)
		}
	})
}

func benchAllowParallelDistinct(b *testing.B, l Limiter) {
	b.Helper()
	ctx := context.Background()
	var seq atomic.Int64
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// rotate through 100k unique keys — realistic cardinality
			id := seq.Add(1) % 100_000
			key := "user:" + strconv.FormatInt(id, 10)
			_, _ = l.Allow(ctx, key)
		}
	})
}
