// Package metrics provides Prometheus instrumentation for rate limiters.
//
// Wrap any goratelimit.Limiter to automatically record request counts,
// latency, and backend errors:
//
//	collector := metrics.NewCollector()
//	limiter, _ := goratelimit.NewTokenBucket(100, 10)
//	limiter = metrics.Wrap(limiter, metrics.TokenBucket, collector)
//
// All metrics are partitioned by algorithm name. Request counts carry an
// additional "decision" label (allowed / denied).
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// Algorithm name constants for the algorithm label.
const (
	FixedWindow          = "fixed_window"
	SlidingWindow        = "sliding_window"
	SlidingWindowCounter = "sliding_window_counter"
	TokenBucket          = "token_bucket"
	LeakyBucket          = "leaky_bucket"
	GCRA                 = "gcra"
)

// Collector holds Prometheus metric vectors for rate limiter instrumentation.
type Collector struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	errors   *prometheus.CounterVec
}

type collectorConfig struct {
	namespace string
	subsystem string
	registry  prometheus.Registerer
	buckets   []float64
}

// CollectorOption configures a Collector.
type CollectorOption func(*collectorConfig)

// WithNamespace sets the Prometheus metric namespace (prefix).
func WithNamespace(ns string) CollectorOption {
	return func(c *collectorConfig) { c.namespace = ns }
}

// WithSubsystem sets the Prometheus metric subsystem.
func WithSubsystem(sub string) CollectorOption {
	return func(c *collectorConfig) { c.subsystem = sub }
}

// WithRegistry registers metrics with the given Registerer instead of
// prometheus.DefaultRegisterer.
func WithRegistry(r prometheus.Registerer) CollectorOption {
	return func(c *collectorConfig) { c.registry = r }
}

// WithBuckets sets custom histogram buckets for request duration.
func WithBuckets(b []float64) CollectorOption {
	return func(c *collectorConfig) { c.buckets = b }
}

var defaultBuckets = []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}

// NewCollector creates a Collector and registers its metrics.
//
// Metrics registered:
//   - {namespace}_requests_total        counter   (algorithm, decision)
//   - {namespace}_request_duration_seconds  histogram (algorithm)
//   - {namespace}_errors_total          counter   (algorithm)
//
// Default namespace is "ratelimit".
func NewCollector(opts ...CollectorOption) *Collector {
	cfg := &collectorConfig{
		namespace: "ratelimit",
		registry:  prometheus.DefaultRegisterer,
		buckets:   defaultBuckets,
	}
	for _, o := range opts {
		o(cfg)
	}

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "requests_total",
		Help:      "Total rate limit checks partitioned by algorithm and decision.",
	}, []string{"algorithm", "decision"})

	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "request_duration_seconds",
		Help:      "Latency of rate limit Allow calls in seconds.",
		Buckets:   cfg.buckets,
	}, []string{"algorithm"})

	errors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.namespace,
		Subsystem: cfg.subsystem,
		Name:      "errors_total",
		Help:      "Total rate limiter backend errors.",
	}, []string{"algorithm"})

	cfg.registry.MustRegister(requests, duration, errors)

	return &Collector{
		requests: requests,
		duration: duration,
		errors:   errors,
	}
}

// Wrap returns a Limiter that transparently records Prometheus metrics
// for every Allow and AllowN call delegated to inner.
func Wrap(inner goratelimit.Limiter, algorithm string, c *Collector) goratelimit.Limiter {
	return &instrumentedLimiter{
		inner:     inner,
		algorithm: algorithm,
		collector: c,
	}
}

type instrumentedLimiter struct {
	inner     goratelimit.Limiter
	algorithm string
	collector *Collector
}

func (l *instrumentedLimiter) Allow(ctx context.Context, key string) (goratelimit.Result, error) {
	return l.AllowN(ctx, key, 1)
}

func (l *instrumentedLimiter) AllowN(ctx context.Context, key string, n int) (goratelimit.Result, error) {
	start := time.Now()
	result, err := l.inner.AllowN(ctx, key, n)
	l.collector.duration.WithLabelValues(l.algorithm).Observe(time.Since(start).Seconds())

	if err != nil {
		l.collector.errors.WithLabelValues(l.algorithm).Inc()
		return goratelimit.Result{}, err
	}

	l.recordDecision(&result)
	return result, nil
}

func (l *instrumentedLimiter) Reset(ctx context.Context, key string) error {
	return l.inner.Reset(ctx, key)
}

func (l *instrumentedLimiter) recordDecision(result *goratelimit.Result) {
	decision := "denied"
	if result.Allowed {
		decision = "allowed"
	}
	l.collector.requests.WithLabelValues(l.algorithm, decision).Inc()
}
