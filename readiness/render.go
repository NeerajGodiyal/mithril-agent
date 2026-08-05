package readiness

import (
	"fmt"
	"io"
	"strings"
)

// Render writes the report as something an operator reads top to bottom. The
// same data serves the TUI home screen and the JSON surface, so the wording
// lives here once rather than in each interface.
func (r Report) Render(output io.Writer) error {
	width := 0
	for _, check := range r.Checks {
		if len(check.Title) > width {
			width = len(check.Title)
		}
	}
	for _, check := range r.Checks {
		line := fmt.Sprintf("  %-*s  %-8s %s\n",
			width, check.Title, stateWord(check.State), check.Detail)
		if _, err := io.WriteString(output, line); err != nil {
			return err
		}
	}

	blocking := r.Blocking()
	if len(blocking) == 0 {
		_, err := fmt.Fprintf(output, "\n%s\n", summaryLine(r.Overall))
		return err
	}
	if _, err := fmt.Fprintf(output, "\n%s\n\n", summaryLine(r.Overall)); err != nil {
		return err
	}
	for _, check := range blocking {
		if _, err := fmt.Fprintf(output, "  %s\n    %s\n", check.Title, check.Action); err != nil {
			return err
		}
	}
	return nil
}

// stateWord keeps the column readable without inventing severity words that
// the State vocabulary does not have.
func stateWord(state State) string {
	switch state {
	case Ready:
		return "ready"
	case Blocked:
		return "BLOCKED"
	case Waiting:
		return "waiting"
	case Skipped:
		return "n/a"
	default:
		return "UNKNOWN"
	}
}

func summaryLine(overall State) string {
	switch overall {
	case Ready:
		return "Everything needed to act is in place."
	case Waiting:
		return "Nothing is wrong. A condition has not been met yet, so no action will start."
	case Blocked:
		return "Not ready. Deal with the following, in order:"
	default:
		return "Not ready: some evidence could not be read. Unreadable evidence is never treated as healthy."
	}
}

// Summary is a one-line form for Telegram and other narrow surfaces.
func (r Report) Summary() string {
	blocking := r.Blocking()
	if len(blocking) == 0 {
		return summaryLine(r.Overall)
	}
	titles := make([]string, 0, len(blocking))
	for _, check := range blocking {
		titles = append(titles, check.Title)
	}
	return fmt.Sprintf("Not ready — %s", strings.Join(titles, ", "))
}
