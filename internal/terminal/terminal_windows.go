package terminal

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var console = syscall.NewLazyDLL("kernel32.dll")
var getMode = console.NewProc("GetConsoleMode")
var setMode = console.NewProc("SetConsoleMode")
var getSize = console.NewProc("GetConsoleScreenBufferInfo")
var waitInput = console.NewProc("WaitForSingleObject")
var readInput = console.NewProc("ReadConsoleInputW")

type nativeState struct {
	input, output uint32
	keys          consoleKeys
}

// Keep pointers passed through uintptr arguments alive across the native call.
//
//go:uintptrescapes
func consoleCall(proc *syscall.LazyProc, args ...uintptr) error {
	ok, _, err := proc.Call(args...)
	if ok == 0 {
		if err != syscall.Errno(0) {
			return err
		}
		return fmt.Errorf("%s failed", proc.Name)
	}
	return nil
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var mode uint32
	return consoleCall(getMode, f.Fd(), uintptr(unsafe.Pointer(&mode))) == nil
}

func openNative(in, out *os.File) (*Session, error) {
	if !isTerminal(in) || !isTerminal(out) {
		return nil, fmt.Errorf("interactive Windows console input and output are required")
	}
	s := &Session{in: in, out: out}
	if err := consoleCall(getMode, in.Fd(), uintptr(unsafe.Pointer(&s.state.input))); err != nil {
		return nil, err
	}
	if err := consoleCall(getMode, out.Fd(), uintptr(unsafe.Pointer(&s.state.output))); err != nil {
		return nil, err
	}
	// ReadConsoleInput receives keys directly; quick-edit must not freeze rendering.
	input := s.state.input &^ uint32(0x1|0x2|0x4|0x10|0x20|0x40|0x200)
	input |= 0x80
	if err := consoleCall(setMode, in.Fd(), uintptr(input)); err != nil {
		return nil, err
	}
	if err := consoleCall(setMode, out.Fd(), uintptr(s.state.output|0x1|0x4)); err != nil {
		_ = consoleCall(setMode, in.Fd(), uintptr(s.state.input))
		return nil, err
	}
	return s, nil
}

func (s *Session) restore() error {
	first := consoleCall(setMode, s.in.Fd(), uintptr(s.state.input))
	second := consoleCall(setMode, s.out.Fd(), uintptr(s.state.output))
	if first != nil {
		return first
	}
	return second
}

func (s *Session) size() (int, int, error) {
	var info struct {
		SizeX, SizeY, CursorX, CursorY       int16
		Attributes                           uint16
		Left, Top, Right, Bottom, MaxX, MaxY int16
	}
	err := consoleCall(getSize, s.out.Fd(), uintptr(unsafe.Pointer(&info)))
	return int(info.Right - info.Left + 1), int(info.Bottom - info.Top + 1), err
}

func (s *Session) read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	status, _, err := waitInput.Call(s.in.Fd(), 100)
	if status == 258 {
		return nil, nil
	}
	if status != 0 {
		return nil, err
	}
	var record consoleRecord
	var n uint32
	if err := consoleCall(readInput, s.in.Fd(), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&n))); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	return s.state.keys.key(record), nil
}
