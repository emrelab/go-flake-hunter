//go:build darwin || linux

package gotest

func cancellationReset(error) bool { return false }

func observeCancellationProcess(int) (func() error, func() error, error) {
	// These helpers create their socket after spawning descendants; EOF proves its owner exited.
	return func() error { return nil }, func() error { return nil }, nil
}
