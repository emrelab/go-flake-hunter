//go:build !darwin && !linux && !windows

package gotest

import "os/exec"

func configureCancellation(_ *exec.Cmd) {}
