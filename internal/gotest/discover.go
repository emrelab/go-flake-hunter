package gotest

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// Discover lists test functions in source order.
//
// `go test -list '.*'` prints them in file/declaration order, which is exactly
// the baseline order the order probe needs (verified on Go 1.26.2:
// -run alternatives do NOT reorder execution, so this is the only source of
// truth for "what order would the package run in by default").
func (e *Exec) Discover(ctx context.Context, pkgDir string) ([]domain.TestID, error) {
	ctx, release, lerr := policy.Locked(ctx, pkgDir)
	if lerr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, domain.Wrap(domain.ErrInternal, "could not acquire lock: %v", lerr)
	}
	defer release()

	cmd := exec.CommandContext(ctx, e.goBin(), "test", "-list", ".*", ".")
	cmd.Dir = pkgDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		return nil, domain.Wrap(domain.ErrBuildFailed, "%s could not be listed: %s",
			pkgDir, firstLines(detail, 3))
	}

	importPath, err := e.importPath(ctx, pkgDir)
	if err != nil {
		return nil, err
	}

	var tests []domain.TestID
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "Test") {
			continue // "ok pkg 0.1s", "? pkg [no test files]", blank
		}
		if strings.ContainsAny(line, " \t") {
			continue // "ok  \tpkg\t0.1s" and friends
		}
		tests = append(tests, domain.TestID{Package: importPath, Name: line})
	}
	return tests, nil
}

// importPath resolves the module import path for a directory, so TestID carries
// a stable identity (":" in a capsule must not depend on the caller's cwd).
func (e *Exec) importPath(ctx context.Context, pkgDir string) (string, error) {
	cmd := exec.CommandContext(ctx, e.goBin(), "list", "-f", "{{.ImportPath}}", ".")
	cmd.Dir = pkgDir
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", domain.Wrap(domain.ErrBuildFailed, "%s import path could not be resolved", pkgDir)
	}
	return strings.TrimSpace(string(out)), nil
}
