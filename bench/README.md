# Benchmarks

Benchmark results and scripts for `go-ratelimit`. Uses [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) for statistically summarized results.

## Setup

```bash
go install golang.org/x/perf/cmd/benchstat@latest
```

## Run benchmarks

From the **repository root**:

```bash
# Full suite, 3 runs (faster)
go test -bench=. -benchmem -count=3 .

# Full suite, 10 runs (for benchstat confidence intervals)
go test -bench=. -benchmem -count=10 . 2>&1 | tee bench/baseline.txt

# Serial algorithms only (faster, for quick comparison)
go test -bench='BenchmarkFixedWindow$|BenchmarkSlidingWindow$|BenchmarkSlidingWindowCounter$|BenchmarkTokenBucket$|BenchmarkGCRA$|BenchmarkCMS$|BenchmarkLeakyBucket_Policing$|BenchmarkPreFilter$' -benchmem -count=10 . 2>&1 | tee bench/serial.txt
```

## Summarize with benchstat

```bash
# Single run summary (from bench/serial.txt or bench/baseline.txt)
benchstat bench/serial.txt

# Compare before vs after a change
benchstat bench/before.txt bench/after.txt
```

Example comparison output:

```
name                old time/op    new time/op    delta
FixedWindow-10        47.5ns ± 2%    46.1ns ± 1%  -2.95%  (p=0.000 n=10+10)
GCRA-10               53.7ns ± 1%    52.8ns ± 1%  -1.67%  (p=0.000 n=10+10)
```

`p=0.000` indicates the delta is statistically significant (not noise).

## Profiling

```bash
# CPU profile
go test -bench=BenchmarkGCRA$ -cpuprofile=bench/cpu.prof -count=1 .
go tool pprof bench/cpu.prof

# Memory profile
go test -bench=BenchmarkGCRA$ -memprofile=bench/mem.prof -count=1 .
go tool pprof bench/mem.prof
```

## Files

| File            | Description |
|-----------------|-------------|
| `baseline.txt`  | Full benchmark output (count=10), for comparison baseline. |
| `serial.txt`    | Serial-algorithm-only output (count=5 or 10). |
| `serial10.txt`  | Short serial subset (FixedWindow, GCRA, CMS), count=10. |
| `results.txt`   | benchstat summary (e.g. of `serial10.txt`). |
| `before.txt`    | Save before a change for comparison. |
| `after.txt`     | Save after a change; run `benchstat before.txt after.txt`. |
