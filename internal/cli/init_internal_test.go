package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
