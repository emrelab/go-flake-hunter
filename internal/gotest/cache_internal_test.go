package gotest

import (
	"math"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func TestCacheKeyPreservesMinimumSeed(t *testing.T) {
	s := domain.RunSpec{ShuffleSeed: math.MinInt64}
	if !strings.Contains(specKey(s), "-9223372036854775808") {
		t.Fatal("minimum seed lost its value in the cache key")
	}
}

func TestCacheKeySeparatesEnvironmentFromTags(t *testing.T) {
	first := domain.RunSpec{Env: []string{"MODE=test\x01integration"}}
	second := domain.RunSpec{Env: []string{"MODE=test"}, Tags: []string{"integration"}}
	third := domain.RunSpec{Tags: []string{"MODE=test", "integration"}}
	if specKey(first) == specKey(second) || specKey(second) == specKey(third) {
		t.Fatal("distinct valid environments and build tags share a cache entry")
	}
}

func BenchmarkSpecKey(b *testing.B) {
	s := domain.RunSpec{Package: "/work/project/service", RunRegex: "^TestFlakeWitness$", Count: 1, Parallel: 4, ShuffleSeed: 123456789, Source: "0123456789abcdef", Env: []string{"TZ=UTC", "LANG=C", "TEST_MODE=1"}, Tags: []string{"integration", "goflakehunter"}}
	b.ReportAllocs()
	for b.Loop() {
		_ = specKey(s)
	}
}
