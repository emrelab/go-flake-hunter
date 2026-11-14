package gotest

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// BuildTags reads the effective -tags flag from Go's configuration.
func BuildTags(ctx context.Context, dir string) []string {
	cmd := exec.CommandContext(ctx, "go", "env", "GOFLAGS")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var tags []string
	for _, arg := range strings.Fields(string(out)) {
		if value, ok := strings.CutPrefix(strings.Trim(arg, "'\""), "-tags="); ok {
			tags = strings.FieldsFunc(value, func(r rune) bool { return r == ',' })
		}
	}
	return tags
}

// PackageFiles asks the target Go toolchain which files the build will use.
func PackageFiles(ctx context.Context, dir string, tags []string) (domain.PackageFiles, error) {
	args := []string{"list", "-e", "-json"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	cmd := exec.CommandContext(ctx, "go", append(args, ".")...)
	cmd.Dir = dir
	var p domain.PackageFiles
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	if err != nil {
		return p, fmt.Errorf("list package in %s: %w", dir, err)
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return p, fmt.Errorf("read package metadata for %s: %w", dir, err)
	}
	if p.Dir == "" {
		return p, domain.Wrap(domain.ErrBuildFailed, "no package metadata for %s", dir)
	}
	return p, nil
}

func TargetGoVersion(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
