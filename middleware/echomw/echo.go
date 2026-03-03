// Package echomw provides Echo middleware for rate limiting.
//
// Separated from the middleware package so that importing the HTTP middleware
// does not pull in github.com/labstack/echo.
//
// Usage:
//
//	limiter, _ := goratelimit.NewGCRA(1000, 50, goratelimit.WithRedis(client))
//	e := echo.New()
//	e.Use(echomw.RateLimit(limiter, echomw.KeyByRealIP))
package echomw

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// KeyFunc extracts the rate limiting key from an Echo context.
type KeyFunc func(c echo.Context) string

// DeniedHandler is called when a request is rate limited.
type DeniedHandler func(c echo.Context, result *goratelimit.Result) error

// ErrorHandler is called when the limiter returns an error.
type ErrorHandler func(c echo.Context, err error) error

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

	// Headers controls whether X-RateLimit-* headers are set.
	// Default: true.
	Headers *bool
}

// RateLimit creates Echo middleware with default settings.
func RateLimit(limiter goratelimit.Limiter, keyFunc KeyFunc) echo.MiddlewareFunc {
	return RateLimitWithConfig(Config{
		Limiter: limiter,
		KeyFunc: keyFunc,
	})
}

// RateLimitWithConfig creates Echo middleware with full configuration control.
func RateLimitWithConfig(cfg Config) echo.MiddlewareFunc {
	if cfg.Limiter == nil {
		panic("echomw: Limiter is required")
	}
	if cfg.KeyFunc == nil {
		panic("echomw: KeyFunc is required")
	}
	if cfg.DeniedHandler == nil {
		cfg.DeniedHandler = defaultDeniedHandler
	}
	if cfg.ErrorHandler == nil {
		cfg.ErrorHandler = defaultErrorHandler
	}
	sendHeaders := cfg.Headers == nil || *cfg.Headers

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if cfg.ExcludePaths != nil && cfg.ExcludePaths[c.Request().URL.Path] {
				return next(c)
			}

			key := cfg.KeyFunc(c)
			result, err := cfg.Limiter.Allow(c.Request().Context(), key)
			if err != nil {
				return cfg.ErrorHandler(c, err)
			}

			if sendHeaders {
				setHeaders(c, result)
			}

			if !result.Allowed {
				if result.RetryAfter > 0 {
					c.Response().Header().Set("Retry-After",
						strconv.FormatInt(int64(result.RetryAfter.Seconds()+0.5), 10))
				}
				return cfg.DeniedHandler(c, result)
			}

			return next(c)
		}
	}
}

// ─── Built-in Key Extractors ─────────────────────────────────────────────────

// KeyByRealIP uses Echo's RealIP() which respects X-Forwarded-For / X-Real-IP.
func KeyByRealIP(c echo.Context) string {
	return c.RealIP()
}

// KeyByHeader returns a KeyFunc that extracts from a request header.
func KeyByHeader(header string) KeyFunc {
	return func(c echo.Context) string {
		return c.Request().Header.Get(header)
	}
}

// KeyByParam returns a KeyFunc that extracts from a path parameter.
func KeyByParam(param string) KeyFunc {
	return func(c echo.Context) string {
		return c.Param(param)
	}
}

// KeyByPathAndIP combines the request path and real IP.
func KeyByPathAndIP(c echo.Context) string {
	return c.Path() + ":" + c.RealIP()
}

// ─── Internals ───────────────────────────────────────────────────────────────

func setHeaders(c echo.Context, result *goratelimit.Result) {
	h := c.Response().Header()
	h.Set("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
	h.Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
	if !result.ResetAt.IsZero() {
		h.Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	}
}

func defaultDeniedHandler(c echo.Context, result *goratelimit.Result) error {
	return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
		"error":       "rate limit exceeded",
		"limit":       result.Limit,
		"remaining":   result.Remaining,
		"reset_at":    result.ResetAt.UTC().Format(time.RFC3339),
		"retry_after": int(result.RetryAfter.Seconds() + 0.5),
	})
}

func defaultErrorHandler(c echo.Context, err error) error {
	return nil
}
