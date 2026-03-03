package goratelimit_test

import (
	"context"
	"fmt"
	"time"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

func ExampleNew() {
	limiter, _ := goratelimit.New("", goratelimit.PerMinute(100))
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d limit=%d\n", result.Allowed, result.Remaining, result.Limit)
	// Output: allowed=true remaining=99 limit=100
}

func ExampleNewInMemory() {
	limiter, _ := goratelimit.NewInMemory(goratelimit.PerHour(500))
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v limit=%d\n", result.Allowed, result.Limit)
	// Output: allowed=true limit=500
}

func ExampleNewFixedWindow() {
	limiter, _ := goratelimit.NewFixedWindow(10, 60)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=9
}

func ExampleNewSlidingWindow() {
	limiter, _ := goratelimit.NewSlidingWindow(10, 60)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=9
}

func ExampleNewSlidingWindowCounter() {
	limiter, _ := goratelimit.NewSlidingWindowCounter(10, 60)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=9
}

func ExampleNewTokenBucket() {
	limiter, _ := goratelimit.NewTokenBucket(100, 10)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=99
}

func ExampleNewLeakyBucket_policing() {
	limiter, _ := goratelimit.NewLeakyBucket(10, 1, goratelimit.Policing)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=9
}

func ExampleNewLeakyBucket_shaping() {
	limiter, _ := goratelimit.NewLeakyBucket(10, 1, goratelimit.Shaping)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v\n", result.Allowed)
	// Output: allowed=true
}

func ExampleNewGCRA() {
	limiter, _ := goratelimit.NewGCRA(5, 10)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=8
}

func ExampleLimiter_allowN() {
	limiter, _ := goratelimit.NewTokenBucket(10, 1)
	result, _ := limiter.AllowN(context.Background(), "user:123", 3)
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=7
}

func ExampleLimiter_reset() {
	ctx := context.Background()
	limiter, _ := goratelimit.NewFixedWindow(1, 60)
	limiter.Allow(ctx, "user:123")

	result, _ := limiter.Allow(ctx, "user:123")
	fmt.Printf("before reset: allowed=%v\n", result.Allowed)

	_ = limiter.Reset(ctx, "user:123")
	result, _ = limiter.Allow(ctx, "user:123")
	fmt.Printf("after reset:  allowed=%v\n", result.Allowed)
	// Output:
	// before reset: allowed=false
	// after reset:  allowed=true
}

func ExampleNewBuilder() {
	limiter, _ := goratelimit.NewBuilder().
		SlidingWindowCounter(100, 60*time.Second).
		KeyPrefix("api").
		FailOpen(true).
		Build()

	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=99
}

func ExampleNewCMS() {
	limiter, _ := goratelimit.NewCMS(100, 60, 0.01, 0.001)
	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=99
}

func ExampleNewPreFilter() {
	local, _ := goratelimit.NewCMS(100, 60, 0.01, 0.001)
	precise, _ := goratelimit.NewGCRA(5, 10)
	limiter := goratelimit.NewPreFilter(local, precise)

	result, _ := limiter.Allow(context.Background(), "user:123")
	fmt.Printf("allowed=%v remaining=%d\n", result.Allowed, result.Remaining)
	// Output: allowed=true remaining=8
}

func ExampleCMSMemoryBytes() {
	bytes := goratelimit.CMSMemoryBytes(0.01, 0.001)
	fmt.Printf("memory=%d bytes\n", bytes)
	// Output: memory=30464 bytes
}

func ExampleWithLimitFunc() {
	limiter, _ := goratelimit.NewFixedWindow(5, 60,
		goratelimit.WithLimitFunc(func(ctx context.Context, key string) int64 {
			if key == "premium" {
				return 1000
			}
			return 0
		}),
	)

	ctx := context.Background()
	r1, _ := limiter.Allow(ctx, "premium")
	r2, _ := limiter.Allow(ctx, "free")
	fmt.Printf("premium: limit=%d\nfree:    limit=%d\n", r1.Limit, r2.Limit)
	// Output:
	// premium: limit=1000
	// free:    limit=5
}

func ExampleWithClock() {
	clock := goratelimit.NewFakeClock()
	limiter, _ := goratelimit.NewFixedWindow(2, 60, goratelimit.WithClock(clock))

	ctx := context.Background()
	limiter.Allow(ctx, "k")
	limiter.Allow(ctx, "k")
	r, _ := limiter.Allow(ctx, "k")
	fmt.Printf("before advance: allowed=%v\n", r.Allowed)

	clock.Advance(61 * time.Second)
	r, _ = limiter.Allow(ctx, "k")
	fmt.Printf("after advance:  allowed=%v\n", r.Allowed)
	// Output:
	// before advance: allowed=false
	// after advance:  allowed=true
}
