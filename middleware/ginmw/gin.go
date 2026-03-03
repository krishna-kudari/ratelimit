// Package ginmw provides Gin middleware for rate limiting.
//
// Separated from the middleware package so that importing the HTTP middleware
// does not pull in github.com/gin-gonic/gin.
//
// Usage:
//
//	limiter, _ := goratelimit.NewGCRA(1000, 50, goratelimit.WithRedis(client))
//	r := gin.Default()
//	r.Use(ginmw.RateLimit(limiter, ginmw.KeyByClientIP))
package ginmw

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware"
)

// KeyFunc extracts the rate limiting key from a Gin context.
type KeyFunc func(c *gin.Context) string

// DeniedHandler is called when a request is rate limited.
type DeniedHandler func(c *gin.Context, result *goratelimit.Result)

// ErrorHandler is called when the limiter returns an error.
type ErrorHandler func(c *gin.Context, err error)

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
	BypassFunc func(c *gin.Context) bool

	// Allowlist is a list of CIDR blocks. Requests whose client IP is in any block skip rate limiting.
	Allowlist []string

	// Headers controls whether X-RateLimit-* headers are set.
	// Default: true.
	Headers *bool
}

// RateLimit creates Gin middleware with default settings.
func RateLimit(limiter goratelimit.Limiter, keyFunc KeyFunc) gin.HandlerFunc {
	return RateLimitWithConfig(Config{
		Limiter: limiter,
		KeyFunc: keyFunc,
	})
}

// RateLimitWithConfig creates Gin middleware with full configuration control.
func RateLimitWithConfig(cfg Config) gin.HandlerFunc {
	if cfg.Limiter == nil {
		panic("ginmw: Limiter is required")
	}
	if cfg.KeyFunc == nil {
		panic("ginmw: KeyFunc is required")
	}
	if cfg.DeniedHandler == nil {
		cfg.DeniedHandler = defaultDeniedHandler
	}
	if cfg.ErrorHandler == nil {
		cfg.ErrorHandler = defaultErrorHandler
	}
	sendHeaders := cfg.Headers == nil || *cfg.Headers
	allowlistNets := middleware.ParseAllowlistCIDRs(cfg.Allowlist)

	return func(c *gin.Context) {
		if cfg.ExcludePaths != nil && cfg.ExcludePaths[c.Request.URL.Path] {
			c.Next()
			return
		}
		if cfg.BypassFunc != nil && cfg.BypassFunc(c) {
			c.Next()
			return
		}
		if len(allowlistNets) > 0 && middleware.IPInAllowlist(c.ClientIP(), allowlistNets) {
			c.Next()
			return
		}

		key := cfg.KeyFunc(c)
		result, err := cfg.Limiter.Allow(c.Request.Context(), key)
		if err != nil {
			cfg.ErrorHandler(c, err)
			return
		}

		if sendHeaders {
			setHeaders(c, &result)
		}

		if !result.Allowed {
			if result.RetryAfter > 0 {
				c.Header("Retry-After", strconv.FormatInt(int64(result.RetryAfter.Seconds()+0.5), 10))
			}
			cfg.DeniedHandler(c, &result)
			return
		}

		c.Next()
	}
}

// ─── Built-in Key Extractors ─────────────────────────────────────────────────

// KeyByClientIP uses Gin's ClientIP() which respects trusted proxies.
func KeyByClientIP(c *gin.Context) string {
	return c.ClientIP()
}

// KeyByHeader returns a KeyFunc that extracts from a request header.
func KeyByHeader(header string) KeyFunc {
	return func(c *gin.Context) string {
		return c.GetHeader(header)
	}
}

// KeyByAPIKey extracts the rate limit key from the Authorization header.
// Use for API key or Bearer token rate limiting.
func KeyByAPIKey(c *gin.Context) string {
	return c.GetHeader("Authorization")
}

// KeyByPath returns the route path as the rate limit key (per-endpoint limiting).
func KeyByPath(c *gin.Context) string {
	return c.FullPath()
}

// KeyByUser returns a KeyFunc that reads the user from the Gin context.
// Set the user in context in your auth middleware (e.g. after JWT); use the same key here.
func KeyByUser(key string) KeyFunc {
	return func(c *gin.Context) string {
		v, ok := c.Get(key)
		if !ok {
			return ""
		}
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
}

// KeyByParam returns a KeyFunc that extracts from a URL parameter.
func KeyByParam(param string) KeyFunc {
	return func(c *gin.Context) string {
		return c.Param(param)
	}
}

// KeyByPathAndIP combines the request path and client IP.
func KeyByPathAndIP(c *gin.Context) string {
	return c.FullPath() + ":" + c.ClientIP()
}

// ─── Internals ───────────────────────────────────────────────────────────────

func setHeaders(c *gin.Context, result *goratelimit.Result) {
	c.Header("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
	if !result.ResetAt.IsZero() {
		c.Header("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	}
}

func defaultDeniedHandler(c *gin.Context, result *goratelimit.Result) {
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":       "rate limit exceeded",
		"limit":       result.Limit,
		"remaining":   result.Remaining,
		"reset_at":    result.ResetAt.UTC().Format(time.RFC3339),
		"retry_after": int(result.RetryAfter.Seconds() + 0.5),
	})
}

func defaultErrorHandler(c *gin.Context, _ error) {
	c.Next()
}
