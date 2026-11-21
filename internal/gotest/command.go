//go:build !windows

package gotest

import (
	"context"
	"os/exec"
	"time"
)

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// An inherited output pipe must not keep cancellation waiting indefinitely.
	cmd.WaitDelay = time.Second
	configureCancellation(cmd)
	return cmd
}
