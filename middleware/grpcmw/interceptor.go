// Package grpcmw provides gRPC server interceptors for rate limiting.
//
// Separated from the middleware package so that importing the HTTP middleware
// does not pull in google.golang.org/grpc.
//
// Usage:
//
//	limiter, _ := goratelimit.NewGCRA(1000, 50, goratelimit.WithRedis(client))
//	server := grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(grpcmw.UnaryServerInterceptor(limiter, grpcmw.KeyByPeer)),
//	    grpc.ChainStreamInterceptor(grpcmw.StreamServerInterceptor(limiter, grpcmw.StreamKeyByPeer)),
//	)
package grpcmw

import (
	"context"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	goratelimit "github.com/krishna-kudari/ratelimit"
)

// KeyFunc extracts the rate limiting key from a unary RPC context.
type KeyFunc func(ctx context.Context, info *grpc.UnaryServerInfo) string

// StreamKeyFunc extracts the rate limiting key from a streaming RPC context.
type StreamKeyFunc func(ctx context.Context, info *grpc.StreamServerInfo) string

// DeniedHandler produces the gRPC error returned when a request is rate limited.
// Default: codes.ResourceExhausted with retry info.
type DeniedHandler func(ctx context.Context, result *goratelimit.Result) error

// Config holds full configuration for gRPC rate limit interceptors.
type Config struct {
	// Limiter is the rate limiter instance (required).
	Limiter goratelimit.Limiter

	// KeyFunc extracts the rate limit key for unary RPCs (required for unary).
	KeyFunc KeyFunc

	// StreamKeyFunc extracts the rate limit key for streaming RPCs (required for stream).
	StreamKeyFunc StreamKeyFunc

	// DeniedHandler produces the error returned on denial.
	// Default: codes.ResourceExhausted.
	DeniedHandler DeniedHandler

	// ExcludeMethods are full method names (e.g. "/pkg.Service/Method")
	// that bypass rate limiting.
	ExcludeMethods map[string]bool

	// Headers controls whether rate limit metadata is sent in response headers.
	// Default: true.
	Headers *bool
}

// ─── Unary Interceptors ──────────────────────────────────────────────────────

// UnaryServerInterceptor creates a unary server interceptor with default settings.
func UnaryServerInterceptor(limiter goratelimit.Limiter, keyFunc KeyFunc) grpc.UnaryServerInterceptor {
	return UnaryServerInterceptorWithConfig(Config{
		Limiter: limiter,
		KeyFunc: keyFunc,
	})
}

// UnaryServerInterceptorWithConfig creates a unary server interceptor with full
// configuration control.
func UnaryServerInterceptorWithConfig(cfg Config) grpc.UnaryServerInterceptor {
	if cfg.Limiter == nil {
		panic("grpcmw: Limiter is required")
	}
	if cfg.KeyFunc == nil {
		panic("grpcmw: KeyFunc is required")
	}
	if cfg.DeniedHandler == nil {
		cfg.DeniedHandler = defaultDeniedHandler
	}
	sendHeaders := cfg.Headers == nil || *cfg.Headers

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if cfg.ExcludeMethods != nil && cfg.ExcludeMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		key := cfg.KeyFunc(ctx, info)
		result, err := cfg.Limiter.Allow(ctx, key)
		if err != nil {
			return handler(ctx, req)
		}

		if sendHeaders {
			setRateLimitMetadata(ctx, &result)
		}

		if !result.Allowed {
			return nil, cfg.DeniedHandler(ctx, &result)
		}

		return handler(ctx, req)
	}
}

// ─── Stream Interceptors ─────────────────────────────────────────────────────

// StreamServerInterceptor creates a stream server interceptor with default settings.
func StreamServerInterceptor(limiter goratelimit.Limiter, keyFunc StreamKeyFunc) grpc.StreamServerInterceptor {
	return StreamServerInterceptorWithConfig(Config{
		Limiter:       limiter,
		StreamKeyFunc: keyFunc,
	})
}

// StreamServerInterceptorWithConfig creates a stream server interceptor with full
// configuration control.
func StreamServerInterceptorWithConfig(cfg Config) grpc.StreamServerInterceptor {
	if cfg.Limiter == nil {
		panic("grpcmw: Limiter is required")
	}
	if cfg.StreamKeyFunc == nil {
		panic("grpcmw: StreamKeyFunc is required")
	}
	if cfg.DeniedHandler == nil {
		cfg.DeniedHandler = defaultDeniedHandler
	}
	sendHeaders := cfg.Headers == nil || *cfg.Headers

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()

		if cfg.ExcludeMethods != nil && cfg.ExcludeMethods[info.FullMethod] {
			return handler(srv, ss)
		}

		key := cfg.StreamKeyFunc(ctx, info)
		result, err := cfg.Limiter.Allow(ctx, key)
		if err != nil {
			return handler(srv, ss)
		}

		if sendHeaders {
			setRateLimitMetadata(ctx, &result)
		}

		if !result.Allowed {
			return cfg.DeniedHandler(ctx, &result)
		}

		return handler(srv, ss)
	}
}

// ─── Built-in Key Extractors ─────────────────────────────────────────────────

// KeyByPeer extracts the remote peer address as the rate limit key.
func KeyByPeer(ctx context.Context, _ *grpc.UnaryServerInfo) string {
	return peerAddr(ctx)
}

// StreamKeyByPeer extracts the remote peer address as the rate limit key for streams.
func StreamKeyByPeer(ctx context.Context, _ *grpc.StreamServerInfo) string {
	return peerAddr(ctx)
}

// KeyByMetadata returns a KeyFunc that uses a value from incoming gRPC metadata.
func KeyByMetadata(header string) KeyFunc {
	return func(ctx context.Context, _ *grpc.UnaryServerInfo) string {
		return metadataValue(ctx, header)
	}
}

// StreamKeyByMetadata returns a StreamKeyFunc that uses a value from incoming gRPC metadata.
func StreamKeyByMetadata(header string) StreamKeyFunc {
	return func(ctx context.Context, _ *grpc.StreamServerInfo) string {
		return metadataValue(ctx, header)
	}
}

// KeyByMethod returns a KeyFunc that uses "method:peer" as the key,
// enabling per-method rate limits.
func KeyByMethod(ctx context.Context, info *grpc.UnaryServerInfo) string {
	return info.FullMethod + ":" + peerAddr(ctx)
}

// StreamKeyByMethod returns a StreamKeyFunc that uses "method:peer" as the key.
func StreamKeyByMethod(ctx context.Context, info *grpc.StreamServerInfo) string {
	return info.FullMethod + ":" + peerAddr(ctx)
}

// ─── Internals ───────────────────────────────────────────────────────────────

func peerAddr(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if ok && p.Addr != nil {
		return p.Addr.String()
	}
	return "unknown"
}

func metadataValue(ctx context.Context, header string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		if vals := md.Get(header); len(vals) > 0 {
			return vals[0]
		}
	}
	return "unknown"
}

func setRateLimitMetadata(ctx context.Context, result *goratelimit.Result) {
	md := metadata.Pairs(
		"x-ratelimit-limit", strconv.FormatInt(result.Limit, 10),
		"x-ratelimit-remaining", strconv.FormatInt(result.Remaining, 10),
	)
	if !result.ResetAt.IsZero() {
		md.Append("x-ratelimit-reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
	}
	if !result.Allowed && result.RetryAfter > 0 {
		md.Append("retry-after", strconv.FormatInt(int64(result.RetryAfter.Seconds()+0.5), 10))
	}
	_ = grpc.SetHeader(ctx, md)
}

func defaultDeniedHandler(_ context.Context, result *goratelimit.Result) error {
	return status.Errorf(codes.ResourceExhausted,
		"rate limit exceeded, retry after %v", result.RetryAfter)
}
