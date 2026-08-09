package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/readiness"
)

// doctor knew how to check a single-leg setup and nothing about a strategy,
// which is where the two failures that actually cost time today lived:
//
//   - a leg armed for more trades than its caps fund. The signer refused every
//     trade after the first, the refusal was flattened to a category, and the
//     visible symptom was an expired blockhash a minute later.
//   - legs armed with no runner executing for them. The runner only ever lived
//     in a tmux session, so every session kill silently left authority granted
//     with no process to use it and no supervisor to notice.
//
// Both are observable from local files alone — no network, no key, no chain —
// which is what makes them worth a readiness check rather than a runbook line.
const (
	// A leg writes its operator status every cycle. Runners poll on the order of
	// ten seconds, so silence for four minutes means the process is gone, wedged
	// or starved, not merely between cycles.
	strategyStatusStaleAfter = 4 * time.Minute
	strategyCheckName        = "strategy"
	strategyCheckTitle       = "Strategy legs"
)

// doctorStrategyCheck reports whether a configured strategy could actually
// trade right now. It is deliberately silent when no strategy is configured:
// most deployments are single-leg, and a permanent "not configured" line would
// train operators to ignore the section.
func doctorStrategyCheck(now func() time.Time) readiness.Check {
	paths, unreadable := discoverStrategy()
	if paths.empty() && len(unreadable) == 0 {
		return readiness.Check{
			Name: strategyCheckName, Title: strategyCheckTitle,
			State: readiness.Skipped, Detail: "no strategy configured",
		}
	}
	// A leg the pointer names but nothing can read may still be armed, and its
	// runner holds the profile in memory regardless of what happened to the
	// file. That is a finding, not a display detail.
	if len(unreadable) != 0 {
		return readiness.Check{
			Name: strategyCheckName, Title: strategyCheckTitle,
			State:  readiness.Blocked,
			Detail: fmt.Sprintf("%d recorded leg(s) cannot be read", len(unreadable)),
			Action: "run: mithril-agent strategy stop --reason TEXT, then set the strategy up again",
		}
	}

	var (
		armed      int
		notRunning []string
		starved    []string
	)
	for _, leg := range paths.configured() {
		cfg, err := readConfig(leg.path)
		if err != nil {
			return readiness.Check{
				Name: strategyCheckName, Title: strategyCheckTitle,
				State:  readiness.Blocked,
				Detail: fmt.Sprintf("the %s leg's config cannot be read", leg.leg),
				Action: "run: mithril-agent strategy show, and repair the leg it names",
			}
		}
		reporting := statusIsFresh(cfg.Journal.Path, now())
		_, grant, live := controlGrantAt(cfg.Control.StatePath)
		if grant == "" || !live {
			if !reporting {
				notRunning = append(notRunning, leg.leg)
			}
			continue
		}
		armed++
		// Armed and funded for fewer trades than the grant permits is the exact
		// mismatch that made an unattended strategy fail silently for a whole
		// UTC day. Arming refuses it now, but a profile re-created underneath a
		// live grant can still reach this state.
		if cfg.Swap != nil && cfg.Swap.FundedTradesPerDay() == 0 {
			starved = append(starved, leg.leg)
		}
		if !reporting {
			notRunning = append(notRunning, leg.leg)
		}
	}

	switch {
	case len(starved) != 0:
		return readiness.Check{
			Name: strategyCheckName, Title: strategyCheckTitle,
			State:  readiness.Blocked,
			Detail: fmt.Sprintf("%v leg(s) are armed but their daily caps fund no trades", starved),
			Action: "run: mithril-agent setup strategy again with --trades-per-day N",
		}
	case len(notRunning) != 0:
		detail := fmt.Sprintf("%v leg(s) have no recent runner status", notRunning)
		if armed != 0 {
			detail = fmt.Sprintf(
				"%v leg(s) are armed but no runner has reported in %s",
				notRunning, strategyStatusStaleAfter)
		}
		return readiness.Check{
			Name: strategyCheckName, Title: strategyCheckTitle,
			State:  readiness.Blocked,
			Detail: detail,
			Action: "run: mithril-agent service install",
		}
	case armed == 0:
		// Waiting would be the comfortable answer and it is the wrong one: the
		// contract reserves Waiting for "nothing is required", and a leg that is
		// merely configured never arms itself. Reporting that as waiting told an
		// operator whose agent had simply never been granted authority that there
		// was nothing to do — indefinitely.
		command, err := suggestedStrategyEnableCommand(paths)
		if err != nil {
			return readiness.Check{
				Name: strategyCheckName, Title: strategyCheckTitle,
				State: readiness.Blocked, Detail: err.Error(),
				Action: "run: mithril-agent setup strategy again",
			}
		}
		return readiness.Check{
			Name: strategyCheckName, Title: strategyCheckTitle,
			State:  readiness.Blocked,
			Detail: fmt.Sprintf("%d leg(s) configured, none armed", len(paths.configured())),
			Action: "grant it authority to spend: mithril-agent " + command,
		}
	}
	return readiness.Check{
		Name: strategyCheckName, Title: strategyCheckTitle,
		State:  readiness.Ready,
		Detail: fmt.Sprintf("%d leg(s) armed, runner reporting", armed),
	}
}

// statusIsStale reports whether the operator status beside a leg's journal has
// stopped being written. It is the cheapest true signal that a runner is alive:
// the file is rewritten every cycle, so its age is the runner's heartbeat, and
// unlike a process check it works when the runner is on another host.
//
// A missing file is NOT stale. A leg that has never run has nothing to report,
// and calling that a fault would fire on every fresh setup.
func statusIsStale(journalPath string, now time.Time) bool {
	if journalPath == "" {
		return false
	}
	info, err := os.Stat(journalPath + ".status.json")
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) > strategyStatusStaleAfter
}

func statusIsFresh(journalPath string, now time.Time) bool {
	if journalPath == "" {
		return false
	}
	info, err := os.Stat(journalPath + ".status.json")
	return err == nil && now.Sub(info.ModTime()) <= strategyStatusStaleAfter
}
