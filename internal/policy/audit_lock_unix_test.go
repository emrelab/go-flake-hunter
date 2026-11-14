//go:build !windows

package policy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAuditC7_LockWaitCanCancelAndRetry(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	path := lockPath(canonical(dir))
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, release, err := Locked(context.Background(), dir); err == nil {
		release()
		t.Fatal("opening a lock file over a directory should fail")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = funlock(f)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, unlock, err := Locked(ctx, dir)
		unlock()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("retry bypassed the OS lock or returned the wrong error: %v", err)
		}
	case <-time.After(900 * time.Millisecond):
		_ = funlock(f)
		released = true
		<-done
		t.Fatal("OS lock wait did not cancel within one second")
	}
	if err := funlock(f); err != nil {
		t.Fatal(err)
	}
	released = true
	_, unlock, err := Locked(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("retry did not acquire the OS lock")
	}
}
