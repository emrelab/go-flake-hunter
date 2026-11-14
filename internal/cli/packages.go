package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"sync"
)

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
				child := &scanner{o: s.o, out: IO{Stdout: &r.stdout, Stderr: diagnostics, Debug: s.out.Debug}, registry: s.registry, runner: s.runner, budget: s.budget, only: s.only, baseline: s.baseline, cwd: s.cwd}
				r.child = child
				r.err = child.pkg(ctx, unique[i])
				if r.err != nil {
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
		s.fresh += r.child.fresh
		s.ids = append(s.ids, r.child.ids...)
		s.collected = append(s.collected, r.child.collected...)
		s.results = append(s.results, r.child.results...)
		s.broken = s.broken || r.child.broken
		if r.err != nil && first == nil {
			first = r.err
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
