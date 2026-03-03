// Package fibermw provides Fiber middleware for rate limiting.
//
// Separated from the middleware package so that importing the HTTP middleware
// does not pull in github.com/gofiber/fiber. Fiber uses fasthttp (not net/http),
// so a dedicated adapter is required.
//
// Usage:
//
//	limiter, _ := goratelimit.NewGCRA(1000, 50, goratelimit.WithRedis(client))
//	app := fiber.New()
//	app.Use(fibermw.RateLimit(limiter, fibermw.KeyByIP))
package fibermw

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware"
)

// KeyFunc extracts the rate limiting key from a Fiber context.
type KeyFunc func(c *fiber.Ctx) string

// DeniedHandler is called when a request is rate limited.
type DeniedHandler func(c *fiber.Ctx, result *goratelimit.Result) error

// ErrorHandler is called when the limiter returns an error.
type ErrorHandler func(c *fiber.Ctx, err error) error

// Config holds the rate limit middleware configuration.
type Config struct {
	// Limiter is the rate limiter instance (required).
	Limiter goratelimit.Limiter

	// KeyFunc extracts the rate limit key (required).
	KeyFunc KeyFunc

	// DeniedHandler is called on denial. Default: 429 JSON.
	DeniedHandler DeniedHandler

	// ErrorHandler is called on limiter error. Default: pass-through (fail open).
	ErrorHandler ErrorHandler

	// ExcludePaths are request paths that bypass rate limiting.
	ExcludePaths map[string]bool

	// BypassFunc, when non-nil, is called per request. If it returns true, the request skips rate limiting.
	BypassFunc func(c *fiber.Ctx) bool

	// Allowlist is a list of CIDR blocks. Requests whose client IP is in any block skip rate limiting.
	Allowlist []string

	// Headers controls whether X-RateLimit-* headers are set.
	// Default: true.
	Headers *bool
}

// RateLimit creates Fiber middleware with default settings.
func RateLimit(limiter goratelimit.Limiter, keyFunc KeyFunc) fiber.Handler {
	return RateLimitWithConfig(Config{
		Limiter: limiter,
		KeyFunc: keyFunc,
	})
}

// RateLimitWithConfig creates Fiber middleware with full configuration control.
func RateLimitWithConfig(cfg Config) fiber.Handler {
	if cfg.Limiter == nil {
		panic("fibermw: Limiter is required")
	}
	if cfg.KeyFunc == nil {
		panic("fibermw: KeyFunc is required")
	}
	if cfg.DeniedHandler == nil {
		cfg.DeniedHandler = defaultDeniedHandler
	}
	if cfg.ErrorHandler == nil {
		cfg.ErrorHandler = defaultErrorHandler
	}
	sendHeaders := cfg.Headers == nil || *cfg.Headers
	allowlistNets := middleware.ParseAllowlistCIDRs(cfg.Allowlist)

	return func(c *fiber.Ctx) error {
		if cfg.ExcludePaths != nil && cfg.ExcludePaths[c.Path()] {
			return c.Next()
		}
		if cfg.BypassFunc != nil && cfg.BypassFunc(c) {
			return c.Next()
		}
		if len(allowlistNets) > 0 && middleware.IPInAllowlist(c.IP(), allowlistNets) {
			return c.Next()
		}

		key := cfg.KeyFunc(c)
		result, err := cfg.Limiter.Allow(c.UserContext(), key)
		if err != nil {
			return cfg.ErrorHandler(c, err)
		}

		if sendHeaders {
			setHeaders(c, &result)
		}

		if !result.Allowed {
			if result.RetryAfter > 0 {
				c.Set("Retry-After", strconv.FormatInt(int64(result.RetryAfter.Seconds()+0.5), 10))
			}
			return cfg.DeniedHandler(c, &result)
		}

		return c.Next()
	}
}

// ─── Built-in Key Extractors ─────────────────────────────────────────────────

// KeyByIP uses Fiber's IP() method which respects proxy headers.
func KeyByIP(c *fiber.Ctx) string {
	return c.IP()
}

// KeyByHeader returns a KeyFunc that extracts from a request header.
func KeyByHeader(header string) KeyFunc {
	return func(c *fiber.Ctx) string {
		return c.Get(header)
	}
}

// KeyByAPIKey extracts the rate limit key from the Authorization header.
// Use for API key or Bearer token rate limiting.
func KeyByAPIKey(c *fiber.Ctx) string {
	return c.Get("Authorization")
}

// KeyByPath returns the route path as the rate limit key (per-endpoint limiting).
func KeyByPath(c *fiber.Ctx) string {
	return c.Path()
}

// KeyByUser returns a KeyFunc that reads the user from Fiber's Locals.
// Set the user in a prior middleware (e.g. after JWT); use the same key here.
func KeyByUser(key string) KeyFunc {
	return func(c *fiber.Ctx) string {
		v := c.Locals(key)
		if v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
}

// KeyByParam returns a KeyFunc that extracts from a route parameter.
func KeyByParam(param string) KeyFunc {
	return func(c *fiber.Ctx) string {
		return c.Params(param)
	}
}

// KeyByPathAndIP combines the request path and client IP.
func KeyByPathAndIP(c *fiber.Ctx) string {
	return c.Path() + ":" + c.IP()
}

// ─── Internals ───────────────────────────────────────────────────────────────

func setHeaders(c *fiber.Ctx, result *goratelimit.Result) {
	c.Set("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
	c.Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
	if !result.ResetAt.IsZero() {
		c.Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	}
}

func defaultDeniedHandler(c *fiber.Ctx, result *goratelimit.Result) error {
	return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
		"error":       "rate limit exceeded",
		"limit":       result.Limit,
		"remaining":   result.Remaining,
		"reset_at":    result.ResetAt.UTC().Format(time.RFC3339),
		"retry_after": int(result.RetryAfter.Seconds() + 0.5),
	})
}

func defaultErrorHandler(c *fiber.Ctx, _ error) error {
	return c.Next()
}
