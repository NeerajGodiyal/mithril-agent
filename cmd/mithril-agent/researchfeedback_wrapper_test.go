package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestResearchFeedbackReaderProcess(t *testing.T) {
	if os.Getenv("MITHRIL_AGENT_RESEARCH_FEEDBACK_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(os.Args[i+1:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	t.Fatal("missing reader arguments")
}

func TestResearchFeedbackWrapperUsesVerifiedOutcomeProjection(t *testing.T) {
	runner := readDocumentation(t, "../../deploy/hermes-research/run-market-scout.sh")
	block := func(start, end string) string {
		t.Helper()
		from := strings.Index(runner, start)
		if from < 0 {
			t.Fatalf("missing feedback boundary %q", start)
		}
		through := strings.Index(runner[from:], end)
		if through < 0 {
			t.Fatalf("missing feedback boundary %q", end)
		}
		return runner[from : from+through]
	}
	gate := block("case \"$outcome_feedback\" in", "allocation_diagnostic() (")
	collection := block("if [ \"$outcome_feedback\" -eq 1 ]; then", "\nrendered=")
	collection = strings.ReplaceAll(collection, "/usr/sbin/runuser", "test_runuser")
	prompt := block("  if [ -n \"$sol_outcome_history$jup_outcome_history\" ]; then", "  if [ -n \"$sol_replay_history$jup_replay_history\" ]; then")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	jup, err := buildAdaptiveJUPPolicy(250_000_000, 80_000_000, 3_000_000, 100, 100_000,
		"So11111111111111111111111111111111111111112", 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"off", "invalid-flag", "no-artifact", "valid", "stale", "wrong-policy", "no-jup-policy", "corrupt", "corrupt-jup", "orphan-next", "orphan-lock", "orphan-segment", "dangling-symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDirectory(t)
			policies := []shadow.Policy{adaptiveShadowSearchPolicy(), jup}
			policyPaths := []string{writeShadowPolicy(t, policies[0]), writeShadowPolicy(t, policies[1])}
			for i, market := range []string{"sol", "jup"} {
				path := filepath.Join(root, market+".jsonl")
				if mode == "no-artifact" {
					continue
				}
				if i == 0 && (strings.HasPrefix(mode, "orphan-") || mode == "dangling-symlink" || mode == "corrupt") || i == 1 && mode == "corrupt-jup" {
					switch mode {
					case "dangling-symlink":
						err = os.Symlink(filepath.Join(root, "missing"), path)
					case "orphan-next":
						err = os.WriteFile(path+".next", nil, 0600)
					case "orphan-lock":
						err = os.WriteFile(path+".lock", nil, 0600)
					case "orphan-segment":
						err = os.WriteFile(path+".seg-00000001", nil, 0600)
					case "corrupt", "corrupt-jup":
						err = os.WriteFile(path, []byte("invalid journal\n"), 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				receipt := shadowResearchOutcomeReceiptFixture()
				receipt.BasePolicySHA256, err = policies[i].Fingerprint()
				if err != nil {
					t.Fatal(err)
				}
				receipt.Market = shadowMarketPair(policies[i])
				at := time.Now().UTC().Add(-time.Hour)
				if mode == "stale" {
					at = at.Add(-8 * 24 * time.Hour)
				}
				if mode == "wrong-policy" {
					receipt.BasePolicySHA256 = strings.Repeat("e", 64)
				}
				if appended, err := appendShadowResearchOutcome(path, at, shadowResearchForwardEvaluated, receipt); err != nil || !appended {
					t.Fatalf("outcome fixture: appended=%t, error=%v", appended, err)
				}
			}
			if mode == "no-jup-policy" {
				policyPaths[1] = filepath.Join(root, "absent-policy.json")
			}
			script := `set -eu
root=$1
reader=$2
sol_policy=$3
jup_policy=$4
mode=$5
sol_outcome_journal=$root/sol.jsonl
jup_outcome_journal=$root/jup.jsonl
research_query=$root/prompt
unset MITHRIL_HERMES_OUTCOME_FEEDBACK
[ "$mode" = off ] || MITHRIL_HERMES_OUTCOME_FEEDBACK=1
[ "$mode" != invalid-flag ] || MITHRIL_HERMES_OUTCOME_FEEDBACK=invalid
outcome_feedback=${MITHRIL_HERMES_OUTCOME_FEEDBACK:-0}
sol_outcome_history=
jup_outcome_history=
test_runuser() {
  [ "$1" = -u ] && [ "$2" = mithril-agent-research ] && [ "$3" = -- ] &&
    [ "$4" = /usr/local/libexec/mithril-agent/mithril-agent ] || exit 9
  shift 4
  [ "$#" = 11 ] && [ "$1" = shadow ] && [ "$2" = research-outcomes ] &&
    [ "$3" = --journal ] && [ "$5" = --prompt-safe ] && [ "$6" = --limit ] &&
    [ "$7" = 8 ] && [ "$8" = --policy ] && [ "${10}" = --max-age ] && [ "${11}" = 168h ] || exit 9
  printf 'called\n' >> "$root/calls"
  MITHRIL_AGENT_RESEARCH_FEEDBACK_HELPER=1 "$reader" -test.run='^TestResearchFeedbackReaderProcess$' -- "$@"
}
` + gate + collection + "\n" + prompt + "\nprintf published > \"$root/published\"\n"
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/bin/sh", "-c", script, "test", root, binary, policyPaths[0], policyPaths[1], mode)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			fails := mode == "invalid-flag" || strings.HasPrefix(mode, "corrupt") || strings.HasPrefix(mode, "orphan-") || mode == "dangling-symlink"
			if (err != nil) != fails {
				t.Fatalf("wrapper error=%v, expected failure=%t: %s", err, fails, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("reader output escaped prompt capture: %s", stdout.String())
			}
			calls, readErr := os.ReadFile(filepath.Join(root, "calls"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			wantCalls := 2
			switch {
			case mode == "off" || mode == "invalid-flag" || mode == "no-artifact":
				wantCalls = 0
			case fails && mode != "corrupt-jup" || mode == "no-jup-policy":
				wantCalls = 1
			}
			if strings.Count(string(calls), "called\n") != wantCalls {
				t.Fatalf("projection calls=%q, want %d", calls, wantCalls)
			}
			text, readErr := os.ReadFile(filepath.Join(root, "prompt"))
			if fails || wantCalls == 0 {
				if !os.IsNotExist(readErr) {
					t.Fatalf("unavailable feedback created a prompt: %s, %v", text, readErr)
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !strings.Contains(string(text), "internal advisory evidence, not an external source") ||
					!strings.Contains(string(text), "cannot authorize, activate, select, promote, or execute anything") {
					t.Fatal("feedback limitations missing")
				}
				for _, forbidden := range []string{root, "sha256", "hypothesis_id", "evaluated_at", "complete_days", "advantage_micros", "bounded-signal-20260902"} {
					if strings.Contains(string(text), forbidden) {
						t.Fatalf("prompt leaked %q", forbidden)
					}
				}
				seen := 0
				for _, line := range strings.Split(string(text), "\n") {
					label, raw, ok := strings.Cut(line, ": ")
					if !ok || label != "SOL/USDC" && label != "JUP/USDC" {
						continue
					}
					seen++
					var summary shadowResearchOutcomePromptSummary
					if err := json.Unmarshal([]byte(raw), &summary); err != nil {
						t.Fatal(err)
					}
					wantHints := 1
					if mode == "stale" || mode == "wrong-policy" {
						wantHints = 0
					}
					if !summary.PaperOnly || !summary.AdvisoryOnly || summary.Authorized ||
						summary.Status != "research_outcome_learning_hints" || len(summary.Hints) != wantHints {
						t.Fatalf("prompt projection=%+v", summary)
					}
					if wantHints == 1 && (summary.Hints[0].Market != label || summary.Hints[0].State != "accepted" ||
						len(summary.Hints[0].ParameterChanges) != 1 || summary.Hints[0].ParameterChanges[0].Proposed != 250) {
						t.Fatalf("market hint=%+v", summary.Hints)
					}
				}
				if seen != wantCalls {
					t.Fatalf("market prompt lines=%d, want %d", seen, wantCalls)
				}
			}
			_, publishedErr := os.Stat(filepath.Join(root, "published"))
			if fails && !os.IsNotExist(publishedErr) || !fails && publishedErr != nil {
				t.Fatalf("prompt publication error=%v, expected stopped=%t", publishedErr, fails)
			}
		})
	}
}
