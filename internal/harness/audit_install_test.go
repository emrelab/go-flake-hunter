package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/harness"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

func TestAuditS4_PreservesUserWitnessAndSymlinkTarget(t *testing.T) {
	const original = "package p\n// handwritten\n"
	for _, symlink := range []bool{false, true} {
		name := "regular"
		if symlink {
			name = "symlink"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, unlock, err := policy.Locked(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()

			path := filepath.Join(dir, domain.WitnessFileName("collision"))
			userFile := path
			if symlink {
				userFile = filepath.Join(t.TempDir(), "user_test.go")
			}
			if err := os.WriteFile(userFile, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(userFile, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}

			steps := []domain.Step{{Label: domain.VictimLabel("TestVictim")}}
			if _, err := harness.Install(dir, "p", domain.WitnessFuncName, steps, "collision"); err == nil {
				t.Error("install accepted an occupied witness path")
			}
			if got, err := os.ReadFile(userFile); err != nil || string(got) != original {
				t.Errorf("install changed user file: %q (%v)", got, err)
			}

			if removed, err := harness.CleanupStale(dir); err != nil || len(removed) != 0 {
				t.Errorf("cleanup removed an unowned witness path: %v (%v)", removed, err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Errorf("user's witness path did not survive: %v", err)
			}
			if got, err := os.ReadFile(userFile); err != nil || string(got) != original {
				t.Errorf("cleanup changed user file: %q (%v)", got, err)
			}
		})
	}
}

func TestAuditS4_RemovePreservesReplacedWitness(t *testing.T) {
	dir := t.TempDir()
	_, unlock, err := policy.Locked(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	steps := []domain.Step{{Label: domain.VictimLabel("TestVictim")}}
	h, err := harness.Install(dir, "p", domain.WitnessFuncName, steps, "collision")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(h.Path(), filepath.Join(dir, "saved.go")); err != nil {
		t.Fatal(err)
	}
	const original = "package p\n// user replacement\n"
	if err := os.WriteFile(h.Path(), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = h.Remove()
	if got, err := os.ReadFile(h.Path()); err != nil || string(got) != original {
		t.Fatalf("Remove deleted user's replacement: %q (%v)", got, err)
	}
}
