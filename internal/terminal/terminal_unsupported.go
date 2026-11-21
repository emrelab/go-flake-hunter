//go:build !darwin && !linux && !windows

package terminal

import (
	"context"
	"fmt"
	"os"
)

type nativeState struct{}

func isTerminal(*os.File) bool { return false }
func openNative(*os.File, *os.File) (*Session, error) {
	return nil, fmt.Errorf("TUI supports macOS, Linux and Windows consoles; use the CLI on this platform")
}
func (*Session) restore() error          { return nil }
func (*Session) size() (int, int, error) { return 0, 0, fmt.Errorf("terminal size is unavailable") }
func (*Session) read(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("terminal input is unavailable")
}
