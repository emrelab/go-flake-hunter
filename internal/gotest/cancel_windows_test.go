package gotest

import (
	"errors"
	"fmt"
	"syscall"
)

func cancellationReset(err error) bool { return errors.Is(err, syscall.WSAECONNRESET) }

func observeCancellationProcess(pid int) (func() error, func() error, error) {
	// Open before cancellation so the handle pins the identity even if the PID is reused.
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, nil, err
	}
	return func() error {
		status, err := syscall.WaitForSingleObject(handle, 10000)
		if err != nil {
			return err
		}
		if status != syscall.WAIT_OBJECT_0 {
			return fmt.Errorf("process %d did not terminate (wait status %d)", pid, status)
		}
		return nil
	}, func() error { return syscall.CloseHandle(handle) }, nil
}
