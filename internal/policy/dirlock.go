package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Directory locking.
//
// Two problems, one lock:
//
//  1. In-process: a probe installs a witness file and then asks the runner to
//     build the package. The runner must not start a second build in the same
//     directory meanwhile, and neither may a sibling probe.
//  2. Cross-process: `go test ./...` runs one binary per package, so two of
//     this project's own tests can hit the same fixture directory, and a user
//     can run two `go-flake-hunter hunt` invocations. Without an OS lock the second
//     one lists tests while the first has a witness file on disk and sees a
//     test that does not exist.
//
// Re-entrancy is carried by the context, not by a refcount: a refcount cannot
// tell "the same goroutine came back" from "another goroutine arrived", and
// happily lets N goroutines through at once.

type lockTokenKey struct{}

type lockToken struct {
	key    string
	parent *lockToken
}

// mu guards the entry map; never held across an OS lock wait.
var (
	mu      sync.Mutex
	entries = map[string]*entry{}
)

// entry is one directory's lock state.
type entry struct {
	// sem serializes holders inside this process (a 1-slot channel, so a
	// future timed variant can select on it).
	sem chan struct{}
}

func entryFor(key string) *entry {
	mu.Lock()
	defer mu.Unlock()
	e, ok := entries[key]
	if !ok {
		e = &entry{sem: make(chan struct{}, 1)}
		e.sem <- struct{}{}
		entries[key] = e
	}
	return e
}

// Locked acquires dir for the duration of the returned release func and
// returns a context marking the lock as held.
//
// Callers must pass the returned context down to anything that may lock the
// same directory again: a nested call that sees the token returns immediately
// instead of deadlocking.
func Locked(ctx context.Context, dir string) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := canonical(dir)

	// Already held by this call chain: nothing to do.
	if Held(ctx, key) {
		return ctx, func() {}, nil
	}

	e := entryFor(key)

	select {
	case <-e.sem:
	case <-ctx.Done():
		return ctx, func() {}, ctx.Err()
	}

	f, err := acquireFileLock(ctx, key)
	if err != nil {
		e.sem <- struct{}{}
		return ctx, func() {}, err
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = funlock(f)
			_ = f.Close()
			e.sem <- struct{}{}
		})
	}

	parent, _ := ctx.Value(lockTokenKey{}).(lockToken)
	return context.WithValue(ctx, lockTokenKey{}, lockToken{key: key, parent: &parent}), release, nil
}

func acquireFileLock(ctx context.Context, key string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := lockPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flockExclusive(ctx, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// Held reports whether ctx already carries dir's lock.
func Held(ctx context.Context, dir string) bool {
	tok, ok := ctx.Value(lockTokenKey{}).(lockToken)
	key := canonical(dir)
	for ok {
		if tok.key == key {
			return true
		}
		if tok.parent == nil {
			break
		}
		tok = *tok.parent
	}
	return false
}

func canonical(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func lockPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(os.TempDir(), "go-flake-hunter-locks", hex.EncodeToString(sum[:16])+".lock")
}

// LockedDirs acquires a sequence's directories in canonical order to prevent
// opposite-direction cross-package runs from deadlocking or interleaving.
func LockedDirs(ctx context.Context, dirs ...string) (context.Context, func(), error) {
	keys := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, d := range dirs {
		k := canonical(d)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, k := range keys {
		next, unlock, err := Locked(ctx, k)
		if err != nil {
			release()
			return ctx, func() {}, err
		}
		ctx = next
		releases = append(releases, unlock)
	}
	return ctx, release, nil
}
