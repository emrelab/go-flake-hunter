package cli

import "github.com/emrelab/go-flake-hunter/internal/harness"

func cleanupWitnesses(dir string) ([]string, error) {
	return harness.CleanupStale(dir)
}
