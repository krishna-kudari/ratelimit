// Package goratelimit provides production-grade rate limiting for Go with
// six algorithms, in-memory and Redis backends, and drop-in middleware for
// net/http, Gin, Echo, Fiber, and gRPC.
//
// # Algorithms
//
//   - Fixed Window Counter — simple, fixed time intervals
//   - Sliding Window Log — precise, stores every timestamp
//   - Sliding Window Counter — weighted approximation, O(1) memory
//   - Token Bucket — steady refill, burst-friendly
//   - Leaky Bucket — constant drain, policing or shaping mode
//   - GCRA — virtual scheduling with sustained rate + burst
//   - Count-Min Sketch — fixed-memory probabilistic pre-filter
//
// # Quick Start
//
//	limiter, err := goratelimit.NewTokenBucket(100, 10)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	result, err := limiter.Allow(ctx, "user:123")
//	if result.Allowed {
//	    // serve request
//	}
//
// # With Redis
//
//	limiter, _ := goratelimit.NewTokenBucket(100, 10,
//	    goratelimit.WithRedis(redisClient),
//	)
//
// # Builder API
//
//	limiter, _ := goratelimit.NewBuilder().
//	    SlidingWindowCounter(100, 60*time.Second).
//	    Redis(client).
//	    Build()
//
// All algorithms implement the [Limiter] interface and return a [Result]
// with Allowed, Remaining, Limit, ResetAt, and RetryAfter fields.
package goratelimit
