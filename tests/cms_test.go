package goratelimit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// ─── Constructor ──────────────────────────────────────────────────────────────

func TestNewCMS(t *testing.T) {
	tests := []struct {
		name           string
		limit          int64
		windowSeconds  int64
		epsilon        float64
		delta          float64
		expectError    bool
		errorSubstring string
	}{
		{"valid parameters", 100, 60, 0.01, 0.001, false, ""},
		{"zero limit", 0, 60, 0.01, 0.001, true, "must be positive"},
		{"negative limit", -1, 60, 0.01, 0.001, true, "must be positive"},
		{"zero windowSeconds", 100, 0, 0.01, 0.001, true, "must be positive"},
		{"epsilon zero", 100, 60, 0, 0.001, true, "epsilon must be in (0, 1)"},
		{"epsilon >= 1", 100, 60, 1.0, 0.001, true, "epsilon must be in (0, 1)"},
		{"delta zero", 100, 60, 0.01, 0, true, "delta must be in (0, 1)"},
		{"delta >= 1", 100, 60, 0.01, 1.0, true, "delta must be in (0, 1)"},
		{"tight accuracy", 1000, 10, 0.001, 0.0001, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter, err := goratelimit.NewCMS(tt.limit, tt.windowSeconds, tt.epsilon, tt.delta)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorSubstring)
				assert.Nil(t, limiter)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, limiter)
			}
		})
	}
}

// ─── Allow ────────────────────────────────────────────────────────────────────

func TestCMS_Allow(t *testing.T) {
	ctx := context.Background()

	t.Run("allows requests within limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		require.NoError(t, err)

		for i := 0; i < 10; i++ {
			res, err := limiter.Allow(ctx, "user1")
			require.NoError(t, err)
			assert.True(t, res.Allowed, "request %d should be allowed", i+1)
			assert.GreaterOrEqual(t, res.Remaining, int64(0))
			assert.Equal(t, int64(10), res.Limit)
		}
	})

	t.Run("rejects requests exceeding limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		require.NoError(t, err)

		for i := 0; i < 5; i++ {
			res, _ := limiter.Allow(ctx, "user2")
			assert.True(t, res.Allowed, "request %d should be allowed", i+1)
		}

		res, err := limiter.Allow(ctx, "user2")
		require.NoError(t, err)
		assert.False(t, res.Allowed)
		assert.Equal(t, int64(0), res.Remaining)
		assert.Greater(t, res.RetryAfter, time.Duration(0))
	})

	t.Run("remaining count decreases", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		require.NoError(t, err)

		prev := int64(5)
		for i := 0; i < 5; i++ {
			res, _ := limiter.Allow(ctx, "user3")
			assert.Less(t, res.Remaining, prev, "remaining should decrease")
			prev = res.Remaining
		}
	})

	t.Run("separate keys are independent", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(3, 60, 0.01, 0.001)
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "keyA")
			assert.True(t, res.Allowed, "keyA request %d", i+1)
		}

		res, _ := limiter.Allow(ctx, "keyA")
		assert.False(t, res.Allowed, "keyA should be exhausted")

		res, _ = limiter.Allow(ctx, "keyB")
		assert.True(t, res.Allowed, "keyB should be independent")
	})

	t.Run("allows requests after window rotates", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(3, 1, 0.01, 0.001)
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "user4")
			assert.True(t, res.Allowed, "request %d", i+1)
		}
		res, _ := limiter.Allow(ctx, "user4")
		assert.False(t, res.Allowed, "should be rejected after exhausting limit")

		// Wait 2+ full windows so both current and previous sketches are cleared.
		time.Sleep(2100 * time.Millisecond)

		res, _ = limiter.Allow(ctx, "user4")
		assert.True(t, res.Allowed, "should be allowed after window rotation")
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(50, 60, 0.01, 0.001)
		require.NoError(t, err)

		results := make(chan bool, 100)
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, _ := limiter.Allow(ctx, "concurrent")
				results <- res.Allowed
			}()
		}
		wg.Wait()
		close(results)

		allowed := 0
		for a := range results {
			if a {
				allowed++
			}
		}
		assert.Equal(t, 50, allowed)
	})
}

// ─── AllowN ───────────────────────────────────────────────────────────────────

func TestCMS_AllowN(t *testing.T) {
	ctx := context.Background()

	t.Run("allows batch within limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		require.NoError(t, err)

		res, err := limiter.AllowN(ctx, "batch1", 5)
		require.NoError(t, err)
		assert.True(t, res.Allowed)

		res, err = limiter.AllowN(ctx, "batch1", 5)
		require.NoError(t, err)
		assert.True(t, res.Allowed, "second batch of 5 should be allowed (total 10)")
	})

	t.Run("rejects batch exceeding limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		require.NoError(t, err)

		res, _ := limiter.AllowN(ctx, "batch2", 8)
		assert.True(t, res.Allowed)

		res, _ = limiter.AllowN(ctx, "batch2", 5)
		assert.False(t, res.Allowed, "batch of 5 should be rejected (total would be 13 > 10)")
	})
}

// ─── Reset ────────────────────────────────────────────────────────────────────

func TestCMS_Reset(t *testing.T) {
	ctx := context.Background()

	t.Run("reset is a no-op (returns nil)", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		require.NoError(t, err)

		for i := 0; i < 5; i++ {
			limiter.Allow(ctx, "reset-key")
		}
		assert.NoError(t, limiter.Reset(ctx, "reset-key"))
	})
}

// ─── MemoryBytes ──────────────────────────────────────────────────────────────

func TestCMSMemoryBytes(t *testing.T) {
	tests := []struct {
		name     string
		epsilon  float64
		delta    float64
		minBytes int
		maxBytes int
	}{
		{"standard 1% error", 0.01, 0.001, 20000, 40000},
		{"tight 0.1% error", 0.001, 0.0001, 200000, 500000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := goratelimit.CMSMemoryBytes(tt.epsilon, tt.delta)
			assert.GreaterOrEqual(t, mem, tt.minBytes)
			assert.LessOrEqual(t, mem, tt.maxBytes)
		})
	}
}

// ─── LimitFunc ────────────────────────────────────────────────────────────────

func TestCMS_LimitFunc(t *testing.T) {
	ctx := context.Background()

	limiter, err := goratelimit.NewCMS(100, 60, 0.01, 0.001,
		goratelimit.WithLimitFunc(func(ctx context.Context, key string) int64 {
			if key == "vip" {
				return 10
			}
			return 3
		}),
	)
	require.NoError(t, err)

	t.Run("regular user gets lower limit", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "regular")
			assert.True(t, res.Allowed, "request %d", i+1)
		}
		res, _ := limiter.Allow(ctx, "regular")
		assert.False(t, res.Allowed, "4th request should be rejected")
	})

	t.Run("vip user gets higher limit", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			res, _ := limiter.Allow(ctx, "vip")
			assert.True(t, res.Allowed, "vip request %d", i+1)
		}
		res, _ := limiter.Allow(ctx, "vip")
		assert.False(t, res.Allowed, "11th vip request should be rejected")
	})
}

// ─── Builder ──────────────────────────────────────────────────────────────────

func TestCMS_Builder(t *testing.T) {
	limiter, err := goratelimit.NewBuilder().
		CMS(100, 60*time.Second, 0.01, 0.001).
		Build()
	require.NoError(t, err)
	require.NotNil(t, limiter)

	res, err := limiter.Allow(context.Background(), "builder-key")
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

// ─── PreFilter ────────────────────────────────────────────────────────────────

func TestPreFilter(t *testing.T) {
	ctx := context.Background()

	t.Run("allows when both limiters allow", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 10)
		limiter := goratelimit.NewPreFilter(local, precise)

		res, err := limiter.Allow(ctx, "pf-user1")
		require.NoError(t, err)
		assert.True(t, res.Allowed)
	})

	t.Run("local blocks without hitting precise", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(3, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(100, 100)
		limiter := goratelimit.NewPreFilter(local, precise)

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "pf-user2")
			assert.True(t, res.Allowed, "request %d", i+1)
		}

		res, _ := limiter.Allow(ctx, "pf-user2")
		assert.False(t, res.Allowed, "blocked by local CMS (limit 3)")
	})

	t.Run("precise blocks even when local allows", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(100, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 3)
		limiter := goratelimit.NewPreFilter(local, precise)

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "pf-user3")
			assert.True(t, res.Allowed, "request %d", i+1)
		}

		res, _ := limiter.Allow(ctx, "pf-user3")
		assert.False(t, res.Allowed, "blocked by precise GCRA (burst 3)")
	})

	t.Run("AllowN propagates correctly", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 10)
		limiter := goratelimit.NewPreFilter(local, precise)

		res, err := limiter.AllowN(ctx, "pf-user4", 5)
		require.NoError(t, err)
		assert.True(t, res.Allowed)
	})

	t.Run("reset resets both limiters", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 5)
		limiter := goratelimit.NewPreFilter(local, precise)

		for i := 0; i < 5; i++ {
			limiter.Allow(ctx, "pf-reset")
		}
		assert.NoError(t, limiter.Reset(ctx, "pf-reset"))
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(50, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(100, 100)
		limiter := goratelimit.NewPreFilter(local, precise)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				limiter.Allow(ctx, "pf-concurrent")
			}()
		}
		wg.Wait()
	})
}
