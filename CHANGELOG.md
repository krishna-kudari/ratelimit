# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] — 2026-03-02

### Added

- **Count-Min Sketch algorithm** (`NewCMS`) — fixed-memory probabilistic rate limiter using two rotating sketches for a sliding window effect. In-memory only; ideal for high-cardinality / DDoS scenarios.
- **PreFilter combinator** (`NewPreFilter`) — chains a fast local limiter (CMS) with a precise distributed limiter (e.g. GCRA/Redis). Blocks obvious abusers locally in nanoseconds; only normal-looking traffic reaches the backend.
- `CMSMemoryBytes(epsilon, delta)` helper for capacity planning.
- Builder support: `CMS(limit, window, epsilon, delta)`.
- Benchmarks for CMS and PreFilter (serial, parallel, distinct keys).
- Examples and testable `Example*` functions for CMS and PreFilter.

## [1.0.1] — 2026-03-02

### Changed

- **SECURITY:** Update contact email for vulnerability reporting.
- **golangci-lint:** Revise configuration with YAML schema reference and stricter rules.
- **Tests:** Improve test code quality to align with updated linter rules.

### Added

- `.gitignore`, `.golangci.yml`, and initial project documentation (`CHANGELOG.md`, `CONTRIBUTING.md`, `LICENSE`, `SECURITY.md`, `doc.go`).
- GitHub templates: issue templates (bug report, feature request), PR template, Dependabot config.
- `example_test.go` — testable examples for all six algorithms, AllowN, Reset, Builder, and dynamic limits.

## [1.0.0] — 2025-03-02

### Added

- **Six algorithms:** Fixed Window, Sliding Window Log, Sliding Window Counter, Token Bucket, Leaky Bucket, GCRA.
- **In-memory store** for single-process use and testing.
- **Redis store** via `redis.UniversalClient` — standalone, Cluster, Ring, Sentinel.
- **Redis Cluster hash-tag routing** (`WithHashTag`) for multi-key algorithms.
- **Middleware** for net/http, Gin, Echo, Fiber, and gRPC (unary + streaming).
- **Key extractors:** by IP, header, path+IP, method, metadata, and custom functions.
- **Configurable response:** custom status codes, messages, denied/error handlers.
- **Path exclusion** in all middleware.
- **Dynamic per-key limits** via `WithLimitFunc`.
- **Fail-open/fail-closed** behavior on backend errors.
- **Local cache (L1)** to reduce backend round-trips under load.
- **Prometheus metrics** — request counts, latency histograms, error counters by algorithm.
- **Builder API** for fluent limiter construction.
- **Leaky Bucket modes:** Policing (reject) and Shaping (queue with delay).
- **AllowN** for batch token consumption.
- **Interactive demo** — browser-based algorithm visualizer (`examples/demo`).
- **Comprehensive examples** — basic, HTTP, Gin, Echo, Fiber, gRPC, Redis, advanced patterns.

[1.1.0]: https://github.com/krishna-kudari/ratelimit/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/krishna-kudari/ratelimit/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/krishna-kudari/ratelimit/releases/tag/v1.0.0
