// Rate limiting with Redis backend — works with standalone, Cluster, Ring, Sentinel.
// Run: go run ./examples/redis/
// Requires: Redis running on localhost:6379
package main

import (
	"context"
	"fmt"
	"log"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	// ── Standalone Redis ────────────────────────────────────────
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis not available: %v (start redis or skip this example)", err)
	}

	// Any algorithm works with Redis — just add WithRedis(client)
	limiter, _ := goratelimit.NewTokenBucket(10, 2,
		goratelimit.WithRedis(client),
		goratelimit.WithKeyPrefix("myapp"),
	)

	for i := 1; i <= 12; i++ {
		r, _ := limiter.Allow(ctx, "user:42")
		fmt.Printf("  req %2d: allowed=%-5v remaining=%d\n", i, r.Allowed, r.Remaining)
	}

	limiter.Reset(ctx, "user:42")
	fmt.Println("  (reset)")

	// ── Builder API with Redis ──────────────────────────────────
	limiter2, _ := goratelimit.NewBuilder().
		FixedWindow(5, 10).
		Redis(client).
		KeyPrefix("api").
		Build()

	r, _ := limiter2.Allow(ctx, "user:42")
	fmt.Printf("\n  builder: allowed=%v remaining=%d\n", r.Allowed, r.Remaining)
	limiter2.Reset(ctx, "user:42")

	// ── Redis Cluster (hash tag routing) ────────────────────────
	// For Cluster deployments, enable hash tags so multi-key
	// algorithms (sliding window counter) route to the same slot.
	//
	//   clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
	//       Addrs: []string{"node1:6379", "node2:6379", "node3:6379"},
	//   })
	//   limiter, _ := goratelimit.NewSlidingWindowCounter(100, 60,
	//       goratelimit.WithRedis(clusterClient),
	//       goratelimit.WithHashTag(),
	//   )
}
