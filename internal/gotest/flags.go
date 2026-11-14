package gotest

import (
	"context"
	"os/exec"
	"strings"
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
