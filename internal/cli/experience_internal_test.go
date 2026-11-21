package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

func TestCommandHelpWorkflow(t *testing.T) {
	for _, command := range commands() {
		var out, errs bytes.Buffer
		if code := Execute(context.Background(), []string{"help", command.Name}, IO{Stdout: &out, Stderr: &errs}); code != 0 {
			t.Fatalf("help %s: %d %s", command.Name, code, errs.String())
		}
		for _, want := range []string{"Usage:", "Examples:", "go-flake-hunter " + command.Name, "--debug"} {
			if !strings.Contains(errs.String(), want) {
				t.Errorf("%s help missing %q: %s", command.Name, want, errs.String())
			}
		}
		if command.Name == "hunt" {
			for _, group := range []string{"Search:", "Budget:", "Output:", "CI:"} {
				if !strings.Contains(errs.String(), group) {
					t.Error("missing " + group)
				}
			}
		}
	}
}

func TestFlagTerminatorPreservesLiteralArguments(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := fs.Bool("v", false, "")
	args, debug := stripDebug([]string{"--debug", "first", "-v", "--", "--debug", "--literal"})
	if !debug {
		t.Fatal("global debug lost")
	}
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		t.Fatal(err)
	}
	if !*v || strings.Join(fs.Args(), ",") != "first,--debug,--literal" {
		t.Fatalf("v=%v args=%v", *v, fs.Args())
	}
}

func TestProgressConcurrentLifecycle(t *testing.T) {
	var log bytes.Buffer
	s := newScanner(IO{Stderr: &log, Stdout: io.Discard}, huntOptions{format: "text", progress: "plain"}, "")
	stop := s.startProgress(4, "Packages")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := string(rune('a' + i))
			s.progress.update(label, "discovering")
			s.progress.update(label, "repeat")
			_, _ = io.WriteString(s.out.Stderr, "diagnostic\n")
			s.progress.finish(label, nil)
		}(i)
	}
	wg.Wait()
	stop()
	stop()
	text := log.String()
	if strings.Count(text, "[scan]") != 4 || strings.Count(text, "[done ") != 4 || !strings.Contains(text, "[done 4/4]") || strings.ContainsAny(text, "\r\x1b") {
		t.Fatalf("corrupt progress: %q", text)
	}
	for _, mode := range []string{"off", "auto"} {
		log.Reset()
		s := newScanner(IO{Stderr: &log}, huntOptions{format: "json", progress: mode}, "")
		stop := s.startProgress(1, "Packages")
		s.progress.update("p", "repeat")
		s.progress.finish("p", nil)
		stop()
		if log.Len() != 0 {
			t.Fatalf("structured output got automatic progress: %q", log.String())
		}
	}
	if !errors.Is(validateProgress("bogus"), domain.ErrUsage) {
		t.Fatal("invalid mode accepted")
	}
	t.Run("live lifecycle", func(t *testing.T) {
		t.Setenv("CI", "")
		t.Setenv("TERM", "xterm")
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if !terminal(f) {
			t.Skip("character device unavailable")
		}
		s := newScanner(IO{Stdout: io.Discard, Stderr: f}, huntOptions{format: "text", progress: "auto"}, "")
		stop := s.startProgress(1, "Packages")
		defer stop()
		var workers sync.WaitGroup
		for range 4 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for range 100 {
					s.progress.update("p", "repeat")
					_, _ = s.out.Stderr.Write([]byte("diagnostic\n"))
				}
			}()
		}
		workers.Wait()
		stop()
		select {
		case <-s.progress.done:
		default:
			t.Fatal("live renderer did not stop")
		}
	})
}

func TestLiveProgressKeepsDiagnosticsAndUTF8(t *testing.T) {
	var log, output bytes.Buffer
	p := &scanProgress{w: &log, budget: policy.New(10, time.Minute), started: time.Now(), active: map[string]string{}, total: 1, scope: "Packages", live: true}
	p.update("dir\x1b[31m\n"+strings.Repeat("ç", 100), "order")
	if _, err := p.Write([]byte("diagnostic\n")); err != nil {
		t.Fatal(err)
	}
	w := progressOutput{p, &output}
	_, _ = w.Write([]byte("first"))
	before := log.Len()
	p.update("another", "repeat")
	if log.Len() != before {
		t.Fatal("redrew over an unfinished output line")
	}
	_, _ = w.Write([]byte(" line\n"))
	p.finish("another", context.Canceled)
	p.mu.Lock()
	p.clear()
	p.mu.Unlock()
	if !utf8.ValidString(log.String()) || strings.Contains(log.String(), "\x1b") || !strings.Contains(log.String(), "diagnostic\n") || output.String() != "first line\n" {
		t.Fatalf("log=%q out=%q", log.String(), output.String())
	}
}

func TestDoctorChecksWithoutRunningTests(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":    "module fixture/doctor\n\ngo 1.24\n",
		"p_test.go": "package p\nimport \"testing\"\nfunc init(){panic(\"doctor must not run tests\")}\nfunc TestUnsafe(t *testing.T){}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOWORK", "off")
	var out, errs bytes.Buffer
	code := Execute(context.Background(), []string{"doctor", dir, "--format", "json"}, IO{Stdout: &out, Stderr: &errs})
	var result struct {
		Schema string
		Ready  bool
		Checks []doctorCheck
	}
	if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || !result.Ready || result.Schema != "go-flake-hunter/doctor/v1" {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errs.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("doctor left files: %v %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain.CapsuleDir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := Execute(context.Background(), []string{"doctor", dir}, IO{Stdout: &out, Stderr: &errs}); code != domain.ExitUsage || !strings.Contains(out.String(), "FAIL") {
		t.Fatalf("invalid store accepted: %d %s %s", code, out.String(), errs.String())
	}
}

func TestDoctorDefaultsToRecursivePattern(t *testing.T) {
	// The usual layout has no test file beside go.mod; a bare doctor must still
	// find the packages a bare hunt would scan.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":          "module fixture/doctorroot\n\ngo 1.24\n",
		"pkg/pkg.go":      "package pkg\n",
		"pkg/pkg_test.go": "package pkg\nimport \"testing\"\nfunc TestOne(t *testing.T){}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	var out, errs bytes.Buffer
	if code := Execute(context.Background(), []string{"doctor"}, IO{Stdout: &out, Stderr: &errs}); code != 0 {
		t.Fatalf("bare doctor failed: %d\n%s%s", code, out.String(), errs.String())
	}
	if !strings.Contains(out.String(), "1 test package directories resolved") {
		t.Fatalf("recursive default not resolved: %s", out.String())
	}
}

func TestDoctorHidesSentinelSuffix(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module fixture/doctornotest\n\ngo 1.24\n",
		"pkg.go": "package pkg\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"doctor", "."}, IO{Stdout: &out, Stderr: &out})
	if code != domain.ExitUsage || !strings.Contains(out.String(), "matched no test packages") {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if strings.Contains(out.String(), "usage error") {
		t.Fatalf("sentinel suffix reached the user: %s", out.String())
	}
}

func TestDoctorMissingGoAndCancellation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, errs bytes.Buffer
	if code := Execute(context.Background(), []string{"doctor", "--format", "json"}, IO{Stdout: &out, Stderr: &errs}); code != domain.ExitUsage || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"ready":false`) {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := Execute(ctx, []string{"doctor"}, IO{}); code != domain.ExitCanceled {
		t.Fatalf("cancel code=%d", code)
	}
	if code := Execute(context.Background(), []string{"doctor", "--timeout", "0s"}, IO{}); code != domain.ExitUsage {
		t.Fatalf("invalid timeout code=%d", code)
	}
	out.Reset()
	errs.Reset()
	// A past deadline is synchronous; a 1 ns timer can race a fast missing-Go error on Windows.
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	err := doctorCmd(expired, []string{"--format", "json"}, IO{Stdout: &out, Stderr: &errs})
	if !errors.Is(err, context.DeadlineExceeded) || domain.ExitCode(err) != domain.ExitCanceled || !json.Valid(out.Bytes()) {
		t.Fatalf("timeout err=%v out=%s diagnostics=%s", err, out.String(), errs.String())
	}
}
