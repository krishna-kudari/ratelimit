package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goratelimit "github.com/krishna-kudari/ratelimit"
	"github.com/krishna-kudari/ratelimit/metrics"
)

func TestWrap_AllowedAndDenied(t *testing.T) {
	reg := prometheus.NewRegistry()
	collector := metrics.NewCollector(metrics.WithRegistry(reg))

	limiter, err := goratelimit.NewFixedWindow(2, 60)
	require.NoError(t, err)
	wrapped := metrics.Wrap(limiter, metrics.FixedWindow, collector)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		result, err := wrapped.Allow(ctx, "k1")
		require.NoError(t, err)
		require.True(t, result.Allowed, "request %d: expected allowed", i+1)
	}

	result, err := wrapped.Allow(ctx, "k1")
	require.NoError(t, err)
	require.False(t, result.Allowed, "request 3: expected denied")

	assertCounter(t, reg, "ratelimit_requests_total", map[string]string{
		"algorithm": "fixed_window", "decision": "allowed",
	}, 2)
	assertCounter(t, reg, "ratelimit_requests_total", map[string]string{
		"algorithm": "fixed_window", "decision": "denied",
	}, 1)
	assertHistogramCount(t, reg, "ratelimit_request_duration_seconds", map[string]string{
		"algorithm": "fixed_window",
	}, 3)
	assertCounter(t, reg, "ratelimit_errors_total", map[string]string{
		"algorithm": "fixed_window",
	}, 0)
}

func TestWrap_AllowN(t *testing.T) {
	reg := prometheus.NewRegistry()
	collector := metrics.NewCollector(metrics.WithRegistry(reg))

	limiter, err := goratelimit.NewTokenBucket(10, 10)
	require.NoError(t, err)
	wrapped := metrics.Wrap(limiter, metrics.TokenBucket, collector)

	result, err := wrapped.AllowN(context.Background(), "k1", 5)
	require.NoError(t, err)
	require.True(t, result.Allowed, "expected allowed for AllowN(5)")

	assertCounter(t, reg, "ratelimit_requests_total", map[string]string{
		"algorithm": "token_bucket", "decision": "allowed",
	}, 1)
}

func TestWrap_ErrorCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	collector := metrics.NewCollector(metrics.WithRegistry(reg))

	wrapped := metrics.Wrap(&failLimiter{}, "custom", collector)

	_, err := wrapped.Allow(context.Background(), "k1")
	require.Error(t, err, "expected error")

	assertCounter(t, reg, "ratelimit_errors_total", map[string]string{
		"algorithm": "custom",
	}, 1)
}

func TestWrap_Reset(t *testing.T) {
	reg := prometheus.NewRegistry()
	collector := metrics.NewCollector(metrics.WithRegistry(reg))

	limiter, err := goratelimit.NewFixedWindow(1, 60)
	require.NoError(t, err)
	wrapped := metrics.Wrap(limiter, metrics.FixedWindow, collector)
	ctx := context.Background()

	_, err = wrapped.Allow(ctx, "k1")
	require.NoError(t, err)
	err = wrapped.Reset(ctx, "k1")
	require.NoError(t, err)

	result, err := wrapped.Allow(ctx, "k1")
	require.NoError(t, err)
	require.True(t, result.Allowed, "expected allowed after reset")
}

func TestCollectorOptions(t *testing.T) {
	reg := prometheus.NewRegistry()
	collector := metrics.NewCollector(
		metrics.WithRegistry(reg),
		metrics.WithNamespace("myapp"),
		metrics.WithSubsystem("api"),
		metrics.WithBuckets([]float64{.001, .01, .1}),
	)

	limiter, err := goratelimit.NewTokenBucket(10, 10)
	require.NoError(t, err)
	wrapped := metrics.Wrap(limiter, metrics.TokenBucket, collector)

	_, err = wrapped.Allow(context.Background(), "k1")
	require.NoError(t, err)

	assertCounter(t, reg, "myapp_api_requests_total", map[string]string{
		"algorithm": "token_bucket", "decision": "allowed",
	}, 1)
	assertHistogramCount(t, reg, "myapp_api_request_duration_seconds", map[string]string{
		"algorithm": "token_bucket",
	}, 1)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

type failLimiter struct{}

func (f *failLimiter) Allow(ctx context.Context, key string) (goratelimit.Result, error) {
	return f.AllowN(ctx, key, 1)
}

func (f *failLimiter) AllowN(ctx context.Context, key string, n int) (goratelimit.Result, error) {
	return goratelimit.Result{}, errors.New("backend down")
}

func (f *failLimiter) Reset(ctx context.Context, key string) error {
	return errors.New("backend down")
}

func assertCounter(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string, want float64) {
	t.Helper()
	val := gatherMetricValue(t, reg, name, labels, func(m *dto.Metric) float64 {
		return m.GetCounter().GetValue()
	})
	assert.Equal(t, want, val, "%s%v", name, labels)
}

func assertHistogramCount(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string, want uint64) {
	t.Helper()
	val := gatherMetricValue(t, reg, name, labels, func(m *dto.Metric) float64 {
		return float64(m.GetHistogram().GetSampleCount())
	})
	assert.Equal(t, want, uint64(val), "%s%v sample_count", name, labels)
}

func gatherMetricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string, extract func(*dto.Metric) float64) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if matchLabels(m, labels) {
				return extract(m)
			}
		}
	}
	if len(labels) > 0 {
		return 0
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

func matchLabels(m *dto.Metric, want map[string]string) bool {
	pairs := m.GetLabel()
	if len(pairs) < len(want) {
		return false
	}
	for _, lp := range pairs {
		if v, ok := want[lp.GetName()]; ok && v != lp.GetValue() {
			return false
		}
	}
	return true
}
