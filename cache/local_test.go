package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// mockLimiter records calls and returns configurable results.
type mockLimiter struct {
	mu       sync.Mutex
	calls    int
	allowN   func(ctx context.Context, key string, n int) (goratelimit.Result, error)
	resetErr error
	resets   int
}

func (m *mockLimiter) Allow(ctx context.Context, key string) (goratelimit.Result, error) {
	return m.AllowN(ctx, key, 1)
}

func (m *mockLimiter) AllowN(ctx context.Context, key string, n int) (goratelimit.Result, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return m.allowN(ctx, key, n)
}

func (m *mockLimiter) Reset(ctx context.Context, key string) error {
	m.mu.Lock()
	m.resets++
	m.mu.Unlock()
	return m.resetErr
}

func (m *mockLimiter) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestLocalCache_CacheHit(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 10,
				Limit:     10,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(500*time.Millisecond))
	defer lc.Close()

	ctx := context.Background()

	// First call — cache miss, hits backend
	r, err := lc.Allow(ctx, "k1")
	require.NoError(t, err)
	require.True(t, r.Allowed, "expected allowed")
	require.Equal(t, 1, mock.getCalls(), "expected 1 backend call")

	// Next calls should be served from cache
	for i := 0; i < 5; i++ {
		r, err = lc.Allow(ctx, "k1")
		require.NoError(t, err)
		require.True(t, r.Allowed, "call %d: expected allowed", i)
	}
	require.Equal(t, 1, mock.getCalls(), "expected still 1 backend call after cache hits")
}

func TestLocalCache_RemainingDecreases(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 5,
				Limit:     5,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(time.Second))
	defer lc.Close()

	ctx := context.Background()

	r, _ := lc.Allow(ctx, "k1")
	require.Equal(t, int64(5), r.Remaining, "expected remaining=5 from backend")

	r, _ = lc.Allow(ctx, "k1")
	require.Equal(t, int64(4), r.Remaining, "expected remaining=4")

	r, _ = lc.Allow(ctx, "k1")
	require.Equal(t, int64(3), r.Remaining, "expected remaining=3")
}

func TestLocalCache_ExhaustedLocalQuota_SyncsBackend(t *testing.T) {
	var callCount atomic.Int64
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			callCount.Add(1)
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 2,
				Limit:     3,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(5*time.Second))
	defer lc.Close()

	ctx := context.Background()

	// Call 1: cache miss → backend (call 1), returns remaining=2, localUsed=0
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, int64(1), callCount.Load(), "expected 1 backend call")

	// Call 2: cache hit → remaining=2, localUsed becomes 1, 2-0>=1 true → serves locally
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, int64(1), callCount.Load(), "expected still 1 backend call")

	// Call 3: cache hit → remaining=2, localUsed=1, 2-1>=1 true → serves locally
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, int64(1), callCount.Load(), "expected still 1 backend call after call 3")

	// Call 4: cache hit → remaining=2, localUsed=2, 2-2=0 < 1 → exhausted, syncs backend (call 2)
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, int64(2), callCount.Load(), "expected 2 backend calls after local exhaustion")
}

func TestLocalCache_DeniedCached(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:    false,
				Remaining:  0,
				Limit:      10,
				RetryAfter: time.Second,
				ResetAt:    time.Now().Add(time.Second),
			}, nil
		},
	}

	lc := New(mock, WithTTL(time.Second))
	defer lc.Close()

	ctx := context.Background()

	// First call — backend returns denial
	r, _ := lc.Allow(ctx, "k1")
	require.False(t, r.Allowed, "expected denied")

	// Subsequent calls served from cache (denial cached)
	for i := 0; i < 5; i++ {
		r, _ = lc.Allow(ctx, "k1")
		require.False(t, r.Allowed, "expected cached denial")
	}
	require.Equal(t, 1, mock.getCalls(), "expected 1 backend call for cached denial")
}

func TestLocalCache_TTLExpiry(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 100,
				Limit:     100,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(50*time.Millisecond))
	defer lc.Close()

	ctx := context.Background()

	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, 1, mock.getCalls(), "expected 1 call")

	// Within TTL — should still be cached
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, 1, mock.getCalls(), "expected still 1 call within TTL")

	// Wait for TTL to expire
	time.Sleep(60 * time.Millisecond)

	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, 2, mock.getCalls(), "expected 2 calls after TTL expiry")
}

func TestLocalCache_DenialTTL_UsesRetryAfter(t *testing.T) {
	callCount := 0
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			callCount++
			return goratelimit.Result{
				Allowed:    false,
				Remaining:  0,
				Limit:      10,
				RetryAfter: 30 * time.Millisecond,
				ResetAt:    time.Now().Add(30 * time.Millisecond),
			}, nil
		},
	}

	// TTL is 5s, but denied result has RetryAfter=30ms → uses the shorter one
	lc := New(mock, WithTTL(5*time.Second))
	defer lc.Close()

	ctx := context.Background()

	lc.Allow(ctx, "k1")
	require.Equal(t, 1, callCount, "expected 1 call")

	time.Sleep(40 * time.Millisecond)

	lc.Allow(ctx, "k1")
	require.Equal(t, 2, callCount, "expected 2 calls after retryAfter expiry")
}

func TestLocalCache_AllowN(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 10,
				Limit:     10,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(time.Second))
	defer lc.Close()

	ctx := context.Background()

	// AllowN(5): cache miss → backend (call 1), returns remaining=10
	r, _ := lc.AllowN(ctx, "k1", 5)
	require.True(t, r.Allowed, "expected allowed")
	require.Equal(t, int64(10), r.Remaining, "expected remaining=10 from backend")

	// AllowN(5): cache hit → localUsed=5, remaining=10-5=5
	r, _ = lc.AllowN(ctx, "k1", 5)
	require.True(t, r.Allowed, "expected allowed")
	require.Equal(t, int64(5), r.Remaining, "expected remaining=5")

	// AllowN(5): cache hit → localUsed=10, remaining=10-10=0
	r, _ = lc.AllowN(ctx, "k1", 5)
	require.True(t, r.Allowed, "expected allowed")
	require.Equal(t, int64(0), r.Remaining, "expected remaining=0")

	// AllowN(1): local quota exhausted (10-10=0 < 1) → syncs backend (call 2)
	_, _ = lc.AllowN(ctx, "k1", 1)
	require.Equal(t, 2, mock.getCalls(), "expected 2 backend calls")
}

func TestLocalCache_Reset(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 10,
				Limit:     10,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(5*time.Second))
	defer lc.Close()

	ctx := context.Background()

	// Populate cache
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, 1, mock.getCalls(), "expected 1 call")

	// Reset evicts from cache
	err := lc.Reset(ctx, "k1")
	require.NoError(t, err)

	// Next call must hit backend (cache was evicted)
	_, _ = lc.Allow(ctx, "k1")
	require.Equal(t, 2, mock.getCalls(), "expected 2 backend calls after reset")
}

func TestLocalCache_MultipleKeys(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, key string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 5,
				Limit:     5,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(time.Second))
	defer lc.Close()

	ctx := context.Background()

	lc.Allow(ctx, "user:1")
	lc.Allow(ctx, "user:2")
	lc.Allow(ctx, "user:3")

	require.Equal(t, 3, mock.getCalls(), "expected 3 backend calls for 3 different keys")

	// Subsequent calls on same keys → cache hits
	lc.Allow(ctx, "user:1")
	lc.Allow(ctx, "user:2")
	lc.Allow(ctx, "user:3")
	require.Equal(t, 3, mock.getCalls(), "expected still 3 backend calls after cache hits")
}

func TestLocalCache_MaxKeys(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 10,
				Limit:     10,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(5*time.Second), WithMaxKeys(3))
	defer lc.Close()

	ctx := context.Background()

	// Fill to max
	lc.Allow(ctx, "k1")
	time.Sleep(time.Millisecond)
	lc.Allow(ctx, "k2")
	time.Sleep(time.Millisecond)
	lc.Allow(ctx, "k3")

	stats := lc.Stats()
	require.Equal(t, 3, stats.Keys, "expected 3 keys")

	// Adding 4th should evict oldest (k1)
	lc.Allow(ctx, "k4")
	stats = lc.Stats()
	require.Equal(t, 3, stats.Keys, "expected 3 keys after eviction")
}

func TestLocalCache_ConcurrentAccess(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 1000,
				Limit:     1000,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(time.Second))
	defer lc.Close()

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, err := lc.Allow(ctx, "concurrent-key")
				assert.NoError(t, err)
			}
		}()
	}
	wg.Wait()

	// Should have far fewer than 10000 backend calls due to caching
	require.LessOrEqual(t, mock.getCalls(), 100, "expected significantly fewer backend calls with caching")
}

func TestLocalCache_Stats(t *testing.T) {
	mock := &mockLimiter{
		allowN: func(_ context.Context, _ string, _ int) (goratelimit.Result, error) {
			return goratelimit.Result{
				Allowed:   true,
				Remaining: 10,
				Limit:     10,
				ResetAt:   time.Now().Add(time.Minute),
			}, nil
		},
	}

	lc := New(mock, WithTTL(time.Second))
	defer lc.Close()

	ctx := context.Background()

	stats := lc.Stats()
	require.Equal(t, 0, stats.Keys, "expected 0 keys initially")

	_, _ = lc.Allow(ctx, "k1")
	_, _ = lc.Allow(ctx, "k2")

	stats = lc.Stats()
	require.Equal(t, 2, stats.Keys, "expected 2 keys")
}
