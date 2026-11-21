package gotest

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestWindowsJobCloseStopsDescendants(t *testing.T) {
	// The driver exits normally while its descendants still own output pipes.
	// WaitDelay bounds that wait; job closure must then terminate both descendants.
	testCommandTree(t, context.Background(), func() {}, true, func(command any) {
		cmd := command.(*windowsCommand)
		cmd.Env = append(cmd.Env, "GFH_CANCEL_HELPER=driver-exit")
	})
}

func TestWindowsJobTerminationFailureStillStopsDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var called atomic.Bool
	var cmd *windowsCommand
	testCommandTree(t, ctx, cancel, false, func(command any) {
		cmd = command.(*windowsCommand)
		cmd.terminate = func(syscall.Handle) error {
			called.Store(true)
			return syscall.ERROR_ACCESS_DENIED
		}
	})
	if !testing.Short() && !called.Load() {
		t.Fatal("termination failure path was not exercised")
	}
	if cmd != nil && cmd.job != 0 {
		t.Fatal("failed termination retained a job handle")
	}
}

func TestWindowsJobHandles(t *testing.T) {
	if testing.Short() {
		t.Skip("native process/handle lifecycle")
	}
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		name := exe
		if mode == "start failure" {
			name = exe + "-missing"
		}
		if mode == "expired deadline" {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
		}
		cmd := commandContext(ctx, name, "-test.run=^TestCancellationHelper$")
		cmd.Env = append(os.Environ(), "GFH_CANCEL_HELPER=exit")
		// Output must invoke the wrapped lifecycle, not the embedded exec.Cmd.Run.
		output, err := cmd.Output()
		if mode == "success" {
			if err != nil || len(output) == 0 {
				t.Fatalf("normal output: %q %v", output, err)
			}
		} else if err == nil {
			t.Fatalf("%s unexpectedly succeeded", mode)
		}
		if cmd.job != 0 {
			t.Fatalf("%s retained a job handle", mode)
		}
	}
	handles := func() uint32 {
		t.Helper()
		process, err := syscall.GetCurrentProcess()
		if err != nil {
			t.Fatal(err)
		}
		var count uint32
		if err := jobCall(jobKernel.NewProc("GetProcessHandleCount"), uintptr(process), uintptr(unsafe.Pointer(&count))); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for _, mode := range []string{"success", "start failure", "expired deadline"} {
		run(mode)
	}
	runtime.GC()
	before := handles()
	for range 20 {
		for _, mode := range []string{"success", "start failure", "expired deadline"} {
			run(mode)
		}
	}
	// Runtime worker threads can appear during the test; a per-command leak cannot.
	if after := handles(); after > before+2 {
		t.Fatalf("handles grew across 60 command lifecycles: %d -> %d", before, after)
	}
}

func TestWindowsJobSetupFailsClosed(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := commandContext(ctx, exe)
	if err := cmd.Run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start: %v", err)
	}
	if cmd.Process != nil || cmd.job != 0 {
		t.Fatal("cancelled start retained a process or job")
	}
}
