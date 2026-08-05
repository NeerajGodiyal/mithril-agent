package telegramoperator

// operatorWording renders an internal decision as something an operator can act
// on. The raw token stays available in the detail lines; this only replaces the
// headline, so the message reads as a state rather than a variable name.
//
// Presentation only: nothing here changes a decision or a verdict.
var operatorWording = map[string]string{
	"waiting":  "Waiting — conditions not met yet",
	"ready":    "Ready — conditions met",
	"complete": "Completed successfully",
	"stopped":  "Stopped — no action is authorised",
	"canceled": "Canceled",
	"degraded": "Cannot act right now — evidence is unavailable",
	"failed":   "Failed — needs review",
	"halted":   "Halted — needs review",
}

// verdictWording explains what actually happened to a submitted transaction.
var verdictWording = map[string]string{
	"pending":    "Sent; waiting for confirmation",
	"finalized":  "Confirmed on-chain",
	"failed":     "Rejected on-chain",
	"unresolved": "Outcome unknown — do not retry; needs review",
	"diverged":   "Sources disagree on the outcome — needs review",
}

// describeDecision returns operator language, falling back to the raw token so
// an unmapped state is still visible rather than silently blank.
func describeDecision(decision string) string {
	if text, ok := operatorWording[decision]; ok {
		return text
	}
	if decision == "" {
		return "unknown"
	}
	return decision
}

func describeVerdict(verdict string) string {
	if text, ok := verdictWording[verdict]; ok {
		return text
	}
	return verdict
}
