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

var reasonWording = map[string]string{
	"operation_timeout":         "The action timed out before it could finish.",
	"quote_unavailable":         "A usable market quote is temporarily unavailable.",
	"price_below_floor":         "The market price is below the configured sell limit.",
	"before_schedule_anchor":    "The configured start time has not arrived yet.",
	"blockhash_expired":         "The transaction expired before it could be sent; the next cycle will rebuild it.",
	"signer_refused":            "The signer refused the action under the configured limits.",
	"node_unavailable":          "The Mithril node is not responding.",
	"observation_not_ready":     "The node or independent evidence is not ready for a safe action.",
	"clock_unusable":            "The host clock is not accurate enough for a safe action.",
	"control_state_unavailable": "The local spending-authority state cannot be verified.",
	"operation_failed":          "The action failed unexpectedly; review the local service log.",
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

func describeReason(reason string) string {
	if text, ok := reasonWording[reason]; ok {
		return text
	}
	return reason
}
