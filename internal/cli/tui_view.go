package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/report"
)

type tuiRow struct {
	text, right, style string
	selected           bool
}

const tuiAccent = "38;2;167;139;250"
const tuiText = "38;2;226;232;240"
const tuiDim = "38;2;148;163;184"
const tuiGreen = "38;2;74;222;128"
const tuiAmber = "38;2;251;191;36"
const tuiRed = "38;2;248;113;113"

func tuiClip(text string, width int) string {
	text = displayText(text)
	if width < 1 {
		return ""
	}
	used := 0
	for i, r := range text {
		size := utf8.RuneLen(r)
		// UI glyphs have one cell. Other Unicode is bounded conservatively by bytes.
		if r >= 0x2500 && r <= 0x28ff || r == '›' || r == '•' || r == '—' || r == '▏' {
			size = 1
		}
		if used+size > width {
			return text[:i]
		}
		used += size
	}
	return text
}

func (m *tuiApp) view(color bool) string {
	w, h := m.width, m.height
	if w < 1 || h < 1 {
		return ""
	}
	if w < 60 || h < 20 {
		message := fmt.Sprintf("Resize to at least 60x20 (now %dx%d). q quits; Ctrl+C stops a task.", w, h)
		return "\x1b[H\x1b[2J" + tuiClip(message, max(1, w-1))
	}
	rows := make([]tuiRow, h)
	put := func(n int, text, style string) {
		if n >= 0 && n < h {
			rows[n] = tuiRow{text: text, style: style}
		}
	}
	put(1, "◈  GO FLAKE HUNTER", tuiAccent)
	rows[1].right = Version
	put(2, tuiProjectName(m.root)+"  /  "+m.root, tuiDim)
	tabs := []string{"1 Overview", "2 Findings", "3 Activity", "4 Settings"}
	for i := range tabs {
		if i == m.page {
			tabs[i] = "[ " + tabs[i] + " ]"
		}
	}
	put(4, strings.Join(tabs, "   "), tuiText)
	put(5, strings.Repeat("─", w-4), tuiDim)
	end := h - 5
	if m.help {
		m.helpRows(rows, end)
	} else if m.editing != "" {
		m.editorRows(rows, end)
	} else if m.confirm != "" {
		title := "Stop the running task and exit?"
		detail := "The engine will save any partial scan evidence before the terminal closes."
		if m.confirm == "compile" {
			title = "Write the previewed regression test?"
			detail = "Creates a build-tagged Go test in the recorded package. User-owned files are protected."
		}
		put(8, title, tuiAmber)
		put(10, detail, tuiText)
		put(12, "y  Confirm       n / Esc  Cancel", tuiAccent)
	} else {
		switch m.page {
		case 0:
			m.overviewRows(rows, end)
		case 1:
			m.findingRows(rows, end)
		case 2:
			m.activityRows(rows, end)
		case 3:
			m.settingsRows(rows, end)
		}
	}
	put(h-4, strings.Repeat("─", w-4), tuiDim)
	status := m.notice
	style := tuiDim
	if m.task != nil {
		spin := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10]
		status = spin + " " + tuiActionName(m.task.kind) + "  •  " + time.Since(m.task.started).Round(time.Second).String() + "  •  Ctrl+C to stop"
		style = tuiAccent
	} else if m.lastAction != "" {
		style = tuiGreen
		if m.lastCode != 0 {
			style = tuiAmber
		}
	}
	put(h-3, status, style)
	hints := "↑↓ / j k navigate   Enter select   Tab next view   ? help"
	if m.page == 1 {
		hints = "↑↓ select   Enter details   / filter   r replay   g preview test"
		if m.detail {
			hints = "↑↓ scroll   ←→ pan   r replay   g preview   Esc back"
		}
	}
	if m.page == 2 {
		hints = "↑↓ scroll   ←→ pan   PgUp/PgDn   End follow   c stop task"
		if m.previewView {
			hints = "↑↓ scroll source   ←→ pan   w write regression   Esc back"
		}
	}
	if m.page == 3 {
		hints = "↑↓ select   Enter edit/toggle   Ctrl+R start scan"
	}
	if m.editing != "" {
		hints = "← → move   Ctrl+U clear left   Enter apply   Esc cancel"
	}
	if m.confirm != "" {
		hints = "y confirm   n / Esc cancel"
	}
	put(h-1, hints, tuiDim)
	rows[h-1].right = "q quit"
	if m.editing != "" || m.confirm != "" {
		rows[h-1].right = "esc cancel"
	}
	if m.help {
		rows[h-1].text = "↑↓ scroll help   Enter / Esc close"
		rows[h-1].right = "q close"
	}
	var frame strings.Builder
	frame.WriteString("\x1b[H")
	for i, row := range rows {
		if color {
			frame.WriteString("\x1b[48;2;15;23;42m")
		}
		frame.WriteString("\x1b[2K")
		style := row.style
		if style == "" {
			style = tuiText
		}
		if row.selected {
			style = "1;" + tuiAccent + ";48;2;37;34;58"
		}
		if color {
			frame.WriteString("\x1b[" + style + "m")
		}
		available := w - 4
		if row.right != "" {
			available -= len(row.right) + 3
		}
		frame.WriteString("  " + tuiClip(row.text, available))
		if row.right != "" {
			_, _ = fmt.Fprintf(&frame, "\x1b[%dG", max(1, w-len(row.right)-1))
			frame.WriteString(tuiClip(row.right, w/2))
		}
		if color {
			frame.WriteString("\x1b[0m")
		}
		if i < h-1 {
			frame.WriteString("\r\n")
		}
	}
	return frame.String()
}

func (m *tuiApp) overviewRows(rows []tuiRow, end int) {
	rows[7] = tuiRow{text: "A reproducible failure. A regression test you can keep.", style: tuiText}
	version := m.goVersion
	if version == "" {
		version = "Go not discovered"
	}
	rows[9] = tuiRow{text: fmt.Sprintf("%s    •    %d test packages    •    %d stored findings", version, len(m.dirs), len(m.findings)), style: tuiDim}
	rows[10] = tuiRow{text: fmt.Sprintf("Target %s  |  %d runs  |  %s  |  %d workers  |  short %t", strings.Join(m.config.targets, ", "), m.config.budget, m.config.timeout, m.config.jobs, m.config.short), style: tuiDim}
	items := []string{"Start a scan", "Browse findings", "Replay a finding", "Preview a regression test", "Check environment", "Scan settings", "Quit"}
	// Scroll the menu on compact terminals; the selected action always stays visible.
	visible := max(1, end-12)
	start := max(0, m.menu-visible+1)
	for i := start; i < len(items) && 12+i-start < end; i++ {
		prefix := "  "
		if i == m.menu {
			prefix = "› "
		}
		rows[12+i-start] = tuiRow{text: prefix + items[i], selected: i == m.menu, style: tuiText}
	}
}

func (m *tuiApp) findingRows(rows []tuiRow, end int) {
	filtered := m.filtered()
	if m.detail {
		f, ok := m.chosen()
		if !ok {
			m.detail = false
			return
		}
		var text strings.Builder
		_ = report.Text(&text, f.capsule)
		lines := append([]string{"Capsule: " + f.path}, strings.Split(text.String(), "\n")...)
		rows[7] = tuiRow{text: f.capsule.Target.Victim, style: tuiAccent, right: string(f.capsule.Status)}
		m.detailScroll = min(m.detailScroll, max(0, len(lines)-(end-9)))
		for i, line := range lines[m.detailScroll:] {
			if 9+i >= end {
				break
			}
			rows[9+i] = tuiRow{text: tuiPan(line, m.horizontal), style: tuiText}
		}
		return
	}
	label := fmt.Sprintf("Stored findings  %d / %d", len(filtered), len(m.findings))
	if m.filter != "" {
		label += "  •  filter: " + m.filter
	}
	rows[7] = tuiRow{text: label, style: tuiAccent}
	if m.intent != "" {
		rows[8] = tuiRow{text: "Choose a finding to " + m.intent + ". Enter continues.", style: tuiDim}
	}
	if len(filtered) == 0 {
		rows[10] = tuiRow{text: "No matching findings. n starts a scan; / changes the filter.", style: tuiDim}
		return
	}
	visible := max(1, (end-10)/2)
	start := max(0, m.selected-visible+1)
	for i := start; i < len(filtered) && i < start+visible; i++ {
		f := m.findings[filtered[i]].capsule
		name := f.Target.Victim
		if name == "" {
			name = f.ID
		}
		prefix := "  "
		if i == m.selected {
			prefix = "› "
		}
		style := tuiText
		switch f.Status {
		case domain.StatusConfirmed:
			style = tuiRed
		case domain.StatusSuspected, domain.StatusPartial:
			style = tuiAmber
		}
		n := 10 + (i-start)*2
		rows[n] = tuiRow{text: prefix + name, right: string(f.Status) + " / " + string(f.Kind), style: style, selected: i == m.selected}
		rows[n+1] = tuiRow{text: "  " + f.Target.Package, style: tuiDim}
	}
}

func (m *tuiApp) activityRows(rows []tuiRow, end int) {
	logs := m.lastLogs
	progress := domain.ScanProgress{}
	if m.task != nil {
		logs, progress = m.task.snapshot()
	}
	title := "Activity"
	if m.previewView {
		title = "Regression source preview"
	}
	rows[7] = tuiRow{text: title, style: tuiAccent}
	start := 9
	if m.task != nil && progress.Total > 0 {
		rows[8] = tuiRow{text: fmt.Sprintf("Packages %d/%d  •  Runs %d/%d  •  %s", progress.Completed, progress.Total, progress.Runs, progress.MaxRuns, progress.Elapsed.Round(time.Second)), style: tuiText}
		keys := make([]string, 0, len(progress.Active))
		for key := range progress.Active {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if start >= end-3 {
				break
			}
			rows[start] = tuiRow{text: "  " + key + "  →  " + progress.Active[key], style: tuiDim}
			start++
		}
		start++
	}
	if m.previewView {
		logs = strings.Split(m.preview, "\n")
	}
	visible := max(1, end-start)
	if m.previewView {
		m.scroll = min(m.scroll, max(0, len(logs)-visible))
		for i := m.scroll; i < len(logs) && i < m.scroll+visible; i++ {
			rows[start+i-m.scroll] = tuiRow{text: tuiPan(logs[i], m.horizontal), style: tuiText}
		}
	} else {
		m.scroll = min(m.scroll, max(0, len(logs)-visible))
		first := max(0, len(logs)-visible-m.scroll)
		for i := first; i < len(logs) && i < first+visible; i++ {
			rows[start+i-first] = tuiRow{text: tuiPan(logs[i], m.horizontal), style: tuiText}
		}
	}
	if len(logs) == 0 {
		rows[start] = tuiRow{text: "Task output appears here. No task output is mixed into the screen.", style: tuiDim}
	}
}

func (m *tuiApp) settingsRows(rows []tuiRow, end int) {
	rows[7] = tuiRow{text: "Scan settings", style: tuiAccent}
	rows[8] = tuiRow{text: "Session only. No configuration file is created.", style: tuiDim}
	labels := []string{"Target path / pattern", "Run budget", "Wall-clock budget", "Parallel packages", "Capsule directory", "Short tests", "Verify with race", "Reload workspace"}
	values := m.settingValues()
	visible := max(1, end-10)
	first := max(0, m.setting-visible+1)
	for i := first; i < len(labels) && i < first+visible; i++ {
		prefix := "  "
		if i == m.setting {
			prefix = "› "
		}
		rows[10+i-first] = tuiRow{text: fmt.Sprintf("%s%-24s %s", prefix, labels[i], values[i]), selected: i == m.setting, style: tuiText}
	}
}

func (m *tuiApp) editorRows(rows []tuiRow, end int) {
	title := "Edit setting"
	hint := "Enter applies this value. Ctrl+U clears to the left of the cursor."
	if m.editing == "filter" {
		title = "Filter findings"
		hint = "Match a test name, package, class or status."
	}
	if m.editing == "env" {
		title = "Replay environment-dependent witness"
		hint = "Value is masked, never saved in settings, and excluded from task logs."
	}
	if m.editing == "setting" {
		title = "Edit " + []string{"target path / pattern", "run budget", "wall-clock budget", "parallel packages", "capsule directory", "short tests", "race verification", "workspace"}[m.setting]
	}
	rows[8] = tuiRow{text: title, style: tuiAccent}
	rows[10] = tuiRow{text: hint, style: tuiDim}
	before, after := m.input[:m.cursor], m.input[m.cursor:]
	if m.editing == "env" {
		before = strings.Repeat("*", utf8.RuneCountInString(before))
		after = strings.Repeat("*", utf8.RuneCountInString(after))
		if f, ok := m.chosen(); ok && len(f.capsule.Witness.EnvDiff) > 0 {
			rows[11] = tuiRow{text: "Key: " + f.capsule.Witness.EnvDiff[0], style: tuiDim}
		}
	}
	// Keep the caret in view for long paths and pasted values.
	for len(before) > m.width-12 {
		_, size := utf8.DecodeRuneInString(before)
		before = before[size:]
	}
	rows[13] = tuiRow{text: "> " + before + "▏" + after, style: tuiText, selected: true}
}

func (m *tuiApp) helpRows(rows []tuiRow, end int) {
	lines := []string{"Keyboard reference", "", "Tab / Shift+Tab    Change view", "1 2 3 4            Jump to a view", "↑ ↓ or j k         Navigate / scroll", "Enter              Open a finding or edit a setting", "n / Ctrl+R         Start a scan using the visible settings", "d                  Check environment (no target tests)", "f                  Browse stored findings", "/                  Filter findings", "r / g              Replay / preview the selected finding", "w                  Write a previewed regression (confirmation)", "Ctrl+C / c         Cancel a running task safely", "q                  Quit; running tasks require confirmation", "Esc                Back / cancel an editor", "", "Clipboard paste is accepted only inside an input field."}
	m.helpScroll = min(m.helpScroll, max(0, len(lines)-(end-7)))
	for i, line := range lines[m.helpScroll:] {
		if 7+i >= end {
			break
		}
		rows[7+i] = tuiRow{text: line, style: tuiText}
	}
}

func tuiActionName(kind string) string {
	switch kind {
	case "workspace":
		return "Loading workspace"
	case "scan":
		return "Scanning packages"
	case "doctor":
		return "Checking environment"
	case "replay":
		return "Replaying witness"
	case "preview":
		return "Preparing source preview"
	case "compile":
		return "Writing regression test"
	}
	return kind
}

func tuiPan(text string, offset int) string {
	if offset < 1 {
		return text
	}
	runes := []rune(displayText(text))
	return "‹ " + string(runes[min(offset, len(runes)):])
}
