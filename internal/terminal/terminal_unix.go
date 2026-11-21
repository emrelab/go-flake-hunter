//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type nativeState struct{ input syscall.Termios }

func ioctl(fd uintptr, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var state syscall.Termios
	return ioctl(f.Fd(), readTermios, unsafe.Pointer(&state)) == nil
}

func openNative(in, out *os.File) (*Session, error) {
	if !isTerminal(in) || !isTerminal(out) {
		return nil, fmt.Errorf("interactive stdin and stdout are required")
	}
	s := &Session{in: in, out: out}
	if err := ioctl(in.Fd(), readTermios, unsafe.Pointer(&s.state.input)); err != nil {
		return nil, err
	}
	raw := s.state.input
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON | syscall.INLCR | syscall.IGNCR | syscall.PARMRK
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(in.Fd(), writeTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) restore() error {
	return ioctl(s.in.Fd(), writeTermios, unsafe.Pointer(&s.state.input))
}

func (s *Session) size() (int, int, error) {
	var window struct{ Rows, Columns, X, Y uint16 }
	err := ioctl(s.out.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&window))
	return int(window.Columns), int(window.Rows), err
}

func (s *Session) read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd := int(s.in.Fd())
	var ready syscall.FdSet
	bits := int(unsafe.Sizeof(ready.Bits[0]) * 8)
	if fd < 0 || fd/bits >= len(ready.Bits) {
		return nil, fmt.Errorf("terminal descriptor is outside select's supported range")
	}
	ready.Bits[fd/bits] |= 1 << uint(fd%bits)
	timeout := syscall.Timeval{Usec: 100000}
	err := selectInput(fd+1, &ready, &timeout)
	if errors.Is(err, syscall.EINTR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ready.Bits[fd/bits]&(1<<uint(fd%bits)) == 0 {
		return nil, nil
	}
	var data [256]byte
	n, err := syscall.Read(fd, data[:])
	if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("terminal input closed")
	}
	return data[:n], nil
}
