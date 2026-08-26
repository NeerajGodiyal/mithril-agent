package main

import "strings"

// stageMeaning translates a readiness-gate stage token into a sentence a
// non-specialist can act on. The gate deliberately emits stable machine tokens;
// this is presentation only and must never change a verdict.
//
// Every entry says what was refused and what to do, because "failed" without a
// next step is what makes an operator retry blindly.
var stageMeaning = map[string]string{
	"arguments":     "The command was called with arguments it does not accept.",
	"configuration": "The agent configuration could not be read or did not validate.",
	"dependencies":  "A required local component is missing or not reachable.",
	"clock": "The host clock is not provably accurate. Signing needs trusted time, " +
		"so this stops before anything else. Check the time-sync service.",
	"genesis": "The Mithril node did not answer, or answered for the wrong cluster. " +
		"Confirm the node is running and serving Devnet.",
	"mithril_observation_catalog": "The Mithril MCP tool list is missing or incompatible. " +
		"Use the Mithril binary this setup was built and tested with.",
	"mithril_observation_info":       "The MCP server identity or its Mithril RPC binding did not match this setup.",
	"mithril_observation_genesis":    "The MCP server could not prove that its node is on Devnet.",
	"mithril_observation_state_call": "The MCP state check did not complete. Confirm the local MCP process can start and finish.",
	"mithril_observation_state_tool": "The configured Mithril state file is missing or unreadable. " +
		"A restarted node may still be rebuilding it; otherwise check the exact state path and its read permission.",
	"mithril_observation_state_identity": "The Mithril state file was read but did not prove a ready Devnet node. " +
		"Wait for bootstrap to finish; investigate any schema, cluster, shutdown, clock, or corruption warning.",
	"mithril_observation_diagnosis": "The node's health evidence was incomplete or inconsistent, so the agent stopped.",
	"mithril_observation_account":   "The node could not return a valid reading for the agent wallet.",

	"observation_freshness": "The node's account reading was too old to rely on. " +
		"This usually means the node is not keeping up.",
	"wallet_balance": "The wallet holds less than the configured reserve plus fees, " +
		"so no swap may start. Fund the dedicated Devnet agent wallet.",

	"mithril_cross_check_contract": "The node's slot comparison did not come back in " +
		"the expected form, so its lag cannot be trusted.",
	"mithril_cross_check_policy": "The configured slot-lag limit is missing or wider " +
		"than the safety threshold allows.",
	"mithril_cross_check_behind": "The node is further behind the network than the " +
		"configured limit. Wait for it to catch up; do not lower the limit.",
	"mithril_cross_check_ahead": "The node reports a slot ahead of the reference, " +
		"which should not happen and needs investigation.",

	"mithril_health_scope":           "The node's health report described a different scope than expected.",
	"mithril_health_automation_flag": "The node's health report claimed it was safe for automation. It must not; that flag is refused deliberately.",
	"mithril_health_evidence":        "The node's health evidence was incomplete, so readiness cannot be concluded.",
	"mithril_health_freshness":       "The node's health evidence was too old to rely on.",
	"mithril_divergence":             "The node reported bank-hash divergence artifacts. Stop and re-bootstrap the node; do not trade.",

	"quote":          "The Orca quote adapter did not return a usable quote.",
	"quote_policy":   "The quote was worse than the configured policy allows, so it was refused.",
	"price_evidence": "The two price sources were missing, stale, or disagreed by more than the allowed margin.",
	"simulation":     "The node's simulation of the exact transaction did not succeed.",
	"route":          "The swap route did not match the reviewed fixed route.",
	"policy":         "The prepared action did not satisfy the configured policy limits.",
}

// healthIssueMeaning explains the specific node check that made health unusable.
var healthIssueMeaning = map[string]string{
	"divergence_artifacts": "the node could not prove there was no divergence — " +
		"often the evidence directory is unreadable by the agent's service user rather than a real divergence",
	"state":                   "the node's persisted state file could not be read",
	"state_evidence":          "the node's persisted safety evidence could not be evaluated",
	"metrics":                 "the node's metrics endpoint was unreachable",
	"rpc":                     "the node's RPC did not answer",
	"replay":                  "the node's replay timing evidence was unusable",
	"logs":                    "the node's log directory could not be scanned",
	"shutdown":                "the node's last shutdown looks unclean",
	"runtime_state_agreement": "the node's live and persisted views disagree",
	"verification":            "the node's verification state could not be confirmed",
	"turbine_receiver":        "the node's block receiver looks unhealthy",
	"host":                    "the node host itself reported a problem, such as low disk",
	"cross_check":             "the node's slot comparison could not be completed",
	"block_source":            "the node's configured block source is not active",
}

// explainStage renders a gate stage in plain language. Unknown stages return a
// truthful fallback rather than a guess, since the gate may add stages later.
func explainStage(stage string) string {
	if stage == "" {
		return ""
	}
	if text, ok := stageMeaning[stage]; ok {
		return text
	}
	// Health stages carry the failing node check as a suffix.
	const healthPrefix = "mithril_health_status_"
	if rest, ok := strings.CutPrefix(stage, healthPrefix); ok {
		state := rest
		issue := ""
		for _, known := range []string{"unknown", "degraded", "critical"} {
			if trimmed, found := strings.CutPrefix(rest, known+"_"); found {
				state, issue = known, trimmed
				break
			} else if rest == known {
				state = known
				break
			}
		}
		text := "The node reported its health as " + state + "."
		if detail, ok := healthIssueMeaning[issue]; ok {
			text += " Specifically, " + detail + "."
		}
		return text + " The gate refuses to act on health it cannot confirm."
	}
	return "The readiness gate stopped at this step. See the operator guide; " +
		"do not retry blindly or relax the limit to get past it."
}
