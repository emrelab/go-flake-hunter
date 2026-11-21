package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInitDoesNotFollowDanglingWorkflowSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "must-not-create.yml")
	path := filepath.Join(root, workflowPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var out bytes.Buffer
	_ = initCmd(context.Background(), []string{root}, IO{Stdout: &out, Stderr: &out})
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init wrote through a dangling workflow symlink")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("init replaced the existing symlink")
	}
}

func TestAuditC3_ReplayScaffoldGatePreservesExitContract(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "go-flake-hunter-bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for path, script := range map[string]string{filepath.Join(bin, "go-flake-hunter"): "#!/bin/sh\nprintf 'run    : %s reproduced\\n' \"$REPLAY_OBSERVATION\"\nexit \"$REPLAY_STATUS\"\n", filepath.Join(bin, "go"): "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	gate := replayGate(".go-flake-hunter/gfh_123.json")
	_, body, ok := strings.Cut(gate, "        run: |\n")
	if !ok {
		t.Fatal("no run block")
	}
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	script := strings.Join(lines, "\n")
	for _, tc := range []struct {
		input, want int
		observation string
	}{
		{0, 1, "3/3"}, {3, 1, "2/3"}, {3, 0, "0/3"},
		{3, 3, "0/0"}, {6, 6, "0/3"}, {130, 130, "0/3"},
	} {
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+filepath.ToSlash(dir), "REPLAY_STATUS="+strconv.Itoa(tc.input), "REPLAY_OBSERVATION="+tc.observation)
		output, err := cmd.CombinedOutput()
		got := 0
		if err != nil {
			e, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			got = e.ExitCode()
		}
		if got != tc.want {
			t.Fatalf("replay %d -> gate %d, want %d: %s", tc.input, got, tc.want, output)
		}
	}
}
