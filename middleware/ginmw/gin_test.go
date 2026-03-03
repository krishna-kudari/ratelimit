package ginmw_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware/ginmw"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newRouter(mw gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(mw)
	r.GET("/api/data", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/health", func(c *gin.Context) { c.String(200, "ok") })
	return r
}

func TestRateLimit_AllowsWithinLimit(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(5, 60))
	router := newRouter(ginmw.RateLimit(limiter, ginmw.KeyByClientIP))

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/data", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		router.ServeHTTP(w, req)

		require.Equal(t, 200, w.Code, "request %d: expected 200", i+1)
		assert.Equal(t, "5", w.Header().Get("X-RateLimit-Limit"), "request %d: expected limit=5", i+1)
	}
}

func TestRateLimit_DeniesExceedingLimit(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(2, 60))
	router := newRouter(ginmw.RateLimit(limiter, ginmw.KeyByClientIP))

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/data", nil)
		req.RemoteAddr = "5.6.7.8:1234"
		router.ServeHTTP(w, req)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "5.6.7.8:1234"
	router.ServeHTTP(w, req)

	require.Equal(t, 429, w.Code)
	assert.NotEmpty(t, w.Header().Get("Retry-After"), "expected Retry-After header")
}

func TestRateLimit_DefaultDeniedBody_JSON(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	router := newRouter(ginmw.RateLimit(limiter, ginmw.KeyByClientIP))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "100.0.0.1:1234"
	router.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "100.0.0.1:1234"
	router.ServeHTTP(w, req)

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
	router := newRouter(ginmw.RateLimitWithConfig(ginmw.Config{
		Limiter:      limiter,
		KeyFunc:      ginmw.KeyByClientIP,
		ExcludePaths: map[string]bool{"/health": true},
	}))

	// Exhaust limit
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	router.ServeHTTP(w, req)

	// Health should bypass
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	router.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code, "health should bypass")
}

func TestRateLimit_CustomDeniedHandler(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	customCalled := false
	router := newRouter(ginmw.RateLimitWithConfig(ginmw.Config{
		Limiter: limiter,
		KeyFunc: ginmw.KeyByClientIP,
		DeniedHandler: func(c *gin.Context, _ *goratelimit.Result) {
			customCalled = true
			c.AbortWithStatusJSON(429, gin.H{"custom": true})
		},
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "11.0.0.1:1234"
	router.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "11.0.0.1:1234"
	router.ServeHTTP(w, req)

	assert.True(t, customCalled, "custom denied handler should be called")
}

func TestRateLimit_HeadersDisabled(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(5, 60))
	noHeaders := false
	router := newRouter(ginmw.RateLimitWithConfig(ginmw.Config{
		Limiter: limiter,
		KeyFunc: ginmw.KeyByClientIP,
		Headers: &noHeaders,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.RemoteAddr = "12.0.0.1:1234"
	router.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("X-RateLimit-Limit"), "headers should not be set")
}

func TestKeyByHeader(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	router := newRouter(ginmw.RateLimit(limiter, ginmw.KeyByHeader("X-API-Key")))

	// key-A: allowed
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("X-API-Key", "key-A")
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, "key-A should be allowed")

	// key-A: denied
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("X-API-Key", "key-A")
	router.ServeHTTP(w, req)
	require.Equal(t, 429, w.Code, "key-A should be denied")

	// key-B: allowed (different key)
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("X-API-Key", "key-B")
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, "key-B should be allowed")
}

func must(l goratelimit.Limiter, err error) goratelimit.Limiter {
	if err != nil {
		panic(err)
	}
	return l
}
