package goratelimit

import (
	"io"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	// Suppress [DRYRUN] and other log output during tests and benchmarks.
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func TestFormatKey_Plain(t *testing.T) {
	o := defaultOptions()
	got := o.FormatKey("user:123")
	want := "ratelimit:user:123"
	assert.Equal(t, want, got)
}

func TestFormatKey_HashTag(t *testing.T) {
	o := defaultOptions()
	o.HashTag = true
	got := o.FormatKey("user:123")
	want := "ratelimit:{user:123}"
	assert.Equal(t, want, got)
}

func TestFormatKeySuffix_Plain(t *testing.T) {
	o := defaultOptions()
	got := o.FormatKeySuffix("user:123", "42")
	want := "ratelimit:user:123:42"
	assert.Equal(t, want, got)
}

func TestFormatKeySuffix_HashTag(t *testing.T) {
	o := defaultOptions()
	o.HashTag = true
	got := o.FormatKeySuffix("user:123", "42")
	want := "ratelimit:{user:123}:42"
	assert.Equal(t, want, got)
}

func TestFormatKeySuffix_HashTag_SlotConsistency(t *testing.T) {
	o := defaultOptions()
	o.HashTag = true

	k1 := o.FormatKeySuffix("user:123", "100")
	k2 := o.FormatKeySuffix("user:123", "101")

	tag1 := extractHashTag(k1)
	tag2 := extractHashTag(k2)
	assert.Equal(t, tag2, tag1, "hash tags differ for keys: %q, %q", k1, k2)
	assert.Equal(t, "user:123", tag1)
}

func TestWithHashTag_Option(t *testing.T) {
	o := applyOptions([]Option{WithHashTag()})
	assert.True(t, o.HashTag, "WithHashTag should set HashTag to true")
	got := o.FormatKey("ip:10.0.0.1")
	want := "ratelimit:{ip:10.0.0.1}"
	assert.Equal(t, want, got)
}

func TestFormatKey_CustomPrefix_HashTag(t *testing.T) {
	o := applyOptions([]Option{WithKeyPrefix("myapp"), WithHashTag()})
	got := o.FormatKey("api-key-abc")
	want := "myapp:{api-key-abc}"
	assert.Equal(t, want, got)
}

// extractHashTag returns the content between the first { and the next }.
func extractHashTag(key string) string {
	start := -1
	for i, c := range key {
		if c == '{' {
			start = i + 1
		} else if c == '}' && start >= 0 {
			return key[start:i]
		}
	}
	return ""
}
