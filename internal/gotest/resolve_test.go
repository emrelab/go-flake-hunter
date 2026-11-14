package gotest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The map is the trust boundary between a recorded package name and a
// directory: everything outside the roots must be absent from it.
func TestResolveImportDirsBindsPathsToRoots(t *testing.T) {
	root := t.TempDir()
	writeNested(t, filepath.Join(root, "go.mod"), "module example.com/rooted\n\ngo 1.24\n")
	writeNested(t, filepath.Join(root, "a", "a.go"), "package a\n")
	writeNested(t, filepath.Join(root, "b", "b.go"), "package b\n")
	// A nested module is a separate checkout: ./... must not descend into it.
	writeNested(t, filepath.Join(root, "b", "nested", "go.mod"), "module example.com/nested\n\ngo 1.24\n")
	writeNested(t, filepath.Join(root, "b", "nested", "n.go"), "package nested\n")

	dirs, err := ResolveImportDirs(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("ResolveImportDirs: %v", err)
	}

	if got, want := dirs["example.com/rooted/a"], filepath.Join(root, "a"); got != want {
		t.Errorf("a -> %q, want %q", got, want)
	}
	if got, want := dirs["example.com/rooted/b"], filepath.Join(root, "b"); got != want {
		t.Errorf("b -> %q, want %q", got, want)
	}
	// The nested module is absent, so a capsule naming it cannot reach it.
	if dir, ok := dirs["example.com/nested"]; ok {
		t.Errorf("nested module resolved to %q; a nested module is not part of the root checkout", dir)
	}
	if _, ok := dirs["example.com/outside"]; ok {
		t.Error("unrelated import path resolved; the map must only contain packages under the roots")
	}
}

func TestResolveImportDirsUnlistsOutsideModule(t *testing.T) {
	// No go.mod: `go list ./...` fails, and that must be a usage error rather
	// than an empty map that reads as "no packages".
	if _, err := ResolveImportDirs(context.Background(), []string{t.TempDir()}); err == nil {
		t.Fatal("a directory outside any module must not resolve to an empty map")
	}
}

// writeNested creates the parent directories too: the fixtures below live in
// package subdirectories that do not exist yet.
func writeNested(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, content)
}
