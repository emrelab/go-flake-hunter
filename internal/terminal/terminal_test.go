package terminal

import (
	"os"
	"testing"
)

func TestRedirectedStreamsAreNotTerminals(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if IsTerminal(f) {
		t.Fatal("regular file reported as terminal")
	}
	if session, err := Open(f, f); err == nil {
		_ = session.Close()
		t.Fatal("raw mode accepted redirected streams")
	}
}
