package fibermw_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware/fibermw"
)

func newApp(mw fiber.Handler) *fiber.App {
	app := fiber.New()
	app.Use(mw)
	app.Get("/api/data", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/health", func(c *fiber.Ctx) error { return c.SendString("ok") })
	return app
}

func doReq(app *fiber.App, method, path string, headers map[string]string) *http.Response {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, _ := app.Test(req, -1)
	return resp
}

func TestRateLimit_AllowsWithinLimit(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(5, 60))
	app := newApp(fibermw.RateLimit(limiter, fibermw.KeyByIP))

	for i := 0; i < 5; i++ {
		resp := doReq(app, "GET", "/api/data", nil)
		require.Equal(t, 200, resp.StatusCode, "request %d: expected 200", i+1)
		assert.Equal(t, "5", resp.Header.Get("X-RateLimit-Limit"), "request %d: expected limit=5", i+1)
	}
}

func TestRateLimit_DeniesExceedingLimit(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(2, 60))
	app := newApp(fibermw.RateLimit(limiter, fibermw.KeyByIP))

	for i := 0; i < 2; i++ {
		doReq(app, "GET", "/api/data", nil)
	}

	resp := doReq(app, "GET", "/api/data", nil)
	require.Equal(t, 429, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Retry-After"), "expected Retry-After header")
}

func TestRateLimit_DefaultDeniedBody_JSON(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	app := newApp(fibermw.RateLimit(limiter, fibermw.KeyByIP))

	doReq(app, "GET", "/api/data", nil)
	resp := doReq(app, "GET", "/api/data", nil)

	require.Equal(t, 429, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "rate limit exceeded", body["error"])
	assert.Equal(t, float64(1), body["limit"])
	assert.Equal(t, float64(0), body["remaining"])
	assert.NotEmpty(t, body["reset_at"])
	assert.NotNil(t, body["retry_after"])
}

func TestRateLimit_ExcludePaths(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	app := newApp(fibermw.RateLimitWithConfig(fibermw.Config{
		Limiter:      limiter,
		KeyFunc:      fibermw.KeyByIP,
		ExcludePaths: map[string]bool{"/health": true},
	}))

	doReq(app, "GET", "/api/data", nil)

	resp := doReq(app, "GET", "/health", nil)
	assert.Equal(t, 200, resp.StatusCode, "health should bypass")
}

func TestRateLimit_CustomDeniedHandler(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	customCalled := false
	app := newApp(fibermw.RateLimitWithConfig(fibermw.Config{
		Limiter: limiter,
		KeyFunc: fibermw.KeyByIP,
		DeniedHandler: func(c *fiber.Ctx, _ *goratelimit.Result) error {
			customCalled = true
			return c.Status(429).JSON(fiber.Map{"custom": true})
		},
	}))

	doReq(app, "GET", "/api/data", nil)
	doReq(app, "GET", "/api/data", nil)

	assert.True(t, customCalled, "custom denied handler should be called")
}

func TestRateLimit_HeadersDisabled(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(5, 60))
	noHeaders := false
	app := newApp(fibermw.RateLimitWithConfig(fibermw.Config{
		Limiter: limiter,
		KeyFunc: fibermw.KeyByIP,
		Headers: &noHeaders,
	}))

	resp := doReq(app, "GET", "/api/data", nil)
	assert.Empty(t, resp.Header.Get("X-RateLimit-Limit"), "headers should not be set")
}

func TestKeyByHeader(t *testing.T) {
	limiter := must(goratelimit.NewFixedWindow(1, 60))
	app := newApp(fibermw.RateLimit(limiter, fibermw.KeyByHeader("X-API-Key")))

	resp := doReq(app, "GET", "/api/data", map[string]string{"X-API-Key": "key-A"})
	require.Equal(t, 200, resp.StatusCode, "key-A should be allowed")

	resp = doReq(app, "GET", "/api/data", map[string]string{"X-API-Key": "key-A"})
	require.Equal(t, 429, resp.StatusCode, "key-A should be denied")

	resp = doReq(app, "GET", "/api/data", map[string]string{"X-API-Key": "key-B"})
	require.Equal(t, 200, resp.StatusCode, "key-B should be allowed")
}

func must(l goratelimit.Limiter, err error) goratelimit.Limiter {
	if err != nil {
		panic(err)
	}
	return l
}
