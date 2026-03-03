package goratelimit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDryRun_NeverDenies(t *testing.T) {
	ctx := context.Background()
	l, err := NewFixedWindow(2, 60, WithDryRun(true))
	require.NoError(t, err)

	// Exhaust limit
	for i := 0; i < 2; i++ {
		res, err := l.Allow(ctx, "key")
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d", i+1)
	}
	// Would be denied without dry run; with dry run still allowed
	res, err := l.Allow(ctx, "key")
	require.NoError(t, err)
	assert.True(t, res.Allowed, "dry run should allow over limit")
	assert.Equal(t, int64(0), res.Remaining)
	assert.Equal(t, int64(2), res.Limit)
}

func TestDryRun_LogFuncCalledWhenWouldDeny(t *testing.T) {
	ctx := context.Background()
	var loggedKey string
	var loggedResult *Result
	l, err := NewFixedWindow(2, 60,
		WithDryRun(true),
		WithDryRunLogFunc(func(key string, result *Result) {
			loggedKey = key
			loggedResult = result
		}),
	)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		_, _ = l.Allow(ctx, "user")
	}
	// This would deny; dry run allows but calls log func
	res, err := l.Allow(ctx, "user")
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, "user", loggedKey)
	require.NotNil(t, loggedResult)
	assert.False(t, loggedResult.Allowed)
	assert.Equal(t, int64(2), loggedResult.Limit)
	assert.Equal(t, int64(0), loggedResult.Remaining)
}

func TestDryRun_ResetDelegates(t *testing.T) {
	ctx := context.Background()
	l, err := NewFixedWindow(2, 60, WithDryRun(true))
	require.NoError(t, err)
	err = l.Reset(ctx, "key")
	require.NoError(t, err)
}

func TestDryRun_OffByDefault(t *testing.T) {
	ctx := context.Background()
	l, err := NewFixedWindow(2, 60)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, _ = l.Allow(ctx, "key")
	}
	res, _ := l.Allow(ctx, "key")
	assert.False(t, res.Allowed, "without dry run, over limit should be denied")
}
