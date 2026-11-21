package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

func TestTUIRepeatedTasksKeepUniquePackages(t *testing.T) {
	t.Chdir(t.TempDir())
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{dir}
	for range 3 {
		task := &tuiTask{seen: map[string]bool{dir: true}}
		r := tuiRunTask(context.Background(), tuiRequest{kind: "doctor"}, tuiConfig{}, dirs, task, func(context.Context, []string, IO) int { return 0 })
		dirs = r.dirs
	}
	if !reflect.DeepEqual(dirs, []string{dir}) {
		t.Fatalf("package count grew between tasks: %v", dirs)
	}
}

func TestTUIKeyboardFragmentation(t *testing.T) {
	input := "\x1b[Aş\x1b[200~go test\x1b[201~\x1b[999~\x03"
	want := []tuiKey{{name: "up"}, {"text", "ş"}, {"paste", "go test"}, {name: "interrupt"}}
	for split := 0; split <= len(input); split++ {
		var keyboard tuiKeyboard
		got := keyboard.feed([]byte(input[:split]), false)
		got = append(got, keyboard.feed([]byte(input[split:]), false)...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("split %d: %#v", split, got)
		}
	}
	var keyboard tuiKeyboard
	if got := keyboard.feed([]byte{27}, false); len(got) != 0 {
		t.Fatal(got)
	}
	if got := keyboard.feed(nil, true); len(got) != 1 || got[0].name != "escape" {
		t.Fatal(got)
	}
}

func TestTUISlowPasteHeaderCannotTriggerShortcuts(t *testing.T) {
	var keyboard tuiKeyboard
	if keys := keyboard.feed([]byte("\x1b[2"), false); len(keys) != 0 {
		t.Fatal(keys)
	}
	if keys := keyboard.feed(nil, true); len(keys) != 0 {
		t.Fatal(keys)
	}
	keys := keyboard.feed([]byte("00~nwy\r\x1b[201~"), false)
	if !reflect.DeepEqual(keys, []tuiKey{{"paste", "nwy\r"}}) {
		t.Fatalf("slow clipboard paste became shortcuts: %#v", keys)
	}
}

func TestTUIPasteCannotAuthorizeWrite(t *testing.T) {
	m := tuiApp{confirm: "compile", preview: "source", page: 2, previewView: true}
	m.key(tuiKey{"paste", "y"})
	if m.pending != nil || m.confirm != "compile" {
		t.Fatal("clipboard authorized a write")
	}
	m.key(tuiKey{"text", "y"})
	if m.pending == nil || m.pending.kind != "compile" || m.confirm != "" {
		t.Fatal("typed confirmation did not request write")
	}
	if m.pending.preview != "source" {
		t.Fatal("write request lost the approved source")
	}
	var keyboard tuiKeyboard
	got := keyboard.feed([]byte("\x1b[200~"+strings.Repeat("x", 5000)+"\x1b[201~"), false)
	if len(got) != 1 || got[0].name != "paste-too-long" || len(keyboard.paste) != 0 {
		t.Fatalf("oversized paste: %#v", got)
	}
}

func TestTUIEditorUnicodeAndValidation(t *testing.T) {
	m := tuiApp{editing: "filter", input: "iş", cursor: len("iş")}
	m.editKey(tuiKey{name: "left"})
	m.editKey(tuiKey{name: "backspace"})
	if m.input != "ş" || m.cursor != 0 {
		t.Fatalf("%q cursor %d", m.input, m.cursor)
	}
	m.editKey(tuiKey{name: "left"})
	m.editKey(tuiKey{"paste", "bad\ncommand"})
	if m.input != "ş" {
		t.Fatal("multiline paste accepted")
	}
	m.editKey(tuiKey{"paste", "ok\n"})
	m.editKey(tuiKey{name: "enter"})
	if m.filter != "okş" || m.input != "" || m.editing != "" {
		t.Fatalf("%#v", m)
	}
	for _, setting := range []int{1, 2, 3} {
		m.setting = setting
		for _, value := range []string{"", "-1", "0", "oops"} {
			if m.setSetting(value) == nil {
				t.Fatalf("setting %d accepted %q", setting, value)
			}
		}
	}
}

func TestTUIEnvironmentReplayDoesNotRetainSecrets(t *testing.T) {
	t.Chdir(t.TempDir())
	const secret = "private-test-value"
	task := &tuiTask{seen: map[string]bool{}}
	f := tuiFinding{path: "env.json", capsule: domain.Capsule{Kind: domain.KindEnv, Witness: domain.Witness{EnvDiff: []string{"API_KEY"}}}}
	r := tuiRunTask(context.Background(), tuiRequest{kind: "replay", finding: f, secret: secret}, tuiConfig{}, nil, task,
		func(_ context.Context, args []string, out IO) int {
			if !strings.Contains(strings.Join(args, " "), "API_KEY="+secret) {
				t.Fatal(args)
			}
			_, _ = fmt.Fprintln(out.Stdout, secret)
			_, _ = fmt.Fprintln(out.Stderr, secret)
			return 0
		})
	logs, _ := task.snapshot()
	if r.code != 0 || strings.Contains(strings.Join(logs, " ")+r.notice, secret) {
		t.Fatalf("secret retained: %v %#v", logs, r)
	}
}

func TestTUIQuitConfirmationSurvivesFinishedTask(t *testing.T) {
	// q while a task runs, the task finishing before the answer: the
	// confirmation must not outlive the task it was going to cancel.
	done := tuiApp{confirm: "quit"}
	if exit := done.key(tuiKey{"text", "y"}); !exit {
		t.Fatal("a finished task's quit confirmation did not exit")
	}
	cancelled := false
	live := tuiApp{confirm: "quit", task: &tuiTask{cancel: func() { cancelled = true }}}
	if exit := live.key(tuiKey{"text", "y"}); exit || !cancelled || !live.quitAfter || live.confirm != "" {
		t.Fatalf("live task: exit=%v cancelled=%v quitAfter=%v confirm=%q", exit, cancelled, live.quitAfter, live.confirm)
	}
}

func TestTUIChangedCapsuleCannotWrite(t *testing.T) {
	t.Chdir(t.TempDir())
	called := false
	r := tuiRunTask(context.Background(), tuiRequest{kind: "compile", finding: tuiFinding{path: "missing.json"}}, tuiConfig{}, nil, &tuiTask{},
		func(context.Context, []string, IO) int { called = true; return 0 })
	if called || r.code != domain.ExitUsage || !strings.Contains(r.notice, "changed since preview") {
		t.Fatalf("unsafe write: called %v, %#v", called, r)
	}
}

func TestTUITargetsCannotBecomeFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, kind := range []string{"scan", "doctor"} {
		called := false
		_ = tuiRunTask(context.Background(), tuiRequest{kind: kind}, tuiConfig{targets: []string{"--help", "path with spaces"}}, nil, &tuiTask{},
			func(_ context.Context, args []string, out IO) int {
				called = true
				if !reflect.DeepEqual(args[len(args)-3:], []string{"--", "--help", "path with spaces"}) {
					t.Fatal(args)
				}
				_, _ = fmt.Fprint(out.Stdout, `{"flakes":[],"packages":[],"runs":0}`)
				return 0
			})
		if !called {
			t.Fatal("command not called")
		}
	}
}

func TestTUIBoundedLogsAndReport(t *testing.T) {
	task := &tuiTask{}
	for i := 0; i < 300; i++ {
		_, _ = fmt.Fprintf(task, "line %d\n", i)
	}
	_, _ = task.Write([]byte(strings.Repeat("x", 10000)))
	logs, _ := task.snapshot()
	if len(logs) != 201 || logs[0] != "line 100" || len(logs[200]) != 4096 {
		t.Fatal("unbounded or incorrect log tail")
	}
	logs[0] = "changed"
	again, _ := task.snapshot()
	if again[0] == "changed" {
		t.Fatal("snapshot aliases logs")
	}
	var capture tuiCapture
	if _, err := capture.WriteString(strings.Repeat("x", 8<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.Write([]byte("overflow")); err == nil || capture.Len() != 8<<20 {
		t.Fatal("report limit bypassed")
	}
}

func TestTUIViewsSanitizeAndHandleResize(t *testing.T) {
	m := tuiApp{root: "/project\x1b[31m", config: tuiConfig{targets: []string{"./..."}}, width: 80, height: 24}
	for page := 0; page < 4; page++ {
		m.page = page
		view := m.view(false)
		if strings.Contains(view, "\x1b[31m") || strings.Contains(view, "\x1b[0m") || !utf8.ValidString(view) {
			t.Fatal("unsafe or colorized plain view")
		}
	}
	m.width, m.height = 44, 16
	if !strings.Contains(m.view(true), "Resize to at least") {
		t.Fatal("missing compact fallback")
	}
}
