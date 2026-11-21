package gotest

import (
	"context"
	"encoding/json"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// Environment asks the selected Go toolchain for prerequisite metadata only.
func Environment(ctx context.Context, dir string) (map[string]string, error) {
	cmd := commandContext(ctx, "go", "env", "-json", "GOVERSION", "GOMOD", "GOWORK", "CGO_ENABLED", "GOOS", "GOARCH")
	cmd.Dir = dir
	data, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, domain.Wrap(domain.ErrUsage, "Go is unavailable or go env failed; check PATH and GOTOOLCHAIN")
	}
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, domain.Wrap(domain.ErrUsage, "go env returned invalid metadata")
	}
	return env, nil
}
