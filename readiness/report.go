// Package readiness answers one question — is this system ready to act, and if
// not, what should the operator do about it — in a form every interface can
// use.
//
// It exists so the CLI, the guided TUI, MCP and Telegram share one definition
// of readiness instead of each growing its own. Nothing here performs an
// action, reads a key, or contacts a signer: it reports state and nothing more.
package readiness

import "strings"

// State is deliberately coarse. An operator needs to know whether they can
// proceed, not a numeric score.
type State string

const (
	// Ready means this check passed and imposes no obstacle.
	Ready State = "ready"
	// Blocked means the system cannot act until the operator does something.
	Blocked State = "blocked"
	// Waiting means nothing is wrong and nothing is required; a condition has
	// simply not been met yet.
	Waiting State = "waiting"
	// Unknown means the check could not be evaluated. It is never treated as
	// ready: unreadable evidence is not good news.
	Unknown State = "unknown"
	// Skipped means the check does not apply to this configuration.
	Skipped State = "skipped"
)

// Check is one thing an operator can understand and, when necessary, fix.
type Check struct {
	Name string `json:"name"`
	// Title is what a person reads, e.g. "Mithril node".
	Title string `json:"title"`
	State State  `json:"state"`
	// Detail is the observed fact, e.g. "7 slots behind".
	Detail string `json:"detail"`
	// Action is what to do about it, and is empty when nothing is required.
	// A blocked check without an action is a dead end for the operator, so
	// Report.Validate rejects that combination.
	Action string `json:"action,omitempty"`
}

// Report is the whole picture. Overall is derived, never set directly, so a
// caller cannot claim readiness the individual checks do not support.
type Report struct {
	Overall State   `json:"overall"`
	Checks  []Check `json:"checks"`
}

// NewReport derives the overall state from the checks it is given.
//
// The precedence is deliberate: anything Blocked blocks, and an Unknown blocks
// too rather than degrading to Waiting. Evidence that could not be read must
// never be reported as a system merely waiting, because an operator reads
// "waiting" as "nothing to do".
func NewReport(checks []Check) Report {
	overall := Ready
	for _, check := range checks {
		switch check.State {
		case Blocked:
			return Report{Overall: Blocked, Checks: checks}
		case Unknown:
			overall = Unknown
		case Waiting:
			if overall == Ready {
				overall = Waiting
			}
		}
	}
	return Report{Overall: overall, Checks: checks}
}

// CanAct reports whether every check permits an action. Waiting is not enough:
// a condition that has not been met is a reason not to act.
func (r Report) CanAct() bool { return r.Overall == Ready }

// Blocking returns only the checks an operator has to deal with, in order, so
// an interface can show the first real problem rather than a wall of green.
func (r Report) Blocking() []Check {
	var blocking []Check
	for _, check := range r.Checks {
		if check.State == Blocked || check.State == Unknown {
			blocking = append(blocking, check)
		}
	}
	return blocking
}

// Validate catches report construction that would mislead an operator. It is
// used by tests and by the constructors in this package; a malformed report is
// a programming error, not a runtime condition.
func (r Report) Validate() error {
	for _, check := range r.Checks {
		if strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Title) == "" {
			return errMissingIdentity
		}
		if check.State == Blocked && strings.TrimSpace(check.Action) == "" {
			return errBlockedWithoutAction
		}
		if check.State == Ready && strings.TrimSpace(check.Action) != "" {
			return errReadyWithAction
		}
	}
	return nil
}
