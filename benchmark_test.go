package goratelimit

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
)

// ─── Single-key (serial) ─────────────────────────────────────────────────────

func BenchmarkFixedWindow(b *testing.B) {
	l, _ := NewFixedWindow(int64(b.N)+1, 3600)
	benchAllow(b, l)
}

func BenchmarkSlidingWindow(b *testing.B) {
	l, _ := NewSlidingWindow(int64(b.N)+1, 3600)
	benchAllow(b, l)
}

func BenchmarkSlidingWindowCounter(b *testing.B) {
	l, _ := NewSlidingWindowCounter(int64(b.N)+1, 3600)
	benchAllow(b, l)
}

func BenchmarkTokenBucket(b *testing.B) {
	l, _ := NewTokenBucket(int64(b.N)+1, int64(b.N)+1)
	benchAllow(b, l)
}

func BenchmarkLeakyBucket_Policing(b *testing.B) {
	l, _ := NewLeakyBucket(int64(b.N)+1, int64(b.N)+1, Policing)
	benchAllow(b, l)
}

func BenchmarkLeakyBucket_Shaping(b *testing.B) {
	l, _ := NewLeakyBucket(int64(b.N)+1, int64(b.N)+1, Shaping)
	benchAllow(b, l)
}

func BenchmarkGCRA(b *testing.B) {
	l, _ := NewGCRA(int64(b.N)+1, int64(b.N)+1)
	benchAllow(b, l)
}

func BenchmarkCMS(b *testing.B) {
	l, _ := NewCMS(int64(b.N)+1, 3600, 0.01, 0.001)
	benchAllow(b, l)
}

func BenchmarkPreFilter(b *testing.B) {
	local, _ := NewCMS(int64(b.N)+1, 3600, 0.01, 0.001)
	precise, _ := NewGCRA(int64(b.N)+1, int64(b.N)+1)
	l := NewPreFilter(local, precise)
	benchAllow(b, l)
}

// ─── Parallel (contended single key) ─────────────────────────────────────────

func BenchmarkFixedWindow_Parallel(b *testing.B) {
	l, _ := NewFixedWindow(1<<62, 3600)
	benchAllowParallel(b, l, "shared")
}

func BenchmarkSlidingWindowCounter_Parallel(b *testing.B) {
	l, _ := NewSlidingWindowCounter(1<<62, 3600)
	benchAllowParallel(b, l, "shared")
}

func BenchmarkTokenBucket_Parallel(b *testing.B) {
	l, _ := NewTokenBucket(1<<62, 1<<62)
	benchAllowParallel(b, l, "shared")
}

func BenchmarkLeakyBucket_Parallel(b *testing.B) {
	l, _ := NewLeakyBucket(1<<62, 1<<62, Policing)
	benchAllowParallel(b, l, "shared")
}

func BenchmarkGCRA_Parallel(b *testing.B) {
	l, _ := NewGCRA(1<<62, 1<<62)
	benchAllowParallel(b, l, "shared")
}

func BenchmarkCMS_Parallel(b *testing.B) {
	l, _ := NewCMS(1<<62, 3600, 0.01, 0.001)
	benchAllowParallel(b, l, "shared")
}

func BenchmarkPreFilter_Parallel(b *testing.B) {
	local, _ := NewCMS(1<<62, 3600, 0.01, 0.001)
	precise, _ := NewGCRA(1<<62, 1<<62)
	l := NewPreFilter(local, precise)
	benchAllowParallel(b, l, "shared")
}

// ─── Parallel (distinct keys — no lock contention) ───────────────────────────

func BenchmarkTokenBucket_DistinctKeys(b *testing.B) {
	l, _ := NewTokenBucket(1000, 100)
	benchAllowParallelDistinct(b, l)
}

func BenchmarkFixedWindow_DistinctKeys(b *testing.B) {
	l, _ := NewFixedWindow(1000, 3600)
	benchAllowParallelDistinct(b, l)
}

func BenchmarkCMS_DistinctKeys(b *testing.B) {
	l, _ := NewCMS(1000, 3600, 0.01, 0.001)
	benchAllowParallelDistinct(b, l)
}

// ─── AllowN ──────────────────────────────────────────────────────────────────

func BenchmarkTokenBucket_AllowN(b *testing.B) {
	for _, n := range []int{1, 5, 10} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			l, _ := NewTokenBucket(1<<62, 1<<62)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = l.AllowN(ctx, "k", n)
			}
		})
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func benchAllow(b *testing.B, l Limiter) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "k")
	}
}

func benchAllowParallel(b *testing.B, l Limiter, key string) {
	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = l.Allow(ctx, key)
		}
	})
}

func benchAllowParallelDistinct(b *testing.B, l Limiter) {
	ctx := context.Background()
	var seq atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := seq.Add(1)
		key := "user:" + strconv.FormatInt(id, 10)
		for pb.Next() {
			_, _ = l.Allow(ctx, key)
		}
	})
}
