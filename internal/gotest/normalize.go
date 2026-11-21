package gotest

import (
	"regexp"
	"strings"
)

// Patterns that differ between two runs of the same failing test. Left in place
// they make every failure look unique and break "same defect" grouping.
var normalizers = []*regexp.Regexp{
	regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)?`),   // 2026/11/02 01:04:33
	regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z?`), // RFC3339
	regexp.MustCompile(`/var/folders/[^\s:]+`),                          // macOS tmp
	regexp.MustCompile(`/tmp/[^\s:]+`),                                  // linux tmp
	regexp.MustCompile(`/private/var/folders/[^\s:]+`),
	regexp.MustCompile(`0x[0-9a-fA-F]{6,}`),           // pointers
	regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|ms|s)\b`), // durations
	regexp.MustCompile(`127\.0\.0\.1:\d+`),            // ephemeral ports
	regexp.MustCompile(`\[::1\]:\d+`),
}

// Replacements keep the text readable while collapsing the varying part.
var replacements = []string{
	"<ts>", "<ts>", "<tmp>", "<tmp>", "<tmp>", "<ptr>", "<dur>", "<addr>", "<addr>",
}

// Normalize collapses run-to-run noise so two failures of the same defect have
// the same normalized text.
func Normalize(s string) string {
	if s == "" {
		return ""
	}
	out := s
	for i, re := range normalizers {
		// ReplaceAllString copies the input even when nothing matches.
		matches := re.FindAllStringIndex(out, -1)
		if len(matches) == 0 {
			continue
		}
		var b strings.Builder
		b.Grow(len(out))
		last := 0
		for _, match := range matches {
			b.WriteString(out[last:match[0]])
			b.WriteString(replacements[i])
			last = match[1]
		}
		b.WriteString(out[last:])
		out = b.String()
	}
	return out
}
