package gotest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// ExpandPatterns turns `go test`-style package arguments into concrete
// directories.
//
// Why this exists: every real invocation is `go-flake-hunter hunt ./...`, and a tool
// that only accepts one literal directory answers that with a usage error. The
// expansion is delegated to `go list`, which knows about module boundaries,
// build constraints, vendor directories and the testdata exclusion — writing
// our own walker would quietly disagree with the go tool about what a package
// is.
//
// Directories without test files are dropped: they cost a build and can never
// produce a witness.
func ExpandPatterns(ctx context.Context, args []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}

	for _, arg := range args {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dirs, err := expandOne(ctx, arg)
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			if d == "" || seen[d] {
				continue
			}
			if !hasTestFiles(d) {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) == 0 && len(args) > 0 {
		// An empty list reads as "clean"; the user must know their argument
		// matched no packages at all.
		return nil, domain.Wrap(domain.ErrUsage,
			"%s matched no test packages", strings.Join(args, " "))
	}
	sort.Strings(out)
	return out, nil
}

// expandOne resolves one argument: a plain directory is used as-is, anything
// with an ellipsis or a bare import path goes through `go list`.
func expandOne(ctx context.Context, arg string) ([]string, error) {
	if st, err := os.Stat(arg); err == nil && st.IsDir() && !strings.Contains(arg, "...") {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, domain.Wrap(domain.ErrUsage, "%s", arg)
		}
		return []string{abs}, nil
	}

	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-f", "{{.Dir}}", arg)
	// `go list` must run inside a module; the caller's cwd is that module.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, domain.Wrap(domain.ErrUsage, "%s could not be resolved: %s",
			arg, strings.TrimSpace(stderr.String()))
	}

	var dirs []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dirs = append(dirs, line)
		}
	}
	if len(dirs) == 0 {
		return nil, domain.Wrap(domain.ErrUsage, "%s matched no packages", arg)
	}
	return dirs, nil
}

func hasTestFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}
