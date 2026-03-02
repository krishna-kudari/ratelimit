package goratelimit_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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
		{
			name:          "valid parameters",
			limit:         100,
			windowSeconds: 60,
			epsilon:       0.01,
			delta:         0.001,
		},
		{
			name:           "zero limit",
			limit:          0,
			windowSeconds:  60,
			epsilon:        0.01,
			delta:          0.001,
			expectError:    true,
			errorSubstring: "must be positive",
		},
		{
			name:           "negative limit",
			limit:          -1,
			windowSeconds:  60,
			epsilon:        0.01,
			delta:          0.001,
			expectError:    true,
			errorSubstring: "must be positive",
		},
		{
			name:           "zero windowSeconds",
			limit:          100,
			windowSeconds:  0,
			epsilon:        0.01,
			delta:          0.001,
			expectError:    true,
			errorSubstring: "must be positive",
		},
		{
			name:           "epsilon zero",
			limit:          100,
			windowSeconds:  60,
			epsilon:        0,
			delta:          0.001,
			expectError:    true,
			errorSubstring: "epsilon must be in (0, 1)",
		},
		{
			name:           "epsilon >= 1",
			limit:          100,
			windowSeconds:  60,
			epsilon:        1.0,
			delta:          0.001,
			expectError:    true,
			errorSubstring: "epsilon must be in (0, 1)",
		},
		{
			name:           "delta zero",
			limit:          100,
			windowSeconds:  60,
			epsilon:        0.01,
			delta:          0,
			expectError:    true,
			errorSubstring: "delta must be in (0, 1)",
		},
		{
			name:           "delta >= 1",
			limit:          100,
			windowSeconds:  60,
			epsilon:        0.01,
			delta:          1.0,
			expectError:    true,
			errorSubstring: "delta must be in (0, 1)",
		},
		{
			name:          "tight accuracy",
			limit:         1000,
			windowSeconds: 10,
			epsilon:       0.001,
			delta:         0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter, err := goratelimit.NewCMS(tt.limit, tt.windowSeconds, tt.epsilon, tt.delta)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				} else if tt.errorSubstring != "" && !strings.Contains(err.Error(), tt.errorSubstring) {
					t.Errorf("expected error containing %q, got %q", tt.errorSubstring, err.Error())
				}
				if limiter != nil {
					t.Errorf("expected nil limiter on error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if limiter == nil {
					t.Errorf("expected non-nil limiter")
				}
			}
		})
	}
}

// ─── Allow ────────────────────────────────────────────────────────────────────

func TestCMS_Allow(t *testing.T) {
	ctx := context.Background()

	t.Run("allows requests within limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 0; i < 10; i++ {
			res, err := limiter.Allow(ctx, "user1")
			if err != nil {
				t.Fatalf("unexpected error on request %d: %v", i+1, err)
			}
			if !res.Allowed {
				t.Errorf("request %d should be allowed", i+1)
			}
			if res.Remaining < 0 {
				t.Errorf("remaining should be non-negative, got %d", res.Remaining)
			}
			if res.Limit != 10 {
				t.Errorf("limit should be 10, got %d", res.Limit)
			}
		}
	})

	t.Run("rejects requests exceeding limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 0; i < 5; i++ {
			res, _ := limiter.Allow(ctx, "user2")
			if !res.Allowed {
				t.Errorf("request %d should be allowed", i+1)
			}
		}

		res, err := limiter.Allow(ctx, "user2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Allowed {
			t.Error("6th request should be rejected")
		}
		if res.Remaining != 0 {
			t.Errorf("remaining should be 0 when rejected, got %d", res.Remaining)
		}
		if res.RetryAfter <= 0 {
			t.Errorf("retryAfter should be positive when rejected, got %v", res.RetryAfter)
		}
	})

	t.Run("remaining count decreases", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		prev := int64(5)
		for i := 0; i < 5; i++ {
			res, _ := limiter.Allow(ctx, "user3")
			if res.Remaining >= prev {
				t.Errorf("remaining should decrease: got %d (prev %d)", res.Remaining, prev)
			}
			prev = res.Remaining
		}
	})

	t.Run("separate keys are independent", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(3, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "keyA")
			if !res.Allowed {
				t.Errorf("keyA request %d should be allowed", i+1)
			}
		}

		res, _ := limiter.Allow(ctx, "keyA")
		if res.Allowed {
			t.Error("keyA should be exhausted")
		}

		res, _ = limiter.Allow(ctx, "keyB")
		if !res.Allowed {
			t.Error("keyB should be allowed independently of keyA")
		}
	})

	t.Run("allows requests after window rotates", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(3, 1, 0.01, 0.001) // 1-second window
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "user4")
			if !res.Allowed {
				t.Errorf("request %d should be allowed", i+1)
			}
		}
		res, _ := limiter.Allow(ctx, "user4")
		if res.Allowed {
			t.Error("should be rejected after exhausting limit")
		}

		// Wait 2+ full windows so both current and previous sketches are cleared.
		// After one rotation, previous still holds fading counts (sliding window).
		time.Sleep(2100 * time.Millisecond)

		res, _ = limiter.Allow(ctx, "user4")
		if !res.Allowed {
			t.Error("should be allowed after window rotation")
		}
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(50, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

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
		if allowed > 50 {
			t.Errorf("expected at most 50 allowed, got %d", allowed)
		}
		if allowed < 50 {
			t.Errorf("expected at least 50 allowed (burst), got %d", allowed)
		}
	})
}

// ─── AllowN ───────────────────────────────────────────────────────────────────

func TestCMS_AllowN(t *testing.T) {
	ctx := context.Background()

	t.Run("allows batch within limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res, err := limiter.AllowN(ctx, "batch1", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Allowed {
			t.Error("batch of 5 should be allowed")
		}

		res, err = limiter.AllowN(ctx, "batch1", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Allowed {
			t.Error("second batch of 5 should be allowed (total 10)")
		}
	})

	t.Run("rejects batch exceeding limit", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res, _ := limiter.AllowN(ctx, "batch2", 8)
		if !res.Allowed {
			t.Error("batch of 8 should be allowed")
		}

		res, _ = limiter.AllowN(ctx, "batch2", 5)
		if res.Allowed {
			t.Error("batch of 5 should be rejected (total would be 13 > 10)")
		}
	})
}

// ─── Reset ────────────────────────────────────────────────────────────────────

func TestCMS_Reset(t *testing.T) {
	ctx := context.Background()

	t.Run("reset is a no-op (returns nil)", func(t *testing.T) {
		limiter, err := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 0; i < 5; i++ {
			limiter.Allow(ctx, "reset-key")
		}

		if err := limiter.Reset(ctx, "reset-key"); err != nil {
			t.Errorf("Reset should return nil, got %v", err)
		}
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
		{
			name:     "standard 1% error",
			epsilon:  0.01,
			delta:    0.001,
			minBytes: 20000,
			maxBytes: 40000,
		},
		{
			name:     "tight 0.1% error",
			epsilon:  0.001,
			delta:    0.0001,
			minBytes: 200000,
			maxBytes: 500000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := goratelimit.CMSMemoryBytes(tt.epsilon, tt.delta)
			if mem < tt.minBytes || mem > tt.maxBytes {
				t.Errorf("expected memory between %d and %d bytes, got %d", tt.minBytes, tt.maxBytes, mem)
			}
		})
	}
}

// ─── LimitFunc ────────────────────────────────────────────────────────────────

func TestCMS_LimitFunc(t *testing.T) {
	ctx := context.Background()

	limiter, err := goratelimit.NewCMS(100, 60, 0.01, 0.001,
		goratelimit.WithLimitFunc(func(key string) int64 {
			if key == "vip" {
				return 10
			}
			return 3
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("regular user gets lower limit", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "regular")
			if !res.Allowed {
				t.Errorf("request %d should be allowed", i+1)
			}
		}
		res, _ := limiter.Allow(ctx, "regular")
		if res.Allowed {
			t.Error("4th request should be rejected for regular user")
		}
	})

	t.Run("vip user gets higher limit", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			res, _ := limiter.Allow(ctx, "vip")
			if !res.Allowed {
				t.Errorf("vip request %d should be allowed", i+1)
			}
		}
		res, _ := limiter.Allow(ctx, "vip")
		if res.Allowed {
			t.Error("11th vip request should be rejected")
		}
	})
}

// ─── Builder ──────────────────────────────────────────────────────────────────

func TestCMS_Builder(t *testing.T) {
	limiter, err := goratelimit.NewBuilder().
		CMS(100, 60*time.Second, 0.01, 0.001).
		Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limiter == nil {
		t.Fatal("expected non-nil limiter from builder")
	}

	ctx := context.Background()
	res, err := limiter.Allow(ctx, "builder-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Error("first request should be allowed")
	}
}

// ─── PreFilter ────────────────────────────────────────────────────────────────

func TestPreFilter(t *testing.T) {
	ctx := context.Background()

	t.Run("allows when both limiters allow", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 10)
		limiter := goratelimit.NewPreFilter(local, precise)

		res, err := limiter.Allow(ctx, "pf-user1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Allowed {
			t.Error("request should be allowed when both allow")
		}
	})

	t.Run("local blocks without hitting precise", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(3, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(100, 100) // very permissive

		limiter := goratelimit.NewPreFilter(local, precise)

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "pf-user2")
			if !res.Allowed {
				t.Errorf("request %d should be allowed", i+1)
			}
		}

		res, _ := limiter.Allow(ctx, "pf-user2")
		if res.Allowed {
			t.Error("4th request should be blocked by local CMS (limit 3)")
		}
	})

	t.Run("precise blocks even when local allows", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(100, 60, 0.01, 0.001) // very permissive
		precise, _ := goratelimit.NewGCRA(10, 3)              // strict

		limiter := goratelimit.NewPreFilter(local, precise)

		for i := 0; i < 3; i++ {
			res, _ := limiter.Allow(ctx, "pf-user3")
			if !res.Allowed {
				t.Errorf("request %d should be allowed", i+1)
			}
		}

		res, _ := limiter.Allow(ctx, "pf-user3")
		if res.Allowed {
			t.Error("4th request should be blocked by precise GCRA (burst 3)")
		}
	})

	t.Run("AllowN propagates correctly", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(10, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 10)
		limiter := goratelimit.NewPreFilter(local, precise)

		res, err := limiter.AllowN(ctx, "pf-user4", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Allowed {
			t.Error("batch of 5 should be allowed")
		}
	})

	t.Run("reset resets both limiters", func(t *testing.T) {
		local, _ := goratelimit.NewCMS(5, 60, 0.01, 0.001)
		precise, _ := goratelimit.NewGCRA(10, 5)
		limiter := goratelimit.NewPreFilter(local, precise)

		for i := 0; i < 5; i++ {
			limiter.Allow(ctx, "pf-reset")
		}

		err := limiter.Reset(ctx, "pf-reset")
		if err != nil {
			t.Fatalf("unexpected reset error: %v", err)
		}
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
