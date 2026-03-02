// Complete Echo server with rate limiting middleware.
// Run: go run ./examples/echoserver/
// Test: curl -i http://localhost:8080/api/hello
package main

import (
	"net/http"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/middleware/echomw"
	"github.com/labstack/echo/v4"
)

func main() {
	limiter, _ := goratelimit.NewTokenBucket(5, 1)

	e := echo.New()
	e.Use(echomw.RateLimit(limiter, echomw.KeyByRealIP))

	e.GET("/api/hello", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"message": "hello"})
	})

	e.Logger.Fatal(e.Start(":8080"))
}
