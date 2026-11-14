package gotest

import (
	"context"
	"sync"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// Cache memoizes Runner.Run by spec.
//
// Why this matters more than it looks: the shuffle sweep asks the SAME
// question ("run this package under seed S") once per test in the package.
// Without the cache a 30-test package does 30 × 40 builds; with it, 40. It
// also makes probes cheaper to compose, because a later probe can re-ask a
// question an earlier one already paid for.
//
// Failure results are intentionally not cached: a probe that re-runs a build
// expecting it to fail is checking determinism, and a cached failure would
// turn "it reproduced 3 times" into "it reproduced once, remembered".
type Cache struct {
	inner Runner

	mu     sync.Mutex
	hits   map[string]*domain.RunResult
	order  []string // FIFO eviction order
	limit  int
	misses int
	hitN   int
}

// NewCache wraps a Runner. limit <= 0 -> DefaultCacheLimit.
func NewCache(inner Runner, limit int) *Cache {
	if limit <= 0 {
		limit = DefaultCacheLimit
	}
	return &Cache{inner: inner, hits: make(map[string]*domain.RunResult), limit: limit}
}

// DefaultCacheLimit keeps memory bounded: results carry normalized output.
const DefaultCacheLimit = 256

// Discover passes through; listing is cheap and always current.
func (c *Cache) Discover(ctx context.Context, pkgDir string) ([]domain.TestID, error) {
	return c.inner.Discover(ctx, pkgDir)
}

// Run returns a cached PASS/SKIP result when the exact spec was seen before.
func (c *Cache) Run(ctx context.Context, spec domain.RunSpec) (*domain.RunResult, error) {
	if domain.RequiresFreshRuns(ctx) {
		return c.inner.Run(ctx, spec)
	}
	key := specKey(spec)

	c.mu.Lock()
	if res, ok := c.hits[key]; ok && cacheable(res) {
		c.hitN++
		c.mu.Unlock()
		return res, nil
	}
	c.misses++
	c.mu.Unlock()

	res, err := c.inner.Run(ctx, spec)
	if err != nil || !cacheable(res) {
		return res, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.hits[key]; !exists {
		if len(c.order) >= c.limit {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.hits, oldest)
		}
		c.hits[key] = res
		c.order = append(c.order, key)
	}
	return res, nil
}

// Stats reports cache effectiveness; used by --verbose and by the tests that
// guard against silently losing the amortization.
func (c *Cache) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hitN, c.misses
}

// cacheable reports whether a result may be reused.
//
// A failing result, a build failure or a timeout is evidence about the system,
// not a fact about the code: re-asking must reach the real binary.
func cacheable(res *domain.RunResult) bool {
	if res == nil || res.BuildFailed || res.TimedOut || res.ExitCode != 0 {
		return false
	}
	for _, o := range res.Outcomes {
		if o.Status == domain.StatusFail {
			return false
		}
	}
	return true
}

// specKey is the identity of a run. Env participates because a probe that
// varies the environment must not collide with the baseline.
func specKey(s domain.RunSpec) string {
	key := s.Package + "\x00" + s.RunRegex + "\x00"
	key += itoa(s.Count) + "\x00" + itoa(s.Parallel) + "\x00" + itoa(s.CPU) + "\x00"
	key += itoa64(s.ShuffleSeed) + "\x00" + boolStr(s.Race) + boolStr(s.Short)
	// Witness content is part of the key: same package + same -run means a
	// different step set. Without this, the PASS of one ddmin subset was served
	// to another, and the search eliminated the real polluter.
	key += "\x00" + s.Source
	for _, e := range s.Env {
		key += "\x00" + e
	}
	for _, t := range s.Tags {
		key += "\x01" + t
	}
	return key
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func itoa64(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
