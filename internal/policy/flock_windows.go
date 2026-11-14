//go:build windows

package policy

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Windows has no flock; LockFileEx is the equivalent primitive and the stdlib
// does not wrap it. Without it the in-process mutex was the only serialization,
// so two hunt processes in one directory could list tests while the other had
// a witness file on disk (S7).
var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
	procUnlockFile = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x1
	// errLockViolation is ERROR_LOCK_VIOLATION (33), which the syscall
	// package does not name.
	errLockViolation = syscall.Errno(33)
)

func flockExclusive(ctx context.Context, f *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ov syscall.Overlapped
		// One byte at offset 0: the range is a token, the file carries no data.
		ok, _, callErr := procLockFileEx.Call(uintptr(f.Fd()),
			lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0,
			uintptr(unsafe.Pointer(&ov)))
		if ok != 0 {
			return nil
		}
		if !errors.Is(callErr, errLockViolation) {
			// A zero Errno on a failed call must not read as "lock held".
			if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
				return errors.New("LockFileEx failed without an error code")
			}
			return callErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func funlock(f *os.File) error {
	var ov syscall.Overlapped
	ok, _, callErr := procUnlockFile.Call(uintptr(f.Fd()), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if ok == 0 {
		return callErr
	}
	return nil
}
