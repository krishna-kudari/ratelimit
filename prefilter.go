package goratelimit

import "context"

// preFilter chains a fast local limiter (typically CMS) with a precise
// distributed limiter (e.g. GCRA via Redis). The local limiter acts as
// a first line of defense: if it rejects, the request is denied without
// touching the remote backend. If it allows, the precise limiter makes
// the final decision.
//
// This is the "sketch + precise" pattern used in production DDoS mitigation:
//
//	Request → CMS (local, nanoseconds) → clearly over? → BLOCK
//	                                    → looks ok?    → GCRA/Redis (global, precise)
type preFilter struct {
	local   Limiter
	precise Limiter
}

// NewPreFilter creates a rate limiter that checks the local limiter first
// and only escalates to the precise limiter when the local check passes.
//
// Under normal traffic both limiters are consulted and the precise limiter's
// result is authoritative. Under attack the local limiter absorbs the load,
// shielding the remote backend from being overwhelmed.
//
//	cms, _  := goratelimit.NewCMS(100, 60, 0.01, 0.001)
//	gcra, _ := goratelimit.NewGCRA(10, 20, goratelimit.WithRedis(client))
//	limiter := goratelimit.NewPreFilter(cms, gcra)
func NewPreFilter(local, precise Limiter) Limiter {
	return &preFilter{local: local, precise: precise}
}

func (p *preFilter) Allow(ctx context.Context, key string) (Result, error) {
	return p.AllowN(ctx, key, 1)
}

func (p *preFilter) AllowN(ctx context.Context, key string, n int) (Result, error) {
	localResult, err := p.local.AllowN(ctx, key, n)
	if err != nil {
		// Local limiter errored — fall through to precise.
		return p.precise.AllowN(ctx, key, n)
	}
	if !localResult.Allowed {
		return localResult, nil
	}
	return p.precise.AllowN(ctx, key, n)
}

func (p *preFilter) Reset(ctx context.Context, key string) error {
	_ = p.local.Reset(ctx, key)
	return p.precise.Reset(ctx, key)
}
