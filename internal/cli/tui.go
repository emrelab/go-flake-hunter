package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
	nativeTerm "github.com/emrelab/go-flake-hunter/internal/terminal"
)

type tuiApp struct {
	config                                        tuiConfig
	root, goVersion, notice                       string
	dirs                                          []string
	findings                                      []tuiFinding
	page                                          int
	menu, selected, setting, scroll, detailScroll int
	filter                                        string
	detail                                        bool
	intent                                        string
	editing                                       string
	input                                         string
	cursor                                        int
	confirm                                       string
	help                                          bool
	helpScroll                                    int
	preview                                       string
	previewFinding                                tuiFinding
	previewView                                   bool
	task                                          *tuiTask
	pending                                       *tuiRequest
	lastLogs                                      []string
	lastCode                                      int
	lastAction                                    string
	quitAfter, interruptArmed                     bool
	width, height                                 int
	frame                                         int
	execute                                       func(context.Context, []string, IO) int
	horizontal                                    int
}

func interactiveTUI(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && os.Getenv("CI") == "" && os.Getenv("TERM") != "dumb" && nativeTerm.IsTerminal(os.Stdin) && nativeTerm.IsTerminal(f)
}

func tuiCmd(ctx context.Context, args []string, out IO) (resultErr error) {
	c := tuiConfig{budget: policy.DefaultMaxRuns, timeout: policy.DefaultMaxWall, jobs: min(runtime.NumCPU(), 4)}
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(out.Stderr)
	fs.IntVar(&c.budget, "budget", c.budget, "total go test run budget")
	fs.DurationVar(&c.timeout, "timeout", c.timeout, "scan wall-clock budget, checked between runs")
	fs.IntVar(&c.jobs, "jobs", c.jobs, "maximum packages scanned concurrently")
	fs.StringVar(&c.store, "capsule-dir", "", "capsule directory (default: each scanned package's store)")
	fs.BoolVar(&c.short, "short", false, "scan with -short")
	fs.BoolVar(&c.race, "race", false, "verify found witnesses with -race")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if c.budget < 1 || c.timeout <= 0 || c.jobs < 1 {
		return domain.Wrap(domain.ErrUsage, "budget, timeout and jobs must be positive")
	}
	c.targets = append([]string(nil), fs.Args()...)
	if len(c.targets) == 0 {
		c.targets = []string{"./..."}
	}
	f, ok := out.Stdout.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" || !nativeTerm.IsTerminal(os.Stdin) || !nativeTerm.IsTerminal(f) {
		return domain.Wrap(domain.ErrUsage, "TUI requires an ANSI terminal with interactive stdin/stdout; use hunt or --help in CI and pipes")
	}
	session, err := nativeTerm.Open(os.Stdin, f)
	if err != nil {
		return domain.Wrap(domain.ErrUsage, "could not enter terminal mode: %v", err)
	}
	defer func() {
		_, writeErr := io.WriteString(f, "\x1b[?2004l\x1b[0m\x1b[?25h\x1b[?1049l")
		restoreErr := session.Close()
		if restoreErr != nil {
			_, _ = fmt.Fprintf(out.Stderr, "\nTerminal mode restoration failed: %v. Close and reopen this terminal.\n", restoreErr)
		}
		resultErr = errors.Join(resultErr, writeErr, restoreErr)
	}()
	if _, err := io.WriteString(f, "\x1b[?1049h\x1b[?25l\x1b[?2004h"); err != nil {
		return err
	}
	inputCtx, cancelInput := context.WithCancel(ctx)
	keys := make(chan tuiKey, 32)
	inputErrors := make(chan error, 1)
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		var keyboard tuiKeyboard
		for {
			data, err := session.Read(inputCtx)
			if err != nil {
				select {
				case inputErrors <- err:
				default:
				}
				return
			}
			for _, key := range keyboard.feed(data, len(data) == 0) {
				select {
				case keys <- key:
				case <-inputCtx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancelInput(); <-inputDone }()
	root, _ := os.Getwd()
	m := tuiApp{config: c, root: root, notice: "Discovering the workspace. No tests run until you start a scan.", pending: &tuiRequest{kind: "workspace"}}
	defer func() {
		if m.task != nil {
			m.task.cancel()
			<-m.task.result
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastFrame := ""
	for {
		if m.pending != nil && m.task == nil {
			m.launch(ctx, *m.pending)
			m.pending = nil
		}
		w, h, err := session.Size()
		if err != nil || w < 1 || h < 1 {
			w, h = 80, 24
		}
		// shortcut: bound the viewport to 240x100; virtualize if larger consoles need full coverage.
		m.width, m.height = min(w, 240), min(h, 100)
		frame := m.view(os.Getenv("NO_COLOR") == "")
		if frame != lastFrame {
			if _, err := io.WriteString(f, frame); err != nil {
				return err
			}
			lastFrame = frame
		}
		var done <-chan tuiResult
		if m.task != nil {
			done = m.task.result
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-inputErrors:
			return err
		case r := <-done:
			logs, _ := m.task.snapshot()
			m.lastLogs = logs
			m.task.cancel()
			m.task = nil
			m.apply(r)
			// The exit was already requested and the task has now cleaned up.
			if m.quitAfter || m.confirm == "quit" {
				return nil
			}
		case key := <-keys:
			if m.key(key) {
				return nil
			}
		case <-ticker.C:
			if m.task != nil {
				m.frame++
			}
		}
	}
}

func (m *tuiApp) launch(ctx context.Context, request tuiRequest) {
	jobCtx, cancel := context.WithCancel(ctx)
	t := &tuiTask{kind: request.kind, started: time.Now(), cancel: cancel, result: make(chan tuiResult, 1), seen: map[string]bool{}}
	m.task = t
	config := m.config
	config.targets = append([]string(nil), config.targets...)
	dirs := append([]string(nil), m.dirs...)
	execute := m.execute
	if execute == nil {
		execute = Execute
	}
	go func() {
		defer func() {
			if recover() != nil {
				_, _ = fmt.Fprintln(t, "Task failed unexpectedly. No panic payload is retained in the TUI log.")
				t.result <- tuiResult{kind: request.kind, code: domain.ExitInternal, notice: "Task failed unexpectedly. The terminal is still usable; please report the failure."}
			}
		}()
		t.result <- tuiRunTask(jobCtx, request, config, dirs, t, execute)
	}()
}

func (m *tuiApp) apply(r tuiResult) {
	m.lastAction, m.lastCode, m.notice = r.kind, r.code, r.notice
	if r.kind == "workspace" {
		m.dirs = r.dirs
		m.goVersion = r.goVersion
	} else if len(r.dirs) > 0 {
		m.dirs = r.dirs
	}
	if r.goVersion != "" {
		m.goVersion = r.goVersion
	}
	if r.catalogLoaded {
		m.findings = r.findings
	}
	if r.preview != "" {
		m.preview, m.previewFinding, m.previewView = r.preview, r.previewFinding, true
		m.page = 2
		m.scroll = 0
		m.horizontal = 0
	}
	m.selected = min(m.selected, max(0, len(m.filtered())-1))
	if r.kind == "scan" {
		m.page = 2
		m.previewView = false
		m.scroll = 0
	}
}

func (m *tuiApp) filtered() []int {
	result := make([]int, 0, len(m.findings))
	query := strings.ToLower(m.filter)
	for i, f := range m.findings {
		c := f.capsule
		if query == "" || strings.Contains(strings.ToLower(c.Target.Package+" "+c.Target.Victim+" "+string(c.Status)+" "+string(c.Kind)), query) {
			result = append(result, i)
		}
	}
	return result
}

func (m *tuiApp) chosen() (tuiFinding, bool) {
	filtered := m.filtered()
	if m.selected < 0 || m.selected >= len(filtered) {
		return tuiFinding{}, false
	}
	return m.findings[filtered[m.selected]], true
}

func (m *tuiApp) request(kind string, f tuiFinding) {
	if m.task != nil {
		m.notice = "A task is already running. Ctrl+C stops it safely."
		return
	}
	m.pending = &tuiRequest{kind: kind, finding: f}
	if kind == "compile" {
		m.pending.preview = m.preview
	}
	m.page = 2
	m.previewView = false
	m.scroll = 0
}

func (m *tuiApp) findingAction(kind string) {
	f, ok := m.chosen()
	if !ok {
		m.notice = "No finding selected. Start a scan or reload the catalog."
		return
	}
	c := f.capsule
	if kind == "replay" {
		if c.Status != domain.StatusConfirmed && c.Status != domain.StatusSuspected {
			m.notice = "Partial capsules have no verified witness to replay."
			return
		}
		if c.Kind == domain.KindEnv {
			if m.task != nil {
				m.notice = "Finish the current task first."
				return
			}
			m.editing = "env"
			m.input = ""
			m.cursor = 0
			return
		}
	} else if c.Status != domain.StatusConfirmed || c.Witness.CrossBinary || c.Witness.CrossClause {
		m.notice = "Regression generation needs a confirmed, same-clause, same-process witness. Replay remains available."
		return
	}
	m.request(kind, f)
}

func (m *tuiApp) key(k tuiKey) bool {
	if k.name == "paste-too-long" {
		m.notice = "Paste exceeds 4096 bytes; the field was not changed."
		return false
	}
	if k.name == "paste" && m.editing == "" {
		m.notice = "Open a setting or search field before pasting. Clipboard text never runs commands."
		return false
	}
	if k.name == "interrupt" {
		if m.task != nil {
			m.task.cancel()
			m.notice = "Stopping the task and preserving any scan evidence..."
			return false
		}
		if m.editing != "" {
			m.editing, m.input = "", ""
			return false
		}
		if m.interruptArmed {
			return true
		}
		m.interruptArmed = true
		m.notice = "Press Ctrl+C again to exit, or use q."
		return false
	}
	m.interruptArmed = false
	if m.help {
		if k.name == "down" || k.text == "j" {
			m.helpScroll++
		}
		if k.name == "up" || k.text == "k" {
			m.helpScroll = max(0, m.helpScroll-1)
		}
		if k.name == "escape" || k.name == "enter" || k.text == "?" || k.text == "q" {
			m.help = false
		}
		return false
	}
	if m.confirm != "" {
		if strings.EqualFold(k.text, "y") {
			if m.confirm == "quit" {
				// The task can finish while this prompt is open.
				if m.task == nil {
					return true
				}
				m.quitAfter = true
				m.task.cancel()
				m.notice = "Stopping before exit..."
			} else {
				m.request("compile", m.previewFinding)
			}
			m.confirm = ""
		} else if k.name == "escape" || strings.EqualFold(k.text, "n") {
			m.confirm = ""
		}
		return false
	}
	if m.editing != "" {
		m.editKey(k)
		return false
	}
	if k.text == "q" {
		if m.task == nil {
			return true
		}
		if m.task.kind == "workspace" {
			m.quitAfter = true
			m.task.cancel()
		} else {
			m.confirm = "quit"
		}
		return false
	}
	if k.text == "?" {
		m.help = true
		m.helpScroll = 0
		return false
	}
	if k.name == "tab" || k.name == "backtab" {
		delta := 1
		if k.name == "backtab" {
			delta = 3
		}
		m.page = (m.page + delta) % 4
		m.detail = false
		m.intent = ""
		return false
	}
	if strings.Contains("1234", k.text) && len(k.text) == 1 {
		m.page = int(k.text[0] - '1')
		m.detail = false
		m.intent = ""
		return false
	}
	if k.name == "escape" {
		if m.detail {
			m.detail = false
		} else {
			m.page = 0
		}
		m.intent = ""
		return false
	}
	if k.text == "n" || k.name == "run" {
		m.request("scan", tuiFinding{})
		return false
	}
	if k.text == "d" {
		m.request("doctor", tuiFinding{})
		return false
	}
	if k.text == "f" {
		m.page = 1
		m.detail = false
		m.intent = ""
		return false
	}
	if k.text == "s" {
		m.page = 3
		return false
	}
	if k.text == "a" {
		m.page = 2
		return false
	}
	if k.text == "/" && m.page == 1 {
		m.editing = "filter"
		m.input = m.filter
		m.cursor = len(m.input)
		return false
	}
	if k.text == "r" && m.page == 1 {
		m.findingAction("replay")
		return false
	}
	if k.text == "g" && m.page == 1 {
		m.findingAction("preview")
		return false
	}
	if k.text == "w" && m.page == 2 && m.previewView && m.preview != "" && m.task == nil {
		m.confirm = "compile"
		return false
	}
	if k.text == "c" && m.task != nil {
		m.task.cancel()
		m.notice = "Stopping safely..."
		return false
	}
	if (m.page == 1 && m.detail) || m.page == 2 {
		if k.name == "left" {
			m.horizontal = max(0, m.horizontal-8)
		}
		if k.name == "right" {
			m.horizontal += 8
		}
	}
	delta := 0
	if k.name == "up" || k.text == "k" {
		delta = -1
	}
	if k.name == "down" || k.text == "j" {
		delta = 1
	}
	if k.name == "pageup" {
		delta = -max(1, m.height-12)
	}
	if k.name == "pagedown" {
		delta = max(1, m.height-12)
	}
	switch m.page {
	case 0:
		m.menu = ((m.menu+delta)%7 + 7) % 7
		if k.name == "enter" {
			switch m.menu {
			case 0:
				m.request("scan", tuiFinding{})
			case 1:
				m.page = 1
				m.intent = ""
			case 2:
				m.page = 1
				m.intent = "replay"
			case 3:
				m.page = 1
				m.intent = "preview"
			case 4:
				m.request("doctor", tuiFinding{})
			case 5:
				m.page = 3
			case 6:
				return m.key(tuiKey{"text", "q"})
			}
		}
	case 1:
		if m.detail {
			m.detailScroll = max(0, m.detailScroll+delta)
		} else {
			m.selected = max(0, min(m.selected+delta, len(m.filtered())-1))
		}
		if k.name == "enter" {
			if m.intent != "" {
				m.findingAction(m.intent)
			} else if _, ok := m.chosen(); ok {
				m.detail = true
				m.detailScroll = 0
				m.horizontal = 0
			}
		}
	case 2:
		if m.previewView {
			m.scroll = max(0, m.scroll+delta)
		} else {
			m.scroll = max(0, m.scroll-delta)
		}
		if k.name == "end" {
			if m.previewView {
				m.scroll = len(strings.Split(m.preview, "\n"))
			} else {
				m.scroll = 0
			}
		}
		if k.name == "home" {
			if m.previewView {
				m.scroll = 0
			} else {
				m.scroll = 201
			}
		}
	case 3:
		m.setting = max(0, min(m.setting+delta, 7))
		if k.name == "enter" {
			m.beginSetting()
		}
	}
	return false
}

func (m *tuiApp) beginSetting() {
	if m.task != nil {
		m.notice = "Finish or stop the current task before changing settings."
		return
	}
	if m.setting == 5 {
		m.config.short = !m.config.short
		return
	}
	if m.setting == 6 {
		m.config.race = !m.config.race
		return
	}
	if m.setting == 7 {
		m.pending = &tuiRequest{kind: "workspace"}
		return
	}
	m.editing = "setting"
	m.input = m.settingValues()[m.setting]
	if m.setting == 0 {
		m.input = m.config.targets[0]
	}
	m.cursor = len(m.input)
}

func (m *tuiApp) editKey(k tuiKey) {
	if k.name == "escape" {
		m.editing, m.input = "", ""
		return
	}
	if k.name == "left" {
		_, size := utf8.DecodeLastRuneInString(m.input[:m.cursor])
		m.cursor -= size
		return
	}
	if k.name == "right" {
		_, size := utf8.DecodeRuneInString(m.input[m.cursor:])
		m.cursor += size
		return
	}
	if k.name == "home" {
		m.cursor = 0
		return
	}
	if k.name == "end" {
		m.cursor = len(m.input)
		return
	}
	if k.name == "clearleft" {
		m.input = m.input[m.cursor:]
		m.cursor = 0
		return
	}
	if k.name == "clearright" {
		m.input = m.input[:m.cursor]
		return
	}
	if k.name == "backspace" {
		_, size := utf8.DecodeLastRuneInString(m.input[:m.cursor])
		if size > 0 {
			m.input = m.input[:m.cursor-size] + m.input[m.cursor:]
			m.cursor -= size
		}
		return
	}
	if k.name == "delete" {
		_, size := utf8.DecodeRuneInString(m.input[m.cursor:])
		if size > 0 {
			m.input = m.input[:m.cursor] + m.input[m.cursor+size:]
		}
		return
	}
	if k.name == "text" || k.name == "paste" {
		text := k.text
		if k.name == "paste" {
			text = tuiSingleLine(text)
		}
		if strings.IndexFunc(text, unicode.IsControl) >= 0 {
			m.notice = "Use a single-line value without control characters."
			return
		}
		if len(m.input)+len(text) <= 4096 {
			m.input = m.input[:m.cursor] + text + m.input[m.cursor:]
			m.cursor += len(text)
		}
		return
	}
	if k.name != "enter" {
		return
	}
	switch m.editing {
	case "filter":
		m.filter = m.input
		m.selected = 0
		m.detail = false
	case "env":
		f, ok := m.chosen()
		if !ok {
			return
		}
		if m.task != nil {
			m.notice = "Finish the current task first."
			return
		}
		m.pending = &tuiRequest{kind: "replay", finding: f, secret: m.input}
		m.page = 2
		m.previewView = false
		m.scroll = 0
	case "setting":
		if err := m.setSetting(m.input); err != nil {
			m.notice = err.Error()
			return
		}
	}
	m.editing, m.input = "", ""
}

func (m *tuiApp) settingValues() []string {
	store := m.config.store
	if store == "" {
		store = "(per-package default)"
	}
	return []string{strings.Join(m.config.targets, ", "), strconv.Itoa(m.config.budget), m.config.timeout.String(), strconv.Itoa(m.config.jobs), store, strconv.FormatBool(m.config.short), strconv.FormatBool(m.config.race), "Reload workspace and stored findings"}
}

func (m *tuiApp) setSetting(value string) error {
	value = strings.TrimSpace(value)
	switch m.setting {
	case 0:
		if value == "" {
			return fmt.Errorf("target path or pattern cannot be empty")
		}
		m.config.targets = []string{value}
		m.pending = &tuiRequest{kind: "workspace"}
	case 1, 3:
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("enter a positive whole number")
		}
		if m.setting == 1 {
			m.config.budget = n
		} else {
			m.config.jobs = n
		}
	case 2:
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("enter a positive duration, for example 5m")
		}
		m.config.timeout = d
	case 4:
		if value == "(per-package default)" {
			value = ""
		}
		m.config.store = value
		m.pending = &tuiRequest{kind: "workspace"}
	}
	return nil
}

func tuiProjectName(root string) string {
	name := filepath.Base(root)
	if name == "." || name == string(filepath.Separator) {
		return root
	}
	return name
}
