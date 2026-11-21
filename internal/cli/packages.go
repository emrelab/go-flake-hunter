package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// packageScoped reports whether err is a verdict about the package that
// produced it — its tests failed without naming one, or its run did not finish
// — rather than about the scan. Those two are exit 7 either way, but one sick
// package must not hide the rest: stopping the scan there made `hunt ./...`
// print nothing at all on repositories where a single package is slower than
// the per-run timeout, which is exactly the repository this tool is for.
func packageScoped(err error) bool {
	return errors.Is(err, domain.ErrIncomplete) || errors.Is(err, domain.ErrPackageFailed)
}

// skipReason is the sentinel's own wording, without the wrapped package path
// the caller already prints next to it.
func skipReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrIncomplete):
		return "test run incomplete"
	case errors.Is(err, domain.ErrPackageFailed):
		return "package failed"
	}
	return err.Error()
}

// packages bounds concurrency at package granularity. Each worker owns its
// reporting state; only the runner and total budget are shared. Reports merge
// in argument order so concurrent writes cannot corrupt JSON or text.
func (s *scanner) packages(ctx context.Context, dirs []string, jobs int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	seen := map[string]bool{}
	unique := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		abs, _ := filepath.Abs(dir)
		key := abs
		if real, e := filepath.EvalSymlinks(key); e == nil {
			key = real
		}
		if !seen[key] {
			seen[key] = true
			unique = append(unique, abs)
		}
	}
	type result struct {
		child  *scanner
		stdout bytes.Buffer
		err    error
	}
	results := make([]result, len(unique))
	diagnostics := &lockedWriter{w: s.out.Stderr}
	var mu sync.Mutex
	next, halt := 0, false
	var wg sync.WaitGroup
	for worker := 0; worker < min(jobs, len(unique)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if halt || next >= len(unique) {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				r := &results[i]
				child := &scanner{o: s.o, out: IO{Stdout: &r.stdout, Stderr: diagnostics, Debug: s.out.Debug}, registry: s.registry, runner: s.runner, budget: s.budget, only: s.only, baseline: s.baseline, cwd: s.cwd, progress: s.progress}
				r.child = child
				r.err = child.pkg(ctx, unique[i])
				s.progress.finish(child.relDir(unique[i]), r.err)
				if r.err != nil && !packageScoped(r.err) {
					mu.Lock()
					halt = true
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	var first error
	for i := range results {
		r := &results[i]
		if r.child == nil {
			continue
		}
		if _, err := s.out.Stdout.Write(r.stdout.Bytes()); err != nil && first == nil {
			first = err
		}
		s.found += r.child.found
		s.suspected += r.child.suspected
		s.brokenCount += r.child.brokenCount
		s.fresh += r.child.fresh
		s.ids = append(s.ids, r.child.ids...)
		s.collected = append(s.collected, r.child.collected...)
		s.results = append(s.results, r.child.results...)
		s.broken = s.broken || r.child.broken
		s.scanned += r.child.scanned
		for _, name := range r.child.tallyOrder {
			dst := s.tallyFor(name)
			c := r.child.tallies[name]
			dst.tried += c.tried
			dst.explained += c.explained
			dst.skipped += c.skipped
		}
		if r.err != nil {
			// Kept per package, not just as the run's first error: the summary
			// has to name what the verdict does not cover.
			if packageScoped(r.err) {
				// A path that escapes the working directory is no more readable
				// than the absolute one the user typed.
				label := s.relDir(unique[i])
				if strings.HasPrefix(label, "..") {
					label = unique[i]
				}
				s.skipped = append(s.skipped, fmt.Sprintf("%s: %s", label, skipReason(r.err)))
			}
			if first == nil {
				first = r.err
			}
		}
	}
	return first
}

// Diagnostics are emitted immediately, including dependency hints before a
// long-running probe. Each Write is atomic across workers.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}
