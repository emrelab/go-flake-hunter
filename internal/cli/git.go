package cli

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// gitOutput runs one git command in dir and returns its trimmed stdout.
//
// A separate helper: --changed asks git twice (does the ref exist, what is
// the diff), and swallowing stderr on every call would be the easiest way to
// make a missing ref look like "no changes" — errors are returned.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", &gitError{err: err, msg: msg}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitError keeps git's own sentence: "fatal: bad revision" is the diagnosis the
// user needs, not the exit status.
type gitError struct {
	err error
	msg string
}

func (e *gitError) Error() string { return e.msg }
func (e *gitError) Unwrap() error { return e.err }

// gitTimeout bounds each provenance command. A capsule must never be the reason
// a scan hangs: an index.lock left by a crashed GUI, a network-mounted checkout
// or a pre-commit hook that shells out can make `git status` block for minutes.
const gitTimeout = 10 * time.Second

// repoState is the code state a capsule records: module path, short commit and
// whether the worktree was dirty.
//
// One struct, not three queries, because every capsule in a hunt asks the same
// question about the same directory: baseCapsule used to run `git rev-parse` and
// `git status` a SECOND time for every capsule it wrote, on top of the pair the
// witness path already paid.
type repoState struct {
	Module string
	Commit string
	Dirty  bool
}

// readRepoState collects the provenance a capsule carries.
//
// Best-effort on purpose: a capsule outside a git repo is still useful, so a
// missing git must not fail the hunt. It only weakens the id's stability.
func readRepoState(ctx context.Context, dir string) repoState {
	st := repoState{Module: modulePath(dir), Commit: "unknown"}

	// exec.CommandContext (not .Output()) so the deadline actually kills a
	// wedged git instead of waiting for the capsule to be written.
	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	if out, err := gitOutput(gitCtx, dir, "rev-parse", "--short", "HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			st.Commit = s
		}
	}
	if out, err := gitOutput(gitCtx, dir, "status", "--porcelain", "--untracked-files=all", "--",
		":(top)**", ":(top,glob,exclude)**/"+domain.CapsuleDir+"/**",
		":(top,glob,exclude)**/"+domain.WitnessFilePrefix+"*"+domain.WitnessFileSuffix); err == nil {
		st.Dirty = strings.TrimSpace(out) != ""
	}
	return st
}

// repoStateFor memoizes readRepoState per directory for the lifetime of one scan.
//
// The mutex is not decoration: packages are scanned sequentially today, but
// `--jobs` (#23) is a planned change, and a shared map written from one
// goroutine per package would be the first race that scheduler work hits.
func (s *scanner) repoStateFor(ctx context.Context, dir string) repoState {
	key := canonical(dir)

	s.gitMu.Lock()
	st, ok := s.gitState[key]
	s.gitMu.Unlock()
	if ok {
		return st
	}

	st = readRepoState(ctx, dir)

	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if s.gitState == nil {
		s.gitState = map[string]repoState{}
	}
	s.gitState[key] = st
	return st
}
