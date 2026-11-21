package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

func commandHelp(fs *flag.FlagSet) {
	syntax, examples := "[flags]", ""
	switch fs.Name() {
	case "tui":
		syntax = "[package...] [flags]"
		examples = "tui\ntui ./... --short\ntui ./internal/cache --budget 200"
	case "hunt":
		syntax = "[package...] [flags]"
		examples = "hunt ./...\nhunt ./... --changed --fail-on-flake\nhunt ./... --format json --progress off > report.json"
	case "repro":
		syntax = "<package> [TestName] [flags]"
		examples = "repro ./internal/cache TestVictim\nrepro --from-json failure.json\nrepro --from-json - < failure.json"
	case "compile":
		syntax = "[capsule] [flags]"
		examples = "compile --dry-run\ncompile --verify\ncompile .go-flake-hunter/gfh_ID.json --pkg ./internal/cache"
	case "replay":
		syntax = "[capsule] [flags]"
		examples = "replay -n 3\nreplay .go-flake-hunter/gfh_ID.json --pkg ./internal/cache"
	case "explain":
		syntax = "[capsule] [flags]"
		examples = "explain\nexplain .go-flake-hunter/gfh_ID.json --format markdown"
	case "init":
		syntax = "[directory] [flags]"
		examples = "init\ninit --replay-capsule .go-flake-hunter/gfh_ID.json"
	case "doctor":
		syntax = "[package...] [flags]"
		examples = "doctor\ndoctor ./...\ndoctor ./internal/cache --format json"
	}
	w := fs.Output()
	for _, c := range commands() {
		if c.Name == fs.Name() {
			_, _ = fmt.Fprintln(w, c.Summary)
		}
	}
	_, _ = fmt.Fprintf(w, "\nUsage:\n  go-flake-hunter %s %s\n", fs.Name(), syntax)
	if examples != "" {
		_, _ = fmt.Fprintln(w, "\nExamples:")
		for _, line := range strings.Split(examples, "\n") {
			_, _ = fmt.Fprintln(w, "  go-flake-hunter "+line)
		}
	}
	for _, group := range []string{"Search", "Budget", "Output", "CI", "Options"} {
		var flags []*flag.Flag
		fs.VisitAll(func(f *flag.Flag) {
			if flagGroup(f.Name) == group {
				flags = append(flags, f)
			}
		})
		if len(flags) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s:\n", group)
		for _, f := range flags {
			value, description := flag.UnquoteUsage(f)
			label := "--" + f.Name
			if len(f.Name) == 1 {
				label = "-" + f.Name
			}
			if value != "" {
				label += " " + value
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "0s" {
				description += " (default " + f.DefValue + ")"
			}
			wrappedHelp(w, label, description)
		}
	}
	_, _ = fmt.Fprint(w, "\nGlobal:\n  --debug                   Include internal error details\n\nUse -- to separate flags from arguments beginning with '-'.\n")
}

func flagGroup(name string) string {
	switch name {
	case "repeat", "seeds", "short", "race", "polluter-pkg", "from-json", "n", "env", "pkg":
		return "Search"
	case "budget", "timeout", "run-timeout", "jobs":
		return "Budget"
	case "format", "sarif", "capsule-dir", "v", "progress", "print", "dry-run":
		return "Output"
	case "changed", "base", "baseline", "write-baseline", "fail-on-flake", "replay-capsule", "verify":
		return "CI"
	default:
		return "Options"
	}
}

func wrappedHelp(w io.Writer, label, description string) {
	line := fmt.Sprintf("  %-26s", label)
	for _, word := range strings.Fields(description) {
		if len(line)+len(word)+1 > 88 && len(line) > 28 {
			_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
			line = strings.Repeat(" ", 28)
		}
		if !strings.HasSuffix(line, " ") {
			line += " "
		}
		line += word
	}
	_, _ = fmt.Fprintln(w, line)
}
