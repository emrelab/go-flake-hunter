package gotest

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// ResolveImportDirs maps import path → package directory for every package
// under the given roots, as the go tool sees them.
//
// Why this exists: a capsule and a CI log name packages by import path, and
// the caller must turn that name into a directory it already trusts. Joining
// the recorded string onto the working directory instead is what let a log line
// (`Package: "../outside"`) or a hand-edited capsule run a package outside the
// checkout. The map is the only accepted bridge between the two.
//
// Roots are directories. Nested modules are not descended into: each is its own
// checkout and must be named as its own root.
func ResolveImportDirs(ctx context.Context, roots []string, tags ...string) (map[string]string, error) {
	out := make(map[string]string, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, domain.Wrap(domain.ErrUsage, "%s", root)
		}
		realRoot, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, domain.Wrap(domain.ErrUsage, "could not resolve %s: %v", root, err)
		}
		args := []string{"list", "-e", "-f", "{{.ImportPath}}\t{{.Dir}}"}
		if len(tags) > 0 {
			args = append(args, "-tags", strings.Join(tags, ","))
		}
		cmd := exec.CommandContext(ctx, "go", append(args, "./...")...)
		cmd.Dir = abs
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// A cancelled run is not a usage error: the caller asked to stop.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, domain.Wrap(domain.ErrUsage, "%s could not be listed: %s",
				root, firstLines(stderr.String(), 3))
		}
		lines := 0
		for _, line := range strings.Split(stdout.String(), "\n") {
			importPath, dir, ok := strings.Cut(strings.TrimSpace(line), "	")
			// -e reports unlistable packages with an empty Dir or the "..."
			// placeholder; neither names a directory this checkout owns.
			if !ok || importPath == "" || dir == "" || strings.Contains(importPath, "...") {
				continue
			}
			realDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(realRoot, realDir)
			if err != nil || !filepath.IsLocal(rel) {
				continue
			}
			if previous, ok := out[importPath]; ok && previous != dir {
				return nil, domain.Wrap(domain.ErrUsage, "package %s has more than one checkout directory", importPath)
			}
			out[importPath] = dir
			lines++
		}
		if lines == 0 {
			// An empty map would read as "no packages", the same silence a
			// clean scan produces. Say the root named nothing instead.
			return nil, domain.Wrap(domain.ErrUsage, "%s has no Go packages (is it inside a module?)", root)
		}
	}
	return out, nil
}
