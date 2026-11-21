# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.4.0] — 2026-11-20

### Added
- **CI artifact reliability**: Artifact ID-based download in quality job eliminates intermittent download failures in PR CI (`.github/workflows/ci.yml`)
- **PERFORMANCE.md documentation**: Corrected SpecKey benchmark claims and dense normalization trade-off disclosure
- **CI artifact reliability fix**: Artifact ID-based download using upload step's `artifact-id` output

### Changed
- **Nightly CI exit code**: Budget exhaustion (exit 4) now correctly preserved instead of converted to success
- **Benchmark fixture naming**: `gen_%03d_test.go` → `gen%03d_test.go` to avoid Go architecture suffix conflicts
- **CI artifact download**: Switched from name-based to ID-based download for deterministic artifact retrieval
- **Performance documentation**: Corrected SpecKey benchmark claims (baseline already ~100 ns/op on Go 1.27.4) and dense normalization trade-off (increased allocation count, similar bytes)

### Fixed
- **Nightly CI exit code 4 → 0**: Budget-exhausted self-hunts now correctly report exit 4 instead of silently succeeding
- **Benchmark fixture exclusion**: Files like `gen_386_test.go` no longer excluded on non-386 architectures
- **CI artifact download failures**: Intermittent "Artifact not found" errors eliminated via ID-based download

### Performance
- **AST scanning**: ~40-45% latency reduction and ~45% allocation reduction at 400 files (25.5 ms → 14.8 ms, 17.4 MB → 9.5 MB) through `parser.SkipObjectResolution` and AST reuse
- **Log normalization**: Clean inputs allocate 0 bytes (was 724 B/op); noisy logs allocate 50-70% fewer bytes; dense synthetic workloads trade allocation count (5041 vs 80) for similar bytes
- **Cache key generation**: Optimized with single `strings.Builder` and standard integer formatting

## [0.3.0] — 2026-11-13