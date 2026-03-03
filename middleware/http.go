package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// KeyFunc extracts the rate limiting key from an HTTP request.
// The returned string identifies the caller (e.g. IP, API key, user ID).
type KeyFunc func(r *http.Request) string

// ErrorHandler is called when the limiter returns an error.
// Default behavior: 500 Internal Server Error.
type ErrorHandler func(w http.ResponseWriter, r *http.Request, err error)

// DeniedHandler is called when a request is rate limited.
// Default behavior: 429 Too Many Requests with Retry-After header.
type DeniedHandler func(w http.ResponseWriter, r *http.Request, result *goratelimit.Result)

// Config holds the rate limit middleware configuration.
type Config struct {
	// Limiter is the rate limiter instance (required).
	Limiter goratelimit.Limiter

	// KeyFunc extracts the rate limit key from the request (required).
	KeyFunc KeyFunc

	// ErrorHandler is called when the limiter returns an error.
	// Default: responds with 500.
	ErrorHandler ErrorHandler

	// DeniedHandler is called when a request is denied.
	// Default: responds with 429 and Retry-After header.
	DeniedHandler DeniedHandler

	// ExcludePaths are request paths that bypass rate limiting.
	ExcludePaths map[string]bool

	// BypassFunc, when non-nil, is called per request. If it returns true, the request skips rate limiting.
	BypassFunc BypassFunc

	// Allowlist is a list of CIDR blocks (e.g. "10.0.0.0/8"). Requests whose client IP is in any block skip rate limiting.
	Allowlist []string

	// Headers controls whether X-RateLimit-* headers are set on responses.
	// Default: true.
	Headers *bool

	// Message is the response body for denied requests.
	// Default: "Too Many Requests".
	Message string

	// StatusCode is the HTTP status code for denied requests.
	// Default: 429.
	StatusCode int
}

// RateLimit creates HTTP middleware with default settings.
// It sets standard rate limit headers and returns 429 on denial.
//
// Usage with net/http:
//
//	mux := http.NewServeMux()
//	mux.Handle("/api/", middleware.RateLimit(limiter, middleware.KeyByIP)(handler))
//
// Usage with chi:
//
//	r := chi.NewRouter()
//	r.Use(middleware.RateLimit(limiter, middleware.KeyByIP))
func RateLimit(limiter goratelimit.Limiter, keyFunc KeyFunc) func(http.Handler) http.Handler {
	return RateLimitWithConfig(Config{
		Limiter: limiter,
		KeyFunc: keyFunc,
	})
}

// RateLimitWithConfig creates HTTP middleware with full configuration control.
func RateLimitWithConfig(cfg Config) func(http.Handler) http.Handler {
	if cfg.Limiter == nil {
		panic("goratelimit/middleware: Limiter is required")
	}
	if cfg.KeyFunc == nil {
		panic("goratelimit/middleware: KeyFunc is required")
	}
	if cfg.ErrorHandler == nil {
		cfg.ErrorHandler = defaultErrorHandler
	}
	if cfg.DeniedHandler == nil {
		cfg.DeniedHandler = defaultDeniedHandler(cfg.Message, cfg.StatusCode)
	}
	sendHeaders := cfg.Headers == nil || *cfg.Headers

	allowlistNets := ParseAllowlistCIDRs(cfg.Allowlist)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.ExcludePaths != nil && cfg.ExcludePaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if cfg.BypassFunc != nil && cfg.BypassFunc(r) {
				next.ServeHTTP(w, r)
				return
			}
			if len(allowlistNets) > 0 && IPInAllowlist(KeyByIP(r), allowlistNets) {
				next.ServeHTTP(w, r)
				return
			}

			key := cfg.KeyFunc(r)
			result, err := cfg.Limiter.Allow(r.Context(), key)
			if err != nil {
				cfg.ErrorHandler(w, r, err)
				return
			}

			if sendHeaders {
				setRateLimitHeaders(w, &result)
			}

			if !result.Allowed {
				if result.RetryAfter > 0 {
					w.Header().Set("Retry-After", strconv.FormatInt(int64(result.RetryAfter.Seconds()+0.5), 10))
				}
				cfg.DeniedHandler(w, r, &result)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ─── Built-in Key Extractors ─────────────────────────────────────────────────

// KeyByIP extracts the client IP address as the rate limit key.
// It checks X-Forwarded-For, X-Real-IP, then falls back to RemoteAddr.
func KeyByIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// KeyByHeader returns a KeyFunc that uses the value of the given header.
// Useful for API key-based rate limiting.
func KeyByHeader(header string) KeyFunc {
	return func(r *http.Request) string {
		return r.Header.Get(header)
	}
}

// KeyByAPIKey extracts the rate limit key from the Authorization header.
// Use for API key or Bearer token rate limiting; each distinct header value is a separate key.
func KeyByAPIKey(r *http.Request) string {
	return r.Header.Get("Authorization")
}

// KeyByPath returns the request path as the rate limit key.
// Use for per-endpoint (per-route) rate limiting shared across all clients.
func KeyByPath(r *http.Request) string {
	return r.URL.Path
}

// KeyByUser returns a KeyFunc that reads the user identifier from the request context.
// The key should be the same value used when setting the user in context (e.g. after JWT auth).
// If the value is not in context, the empty string is returned.
func KeyByUser(contextKey interface{}) KeyFunc {
	return func(r *http.Request) string {
		v := r.Context().Value(contextKey)
		if v == nil {
			return ""
		}
		switch s := v.(type) {
		case string:
			return s
		case fmt.Stringer:
			return s.String()
		default:
			return fmt.Sprintf("%v", v)
		}
	}
}

// KeyByPathAndIP returns a KeyFunc that combines the request path and client IP.
// Useful for per-endpoint rate limiting.
func KeyByPathAndIP(r *http.Request) string {
	return r.URL.Path + ":" + KeyByIP(r)
}

// ─── Headers ─────────────────────────────────────────────────────────────────

func setRateLimitHeaders(w http.ResponseWriter, result *goratelimit.Result) {
	w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
	if !result.ResetAt.IsZero() {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	}
}

// ─── Default Handlers ────────────────────────────────────────────────────────

func defaultErrorHandler(w http.ResponseWriter, _ *http.Request, _ error) {
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func defaultDeniedHandler(message string, statusCode int) DeniedHandler {
	if message == "" {
		message = "rate limit exceeded"
	}
	if statusCode == 0 {
		statusCode = http.StatusTooManyRequests
	}
	return func(w http.ResponseWriter, _ *http.Request, result *goratelimit.Result) {
		retryAfter := int(result.RetryAfter.Seconds() + 0.5)
		body := deniedBody{
			Error:      message,
			Limit:      result.Limit,
			Remaining:  result.Remaining,
			ResetAt:    result.ResetAt.UTC().Format(time.RFC3339),
			RetryAfter: retryAfter,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(body)
	}
}

type deniedBody struct {
	Error      string `json:"error"`
	Limit      int64  `json:"limit"`
	Remaining  int64  `json:"remaining"`
	ResetAt    string `json:"reset_at"`
	RetryAfter int    `json:"retry_after"`
}
