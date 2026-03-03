package echomw_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware/echomw"
)

func newEcho(mw echo.MiddlewareFunc) *echo.Echo {
	e := echo.New()
	e.Use(mw)
	e.GET("/api/data", func(c echo.Context) error { return c.String(200, "ok") })
	e.GET("/health", func(c echo.Context) error { return c.String(200, "ok") })
	return e
}

func TestRateLimit_AllowsWithinLimit(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(5, 60))
	e := newEcho(echomw.RateLimit(limiter, echomw.KeyByRealIP))

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/data", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		e.ServeHTTP(w, req)

		require.Equal(t, 200, w.Code, "request %d: expected 200", i+1)
		assert.Equal(t, "5", w.Header().Get("X-RateLimit-Limit"), "request %d: expected limit=5", i+1)
	}
}

func TestRateLimit_DeniesExceedingLimit(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(2, 60))
	e := newEcho(echomw.RateLimit(limiter, echomw.KeyByRealIP))

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/data", nil)
		req.RemoteAddr = "5.6.7.8:1234"
		e.ServeHTTP(w, req)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "5.6.7.8:1234"
	e.ServeHTTP(w, req)

	require.Equal(t, 429, w.Code)
	assert.NotEmpty(t, w.Header().Get("Retry-After"), "expected Retry-After header")
}

func TestRateLimit_DefaultDeniedBody_JSON(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	e := newEcho(echomw.RateLimit(limiter, echomw.KeyByRealIP))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "100.0.0.1:1234"
	e.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "100.0.0.1:1234"
	e.ServeHTTP(w, req)

	require.Equal(t, 429, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")

	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "rate limit exceeded", body["error"])
	assert.Equal(t, float64(1), body["limit"])
	assert.Equal(t, float64(0), body["remaining"])
	assert.NotEmpty(t, body["reset_at"])
	assert.NotNil(t, body["retry_after"])
}

func TestRateLimit_ExcludePaths(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	e := newEcho(echomw.RateLimitWithConfig(echomw.Config{
		Limiter:      limiter,
		KeyFunc:      echomw.KeyByRealIP,
		ExcludePaths: map[string]bool{"/health": true},
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	e.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	e.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code, "health should bypass")
}

func TestRateLimit_CustomDeniedHandler(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	customCalled := false
	e := newEcho(echomw.RateLimitWithConfig(echomw.Config{
		Limiter: limiter,
		KeyFunc: echomw.KeyByRealIP,
		DeniedHandler: func(c echo.Context, _ *goratelimit.Result) error {
			customCalled = true
			return c.JSON(429, map[string]bool{"custom": true})
		},
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "11.0.0.1:1234"
	e.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "11.0.0.1:1234"
	e.ServeHTTP(w, req)

	assert.True(t, customCalled, "custom denied handler should be called")
}

func TestRateLimit_HeadersDisabled(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(5, 60))
	noHeaders := false
	e := newEcho(echomw.RateLimitWithConfig(echomw.Config{
		Limiter: limiter,
		KeyFunc: echomw.KeyByRealIP,
		Headers: &noHeaders,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "12.0.0.1:1234"
	e.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("X-RateLimit-Limit"), "headers should not be set")
}

func TestKeyByHeader(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	e := newEcho(echomw.RateLimit(limiter, echomw.KeyByHeader("X-API-Key")))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("X-API-Key", "key-A")
	e.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, "key-A should be allowed")

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("X-API-Key", "key-A")
	e.ServeHTTP(w, req)
	require.Equal(t, 429, w.Code, "key-A should be denied")

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("X-API-Key", "key-B")
	e.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, "key-B should be allowed")
}

func must(l goratelimit.Limiter, err error) goratelimit.Limiter {
	if err != nil {
		panic(err)
	}
	return l
}
