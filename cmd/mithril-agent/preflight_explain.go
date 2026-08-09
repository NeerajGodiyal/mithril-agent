package main

import (
	"fmt"
	"io"
	"sort"
)

// preflightMeaning explains each offline check in terms an operator can act on.
// Presentation only: it never changes a verdict, and every entry names the fix
// rather than just the fault.
var preflightMeaning = map[string]string{
	"config":                   "The agent configuration file could not be read, parsed, or validated.",
	"profile":                  "The configured swap profile is not the reviewed Devnet profile.",
	"policy_binding":           "The signer policy file does not match the configuration it must be bound to.",
	"keypair_binding":          "The signer keypair does not match the policy that authorises it.",
	"risk_policy_binding":      "The risk-authority policy file does not match its configuration.",
	"risk_keypair_binding":     "The risk-authority keypair does not match its policy.",
	"submitter_policy_binding": "The submitter policy file does not match its configuration.",
	"submitter_key_binding":    "The submitter key does not match its policy.",
	"commands":                 "One of the helper executables is missing, not executable, or not owned as required.",
	"control_path":             "The durable control-state file is missing or not privately owned.",
	"journal_path":             "The audit journal is missing or not privately owned.",
	"path_separation":          "Two components share a path that must stay separate, so one could overwrite the other's state.",
	"providers":                "The configured read providers are missing, malformed, or not two independent sources.",
	"mcp_inputs":               "The service user cannot read the Mithril node state it needs. Use the dedicated node-state reader group described in the operator guide; grant it to the state file, not to the accounts database, logs, wallets, or keys.",
	"price_source":             "The configured price sources are incomplete or unusable.",
	"clock":                    "The host clock is not provably accurate. Signing needs trusted time. Check the time-synchronisation service; this check requires Linux and cannot pass on macOS.",
}

// preflightOrder keeps the human report in the order the checks are evaluated,
// so the first failure a reader sees is the earliest one.
var preflightOrder = []string{
	"config", "profile", "policy_binding", "keypair_binding",
	"risk_policy_binding", "risk_keypair_binding",
	"submitter_policy_binding", "submitter_key_binding",
	"commands", "control_path", "journal_path", "path_separation",
	"providers", "mcp_inputs", "price_source", "clock",
}

// explainPreflight writes a plain-language report of the failing checks. It
// prints nothing when everything passed: silence on success keeps the failing
// case prominent.
func explainPreflight(output io.Writer, results map[string]string) error {
	// Only genuine failures are explained. A skipped check was never evaluated
	// — usually because an earlier one failed — so describing it as wrong
	// would send the reader after a fault that does not exist.
	failing := make([]string, 0, len(results))
	seen := map[string]bool{}
	skipped := 0
	for _, name := range preflightOrder {
		seen[name] = true
		switch results[name] {
		case preflightFailed:
			failing = append(failing, name)
		case preflightSkipped:
			skipped++
		}
	}
	extra := make([]string, 0)
	for name, status := range results {
		if seen[name] {
			continue
		}
		switch status {
		case preflightFailed:
			extra = append(extra, name)
		case preflightSkipped:
			skipped++
		}
	}
	sort.Strings(extra)
	failing = append(failing, extra...)

	if len(failing) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(output, "\n%d check(s) did not pass:\n\n", len(failing)); err != nil {
		return err
	}
	for _, name := range failing {
		meaning, ok := preflightMeaning[name]
		if !ok {
			meaning = "This check did not pass. See the operator guide; do not skip it."
		}
		if _, err := fmt.Fprintf(output, "  %s (%s)\n    %s\n\n", name, results[name], meaning); err != nil {
			return err
		}
	}
	if skipped > 0 {
		if _, err := fmt.Fprintf(output,
			"%d further check(s) were skipped because an earlier one failed; they may be fine.\n",
			skipped); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(output, "Fix these in order; each one gates the next stage.\n")
	return err
}

// preflightResultMap flattens the summary so the explainer does not need to
// know the struct layout.
func preflightResultMap(c preflightChecks) map[string]string {
	return map[string]string{
		"config":                   c.Config,
		"profile":                  c.Profile,
		"policy_binding":           c.PolicyBinding,
		"keypair_binding":          c.KeypairBinding,
		"risk_policy_binding":      c.RiskPolicy,
		"risk_keypair_binding":     c.RiskKeypair,
		"submitter_policy_binding": c.SubmitterPolicy,
		"submitter_key_binding":    c.SubmitterKey,
		"commands":                 c.Commands,
		"control_path":             c.ControlPath,
		"journal_path":             c.JournalPath,
		"path_separation":          c.PathSeparation,
		"providers":                c.Providers,
		"mcp_inputs":               c.MCPInputs,
		"price_source":             c.PriceSource,
		"clock":                    c.Clock,
	}
}
