# Performance measurements

Measured on 2026-11-20 with Go 1.27.4, darwin/arm64, Apple M4.
Numbers are medians of five 500 ms benchmark samples before and after the
changes. Both runs use architecture-neutral generated filenames (no GOARCH
suffixes such as `_386`, `_amd64`, `_arm64`).

| Workload | Time before → after | Allocated bytes before → after |
|---|---|---|
| Scan 400 Go test files | 25.5 → 14.8 ms | 17.4 → 9.5 MB |
| Scan 100 Go test files | 6.0 → 3.6 ms | 4.3 → 2.3 MB |
| Scan 25 Go test files | 1.47 → 0.86 ms | 1.09 → 0.59 MB |
| Parse JSON for 400 tests | 0.932 → 0.832 ms | 626 → 393 KB |
| Parse JSON for 100 tests | 0.234 → 0.209 ms | 204 → 146 KB |
| Parse JSON for 25 tests | 0.060 → 0.054 ms | 99.7 → 85.3 KB |
| Parse noisy log: 120 tests × 20 output lines | 5.73 → 5.45 ms | 3.73 → 1.12 MB |
| Normalize unchanged text | 1.45 → 1.20 µs | 724 → 0 B |
| Normalize noisy text | 5.73 → 3.82 µs | 1.70 KB → 0.78 KB |
| Normalize dense (1000× patterns) | 5.73 → 3.48 ms | 463 KB → 463 KB (more allocs) |

The scanner reuses its parsed files for dependency hints and skips identifier
resolution (`parser.SkipObjectResolution`), which its syntax analysis does not use.
Application-file trees are inspected immediately rather than retained together.

Normalization preserves the existing pattern order and literal replacements.
It builds replacement text only for patterns that match. A dense workload
(1,000 repetitions of durations, addresses and pointers) stayed around
3.5 ms and allocated about the same bytes, but made more small allocations
(about 5,041 versus 80). It is not a speed-up for every input.

These are component measurements, not an end-to-end `hunt` speed-up claim.
Integration tests already call the CLI in-process; they do not repeatedly
build a CLI executable. Target test runs and independent confirmation runs
remain real executions.

To compare revisions, run the same command on each without other test jobs:

```sh
go test -run '^$' -bench . -benchmem -benchtime=500ms -count=5 \
  ./internal/facts ./internal/gotest ./internal/search
```

`BenchmarkNormalize` also includes the dense workload for future comparisons.

## Result-cache key (2026-11-20)

On the same Go 1.27.4 / Apple M4 system, five 500 ms samples of
`BenchmarkSpecKey` measured a median of approximately **100 ns/op, 160 B/op,
and 1 allocation** in both baseline and optimized revisions. The workload
includes a witness digest, three environment overrides and two build tags.
A single `strings.Builder` replaces repeated concatenation; standard integer
formatting (`strconv.AppendInt`) preserves the minimum signed shuffle seed.
Environment and tag counts prevent boundary collisions between those lists.
Timeout now participates in identity, and cancelled contexts cannot return a
cached success. This is a cache-key measurement, not an end-to-end scan claim.

```sh
go test -run '^$' -bench '^BenchmarkSpecKey$' -benchmem -count=5 ./internal/gotest
```

**Note:** The previously reported 205.4 → 101.4 ns/op improvement was based on
measurements from a different baseline (Go 1.26.2 / different machine). On Go
1.27.4 the baseline already achieves ~100 ns/op, so no further speedup is
observed for this microbenchmark.