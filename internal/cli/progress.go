package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

type scanProgress struct {
	mu                     sync.Mutex
	w                      io.Writer
	budget                 *policy.Budget
	started                time.Time
	active                 map[string]string
	total, finished, width int
	live                   bool
	scope                  string
	stop, done             chan struct{}
	once                   sync.Once
	lineOpen               bool
	observe                func(domain.ScanProgress)
	silent                 bool
}

func validateProgress(mode string) error {
	switch mode {
	case "", "auto", "plain", "off":
		return nil
	default:
		return domain.Wrap(domain.ErrUsage, "unknown progress mode %q (auto|plain|off)", mode)
	}
}

func terminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("CI") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (s *scanner) startProgress(total int, scope string) func() {
	mode := s.o.progress
	silent := mode == "off" || total == 0 || (mode == "" || mode == "auto") && s.o.format != "" && s.o.format != "text"
	if silent && s.out.Progress == nil {
		return func() {}
	}
	p := &scanProgress{w: s.out.Stderr, budget: s.budget, started: s.started, total: total, active: map[string]string{}, live: mode != "plain" && terminal(s.out.Stderr)}
	p.silent, p.observe = silent, s.out.Progress
	p.live = p.live && !silent
	p.scope = scope
	if p.started.IsZero() {
		p.started = time.Now()
	}
	s.progress = p
	s.out.Stderr = p
	s.out.Stdout = progressOutput{p, s.out.Stdout}
	if p.live || p.observe != nil {
		p.stop, p.done = make(chan struct{}), make(chan struct{})
		go func() {
			defer close(p.done)
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					p.mu.Lock()
					if p.live {
						p.render()
					}
					p.notify("", "", false)
					p.mu.Unlock()
				case <-p.stop:
					return
				}
			}
		}()
	}
	return func() {
		p.once.Do(func() {
			if p.done != nil {
				close(p.stop)
				<-p.done
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			p.clear()
			p.live = false
		})
	}
}

func (p *scanProgress) update(label, phase string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.active[label]; !exists && !p.live && !p.silent {
		_, _ = fmt.Fprintf(p.w, "[scan] %s\n", displayText(label))
	}
	p.active[label] = phase
	p.notify(label, phase, false)
	if p.live {
		p.render()
	}
}

func (p *scanProgress) finish(label string, err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, label)
	p.finished++
	p.notify(label, "finished", true)
	if p.silent {
		return
	}
	if p.live {
		p.render()
		return
	}
	status := "done"
	if err != nil {
		status = "incomplete"
	}
	_, _ = fmt.Fprintf(p.w, "[%s %d/%d] %s (%d/%d runs, %s)\n", status, p.finished, p.total, displayText(label), p.budget.Runs(), p.budget.MaxRuns, time.Since(p.started).Round(time.Second))
}

func (p *scanProgress) notify(label, phase string, finished bool) {
	if p.observe == nil {
		return
	}
	active := make(map[string]string, len(p.active))
	for key, value := range p.active {
		active[key] = value
	}
	p.observe(domain.ScanProgress{Package: label, Phase: phase, Finished: finished, Completed: p.finished, Total: p.total, Runs: p.budget.Runs(), MaxRuns: p.budget.MaxRuns, Elapsed: time.Since(p.started), Active: active})
}

func (p *scanProgress) clear() {
	if p.width > 0 {
		_, _ = fmt.Fprint(p.w, "\r"+strings.Repeat(" ", p.width)+"\r")
		p.width = 0
	}
}

// Write clears the live line around diagnostics so worker messages stay readable.
func (p *scanProgress) Write(data []byte) (int, error) {
	return p.write(p.w, data)
}

type progressOutput struct {
	p *scanProgress
	w io.Writer
}

func (o progressOutput) Write(data []byte) (int, error) { return o.p.write(o.w, data) }

func (p *scanProgress) write(w io.Writer, data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	n, err := w.Write(data)
	if n > 0 {
		p.lineOpen = data[n-1] != '\n'
	}
	if p.live {
		p.render()
	}
	return n, err
}

func (p *scanProgress) render() {
	if p.lineOpen {
		return
	}
	line := fmt.Sprintf("%s %d/%d | Runs %d/%d | %s", p.scope, p.finished, p.total, p.budget.Runs(), p.budget.MaxRuns, time.Since(p.started).Round(time.Second))
	keys := make([]string, 0, len(p.active))
	for key := range p.active {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		line += " | " + displayText(p.active[keys[0]]) + " | " + displayText(keys[0])
		if len(keys) > 1 {
			line += fmt.Sprintf(" (+%d active)", len(keys)-1)
		}
	}
	if len(line) > 76 {
		for len(line) > 73 {
			_, size := utf8.DecodeLastRuneInString(line)
			line = line[:len(line)-size]
		}
		line += "..."
	}
	p.clear()
	_, _ = fmt.Fprint(p.w, "\r"+line)
	p.width = len(line)
}

func displayText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}
