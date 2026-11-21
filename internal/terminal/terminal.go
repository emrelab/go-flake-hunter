// Package terminal owns native console state for the interactive application.
package terminal

import (
	"context"
	"os"
)

// Session holds the exact terminal modes to restore on exit.
type Session struct {
	in, out *os.File
	state   nativeState
}

// Open enables character input after checking both streams are terminals.
func Open(in, out *os.File) (*Session, error) { return openNative(in, out) }

// IsTerminal checks the native terminal API, not just the file's device bit.
func IsTerminal(f *os.File) bool { return isTerminal(f) }

// Read polls with cancellation so no stdin reader survives the session.
func (s *Session) Read(ctx context.Context) ([]byte, error) { return s.read(ctx) }

// Size returns visible columns and rows.
func (s *Session) Size() (int, int, error) { return s.size() }

// Close restores the original modes. Call it even after an output error.
func (s *Session) Close() error { return s.restore() }
