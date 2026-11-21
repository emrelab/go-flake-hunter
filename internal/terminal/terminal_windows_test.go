package terminal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestWindowsConsoleIntegration(t *testing.T) {
	if os.Getenv("GFH_CONSOLE_HELPER") != "1" {
		if testing.Short() {
			t.Skip("native Windows console subprocess")
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWindowsConsoleIntegration$")
		cmd.Env = append(os.Environ(), "GFH_CONSOLE_HELPER=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native console: %v\n%s", err, output)
		}
		return
	}
	// Detach only this helper, preserving the runner's redirected stdio.
	_, _, _ = console.NewProc("FreeConsole").Call()
	if err := consoleCall(console.NewProc("AllocConsole")); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = consoleCall(console.NewProc("FreeConsole")) }()
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	readMode := func(f *os.File) uint32 {
		t.Helper()
		var mode uint32
		if err := consoleCall(getMode, f.Fd(), uintptr(unsafe.Pointer(&mode))); err != nil {
			t.Fatal(err)
		}
		return mode
	}
	inputMode, outputMode := readMode(in), readMode(out)
	// Repeat opening/restoring to expose retained modes and native handles.
	for range 3 {
		session, err := Open(in, out)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if err := session.Close(); err != nil {
					t.Error(err)
				}
			}()
			if readMode(in)&(0x1|0x2|0x4|0x40) != 0 {
				t.Error("processed/line/echo/quick-edit mode still active")
			}
			if readMode(out)&0x4 == 0 {
				t.Error("VT output disabled")
			}
			for _, dimensions := range [][2]int16{{80, 25}, {70, 20}} {
				window := struct{ Left, Top, Right, Bottom int16 }{Right: dimensions[0] - 1, Bottom: dimensions[1] - 1}
				if err := consoleCall(console.NewProc("SetConsoleWindowInfo"), out.Fd(), 1, uintptr(unsafe.Pointer(&window))); err != nil {
					t.Fatal(err)
				}
				w, h, err := session.Size()
				if err != nil || w != int(dimensions[0]) || h != int(dimensions[1]) {
					t.Fatalf("console resize: got %dx%d, want %dx%d (%v)", w, h, dimensions[0], dimensions[1], err)
				}
			}

			records := []consoleRecord{
				keyRecord(0, 'ş', 3, 0), keyRecord(0, 0xD83D, 2, 0), keyRecord(0, 0xDE00, 2, 0),
				keyRecord(0x26, 0, 2, 0), keyRecord(0x28, 0, 1, 0), keyRecord(0x27, 0, 1, 0), keyRecord(0x25, 0, 1, 0),
				keyRecord(9, 9, 1, 0x10), keyRecord('C', 3, 1, 0x8),
			}
			if err := consoleCall(console.NewProc("FlushConsoleInputBuffer"), in.Fd()); err != nil {
				t.Fatal(err)
			}
			var written uint32
			if err := consoleCall(console.NewProc("WriteConsoleInputW"), in.Fd(), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&written))); err != nil {
				t.Fatal(err)
			}
			if int(written) != len(records) {
				t.Fatal("short console input write")
			}
			want := "şşş😀😀\x1b[A\x1b[A\x1b[B\x1b[C\x1b[D\x1b[Z\x03"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var got strings.Builder
			for got.Len() < len(want) {
				data, err := session.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got.Write(data)
			}
			if got.String() != want {
				t.Fatalf("native input = %q, want %q", got.String(), want)
			}
			cancelCtx, cancelRead := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := session.Read(cancelCtx); done <- err }()
			cancelRead()
			// Read is a bounded poll; retry after an idle read to observe cancellation.
			select {
			case err := <-done:
				if err == nil {
					_, err = session.Read(cancelCtx)
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("read cancellation: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("console reader survived cancellation")
			}
		}()
		if readMode(in) != inputMode || readMode(out) != outputMode {
			t.Fatal("console modes were not restored")
		}
	}
}
