#!/usr/bin/env bash
# Run benchmarks and optional benchstat. From repo root: ./bench/run.sh [full|serial|compare]
set -e
cd "$(dirname "$0")/.."

RUN="${1:-serial}"

case "$RUN" in
  full)
    echo "Running full benchmark suite (count=10)..."
    go test -bench=. -benchmem -count=10 . 2>&1 | tee bench/baseline.txt
    echo "Summary:"
    benchstat bench/baseline.txt
    ;;
  serial)
    echo "Running serial algorithms only (count=10)..."
    go test -bench='BenchmarkFixedWindow$|BenchmarkSlidingWindow$|BenchmarkSlidingWindowCounter$|BenchmarkTokenBucket$|BenchmarkGCRA$|BenchmarkCMS$|BenchmarkLeakyBucket_Policing$|BenchmarkPreFilter$' \
      -benchmem -count=10 . 2>&1 | tee bench/serial.txt
    benchstat bench/serial.txt | tee bench/results.txt
    ;;
  compare)
    if [[ ! -f bench/before.txt || ! -f bench/after.txt ]]; then
      echo "Usage: save before.txt and after.txt in bench/, then run: ./bench/run.sh compare"
      echo "  go test -bench=. -benchmem -count=10 . 2>&1 > bench/before.txt"
      echo "  # make changes"
      echo "  go test -bench=. -benchmem -count=10 . 2>&1 > bench/after.txt"
      exit 1
    fi
    benchstat bench/before.txt bench/after.txt
    ;;
  *)
    echo "Usage: $0 {full|serial|compare}"
    echo "  full    - full suite, count=10, save to baseline.txt"
    echo "  serial  - serial algorithms only, count=10, save to serial.txt + results.txt"
    echo "  compare - benchstat before.txt vs after.txt"
    exit 1
    ;;
esac
