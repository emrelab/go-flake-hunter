//go:build windows

package policy

import (
	"context"
	"os"
)

// Windows has no flock in the stdlib; the in-process mutex still serializes
// this binary, which is the common case (one hunt at a time).
func flockExclusive(ctx context.Context, _ *os.File) error { return ctx.Err() }

func funlock(f *os.File) error { return nil }
