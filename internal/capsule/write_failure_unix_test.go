//go:build darwin || linux

package capsule_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/capsule"
	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func TestStorePartialWritePreservesEvidence(t *testing.T) {
	if dir := os.Getenv("GFH_WRITE_FAILURE_STORE"); dir != "" {
		// Isolate the process-wide limit: the write really fails after one byte.
		signal.Ignore(syscall.SIGXFSZ)
		var original syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Fatal(err)
		}
		limited := original
		limited.Cur = 1
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
			t.Fatal(err)
		}
		// The test runner still needs to write coverage metadata after this test returns.
		defer func() {
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
				t.Error(err)
			}
		}()
		c := domain.Capsule{ID: "gfh_existing", Schema: domain.SchemaVersion, Kind: domain.KindOrder, Note: "replacement"}
		if _, err := capsule.NewStore(dir).Write(c); err == nil {
			t.Fatal("partial write unexpectedly succeeded")
		}
		c.ID = "gfh_new"
		if _, err := capsule.NewStore(dir).Write(c); err == nil {
			t.Fatal("partial new write unexpectedly succeeded")
		}
		return
	}
	store := capsule.NewStore(t.TempDir())
	original := domain.Capsule{ID: "gfh_existing", Schema: domain.SchemaVersion, Kind: domain.KindOrder, Note: "original evidence"}
	path, err := store.Write(original)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestStorePartialWritePreservesEvidence$")
	cmd.Env = append(os.Environ(), "GFH_WRITE_FAILURE_STORE="+store.Dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("partial-write helper: %v\n%s", err, output)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("failed replacement changed evidence: %v", err)
	}
	entries, err := os.ReadDir(store.Dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("partial write left files: %v %v", entries, err)
	}
	if _, err := store.Read(original.ID); err != nil {
		t.Fatalf("original evidence is unreadable: %v", err)
	}
}
