package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// Markdown renders the capsule as GitHub-flavoured markdown.
//
// Reason to exist: capsules must read well in CI logs and PR comments. Plain
// text wraps lines out of shape and JSON is unreadable; the table is the only
// format between them — it renders on GitHub and looks honest in a terminal.
func Markdown(w io.Writer, c domain.Capsule) error {
	if c.Status == domain.StatusPartial {
		_, err := fmt.Fprintf(w, "### inconclusive: %s (partial)\n\n| field | value |\n|---|---|\n| package | %s |\n| status | %s |\n| capsule | `%s` |\n| note | %s |\n",
			escapePipe(c.Target.Victim), escapePipe(c.Target.Package), c.Status, c.ID, escapePipe(c.Note))
		return err
	}
	var b strings.Builder
	v := c.Witness

	title := c.Target.Victim
	if c.Status != domain.StatusConfirmed {
		title = fmt.Sprintf("%s (%s)", title, c.Status)
	}
	label := "flake"
	if c.Status == domain.StatusSuspected {
		label = "suspected flake"
	}
	fmt.Fprintf(&b, "### %s: %s\n\n", label, title)

	b.WriteString("| field | value |\n|---|---|\n")
	row := func(k, val string) {
		if val == "" {
			return
		}
		fmt.Fprintf(&b, "| %s | %s |\n", k, escapePipe(val))
	}
	row("package", c.Target.Package)
	row("class", string(c.Kind))
	if len(v.Polluters) > 0 {
		names := make([]string, len(v.Polluters))
		for i, p := range v.Polluters {
			names[i] = p.Name
		}
		row("polluter", strings.Join(names, ", "))
	}
	if len(v.EnvDiff) > 0 {
		row("environment key", strings.Join(v.EnvDiff, ", "))
	}
	if c.Observation.Runs > 0 {
		row("run", fmt.Sprintf("%d/%d failed", c.Observation.Failures, c.Observation.Runs))
	}
	if ci := c.Observation.Wilson95; ci != nil {
		row("Wilson 95%", fmt.Sprintf("%.1f%%–%.1f%%", ci.Lower*100, ci.Upper*100))
	}
	row("external dependencies (static)", strings.Join(c.Evidence.ExternalDependencies, "; "))
	row("evidence", sharedGlobalLine(c))
	if len(c.Evidence.FilesTouched) > 0 {
		row("disk writes", strings.Join(c.Evidence.FilesTouched, "; "))
	}
	row("status", string(c.Status))
	row("capsule", "`"+c.ID+"`")
	if c.Note != "" {
		row("note", c.Note)
	}

	if c.Repro.Command != "" {
		fmt.Fprintf(&b, "\nReproduce:\n\n```sh\n%s\n```\n", c.Repro.Command)
	}
	// The skip condition rendered next to the command it gates (#8).
	if c.Kind == domain.KindEnv && len(c.Witness.EnvDiff) > 0 {
		fmt.Fprintf(&b, "\n*Inert unless* `%s` is set — the capsule stores the key, never the value.\n",
			strings.Join(c.Witness.EnvDiff, "`, `"))
	}
	if c.VerifyStatus != "" {
		fmt.Fprintf(&b, "\nVerification: `%s`.\n", c.VerifyStatus)
	}
	if c.Repro.GeneratedTest != "" {
		fmt.Fprintf(&b, "\nPermanent regression test: `%s` (build tag `%s`).\n",
			c.Repro.GeneratedTest, domain.PersistentBuildTag)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// escapePipe keeps a value from breaking the table it lives in.
func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
