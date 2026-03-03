package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestRateLimit_AllowsWithinLimit(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(5, 60)
	require.NoError(t, err)

	handler := middleware.RateLimit(limiter, middleware.KeyByIP)(okHandler())

	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code, "request %d", i+1)
		assert.Equal(t, "5", rr.Header().Get("X-RateLimit-Limit"), "request %d: expected X-RateLimit-Limit=5", i+1)
		remaining, _ := strconv.ParseInt(rr.Header().Get("X-RateLimit-Remaining"), 10, 64)
		expected := int64(5 - i - 1)
		assert.Equal(t, expected, remaining, "request %d: expected remaining=%d", i+1, expected)
	}
}

func TestRateLimit_DeniesExceedingLimit(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(3, 60)
	require.NoError(t, err)

	handler := middleware.RateLimit(limiter, middleware.KeyByIP)(okHandler())

	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "request %d should be allowed", i+1)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.RemoteAddr = "10.0.0.1:9999"
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Retry-After"), "expected Retry-After header on 429 response")
	assert.Equal(t, "0", rr.Header().Get("X-RateLimit-Remaining"))
}

func TestRateLimit_DefaultDeniedBody_JSON(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(1, 60)
	require.NoError(t, err)

	handler := middleware.RateLimit(limiter, middleware.KeyByIP)(okHandler())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "100.0.0.1:1111"
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "100.0.0.1:1111"
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "rate limit exceeded", body["error"])
	assert.Equal(t, float64(1), body["limit"])
	assert.Equal(t, float64(0), body["remaining"])
	assert.NotEmpty(t, body["reset_at"])
	assert.NotNil(t, body["retry_after"])
}

func TestRateLimit_SeparateKeysTrackedIndependently(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(2, 60)
	require.NoError(t, err)

	handler := middleware.RateLimit(limiter, middleware.KeyByIP)(okHandler())

	// Exhaust limit for IP 1
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "1.1.1.1:1234"
		handler.ServeHTTP(rr, req)
	}

	// IP 1 should be denied
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.1.1.1:1234"
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code, "IP 1 should be rate limited")

	// IP 2 should still be allowed
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "2.2.2.2:5678"
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code, "IP 2 should not be rate limited")
}

func TestRateLimit_ExcludePaths(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(1, 60)
	require.NoError(t, err)

	handler := middleware.RateLimitWithConfig(middleware.Config{
		Limiter:      limiter,
		KeyFunc:      middleware.KeyByIP,
		ExcludePaths: map[string]bool{"/health": true, "/ready": true},
	})(okHandler())

	// Exhaust the limit
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "3.3.3.3:1111"
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "first request should be allowed")

	// Rate limited on normal path
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "3.3.3.3:1111"
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code, "second request to /api/data should be denied")

	// Excluded paths bypass rate limiting
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "3.3.3.3:1111"
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code, "/health should bypass rate limiting")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/ready", nil)
	req.RemoteAddr = "3.3.3.3:1111"
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code, "/ready should bypass rate limiting")
}

func TestRateLimit_CustomDeniedHandler(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(1, 60)
	require.NoError(t, err)

	customCalled := false
	handler := middleware.RateLimitWithConfig(middleware.Config{
		Limiter: limiter,
		KeyFunc: middleware.KeyByIP,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request, result *goratelimit.Result) {
			customCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"custom rate limit message"}`))
		},
	})(okHandler())

	// Exhaust limit
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "4.4.4.4:1111"
	handler.ServeHTTP(rr, req)

	// Trigger custom handler
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "4.4.4.4:1111"
	handler.ServeHTTP(rr, req)

	assert.True(t, customCalled, "custom denied handler should have been called")
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"), "custom handler should set Content-Type to application/json")
}

func TestRateLimit_HeadersDisabled(t *testing.T) {
	limiter, err := goratelimit.NewFixedWindow(5, 60)
	require.NoError(t, err)

	noHeaders := false
	handler := middleware.RateLimitWithConfig(middleware.Config{
		Limiter: limiter,
		KeyFunc: middleware.KeyByIP,
		Headers: &noHeaders,
	})(okHandler())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "5.5.5.5:1111"
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "request should be allowed")
	assert.Empty(t, rr.Header().Get("X-RateLimit-Limit"), "X-RateLimit-Limit should not be set when headers disabled")
	assert.Empty(t, rr.Header().Get("X-RateLimit-Remaining"), "X-RateLimit-Remaining should not be set when headers disabled")
}

func TestKeyByIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 70.41.3.18, 150.172.238.178")
	req.RemoteAddr = "127.0.0.1:1234"

	key := middleware.KeyByIP(req)
	assert.Equal(t, "203.0.113.50", key, "expected first IP from X-Forwarded-For")
}

func TestKeyByIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "198.51.100.42")
	req.RemoteAddr = "127.0.0.1:1234"

	key := middleware.KeyByIP(req)
	assert.Equal(t, "198.51.100.42", key, "expected X-Real-IP value")
}

func TestKeyByIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.100:54321"

	key := middleware.KeyByIP(req)
	assert.Equal(t, "192.168.1.100", key, "expected RemoteAddr IP")
}

func TestKeyByHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "sk-test-12345")

	keyFunc := middleware.KeyByHeader("X-API-Key")
	key := keyFunc(req)
	assert.Equal(t, "sk-test-12345", key, "expected header value")
}

func TestKeyByPathAndIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.RemoteAddr = "10.0.0.5:8080"

	key := middleware.KeyByPathAndIP(req)
	assert.Equal(t, "/api/users:10.0.0.5", key, "expected path:ip")
}

func TestRateLimit_DifferentAlgorithms(t *testing.T) {
	algorithms := []struct {
		name    string
		limiter goratelimit.Limiter
	}{
		{"GCRA", mustLimiter(goratelimit.NewGCRA(100, 3))},
		{"TokenBucket", mustLimiter(goratelimit.NewTokenBucket(3, 1))},
		{"FixedWindow", mustLimiter(goratelimit.NewFixedWindow(3, 60))},
		{"SlidingWindowCounter", mustLimiter(goratelimit.NewSlidingWindowCounter(3, 60))},
	}

	for _, alg := range algorithms {
		t.Run(alg.name, func(t *testing.T) {
			handler := middleware.RateLimit(alg.limiter, middleware.KeyByIP)(okHandler())

			for i := 0; i < 3; i++ {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "9.9.9.9:1111"
				handler.ServeHTTP(rr, req)
				assert.Equal(t, http.StatusOK, rr.Code, "%s: request %d should be allowed", alg.name, i+1)
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "9.9.9.9:1111"
			handler.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusTooManyRequests, rr.Code, "%s: 4th request should be denied", alg.name)
		})
	}
}

func mustLimiter(l goratelimit.Limiter, err error) goratelimit.Limiter {
	if err != nil {
		panic(err)
	}
	return l
}
