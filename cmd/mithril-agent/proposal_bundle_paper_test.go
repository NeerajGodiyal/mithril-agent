package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestProposalBundlePaperReviewStaysUnsigned(t *testing.T) {
	authority, signing, submission := testJupiterPolicySet(t)
	p := validShadowPolicy()
	route := *signing.Jupiter
	route.OutputMint = p.QuoteRoute.OutputMint
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	signing.Jupiter, signing.ProfileFingerprint = &route, fingerprint
	authority.TransactionPolicy = signing
	submission.Jupiter, submission.ProfileFingerprint = &route, fingerprint
	p.Observe, p.FeeLamports = route.Owner, route.MaxFeeLamports
	c := mainnetCandidateForPolicy(t, route)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policyDir := filepath.Join(dir, "policies")
	if err := os.Mkdir(policyDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(policyDir, proposalAuthorityPolicyName), authority)
	writeJSON(t, filepath.Join(policyDir, proposalSignerPolicyName), signing)
	writeJSON(t, filepath.Join(policyDir, proposalSubmitterPolicyName), submission)
	candidatePath, policyPath := filepath.Join(dir, "candidate.json"), filepath.Join(dir, "paper.json")
	writeJSON(t, candidatePath, c)
	writeJSON(t, policyPath, p)
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tick := shadow.Tick{At: at, Event: shadow.EventSignal, Triggered: true, PriceMicros: p.Trigger.ThresholdMicros,
		QuoteLowerMicros: p.QuotePeg.MinimumMicros, QuoteUpperMicros: p.QuotePeg.MaximumMicros,
		EquityMicros:  p.Trigger.ThresholdMicros,
		DecisionQuote: &shadow.Quote{InputAmount: c.Request.InputAmount, EstimatedOutput: c.Quote.EstimatedOutput, MinimumOutput: c.Quote.MinimumOutput, ReceivedAt: at}}
	pin, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := proposalcheck.PaperEvidenceSHA256([]shadow.Tick{tick})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "shadow-2026-09-05.jsonl")
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, shadow.EventOpened, "", shadow.Opening{Version: shadow.JournalVersionFor(p), PolicySHA256: pin}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(at, shadow.EventSignal, "", tick); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	boundsPath := filepath.Join(dir, "bounds.json")
	bounds := proposalcheck.PaperIntentBounds{PolicySHA256: pin, EvidenceSHA256: evidence, MaxInputAmount: p.InputAmount, NativeBudgetLamports: 20_000_000, ReserveLamports: 1_000_000}
	writeJSON(t, boundsPath, bounds)
	args := []string{"--candidate", candidatePath, "--policy-dir", policyDir, "--paper-policy", policyPath, "--paper-journal", journalPath, "--paper-bounds", boundsPath}
	var output bytes.Buffer
	if err := runProposalBundleCheck(args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status     string                     `json:"status"`
		Intent     *proposalcheck.PaperIntent `json:"unsigned_paper_intent"`
		Signing    bool                       `json:"signing_enabled"`
		Submission bool                       `json:"submission_enabled"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "bundle_consistent_not_authorized" || result.Intent == nil || result.Intent.EvidenceSHA256 != evidence || result.Signing || result.Submission {
		t.Fatalf("invalid review receipt: %s", output.String())
	}
	if !strings.Contains(output.String(), `"input_amount":"1000000"`) {
		t.Fatal("base units were not serialized as an exact decimal string")
	}
	t.Run("minimum output alone", func(t *testing.T) {
		changed := tick
		quote := *tick.DecisionQuote
		quote.MinimumOutput++
		changed.DecisionQuote = &quote
		if quote.EstimatedOutput != c.Quote.EstimatedOutput || quote.MinimumOutput > quote.EstimatedOutput {
			t.Fatal("fixture must isolate minimum output without changing the estimate")
		}
		if _, err := shadow.Replay(p, []shadow.Tick{changed}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := proposalcheck.ValidateCandidateMaterial(route, c); err != nil {
			t.Fatal(err)
		}
		frozen := bounds
		frozen.EvidenceSHA256, err = proposalcheck.PaperEvidenceSHA256([]shadow.Tick{changed})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proposalcheck.CheckPaperIntent(p, []shadow.Tick{changed}, route, c, frozen); err == nil {
			t.Fatal("candidate weakening only the minimum output was accepted")
		}
	})
	for _, name := range []string{"policy pin", "evidence pin", "duplicate", "fraction", "overflow", "numeric"} {
		t.Run(name, func(t *testing.T) {
			changed := bounds
			switch name {
			case "policy pin":
				changed.PolicySHA256 = strings.Repeat("0", 64)
			case "evidence pin":
				changed.EvidenceSHA256 = strings.Repeat("0", 64)
			}
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "duplicate":
				raw = append(raw[:len(raw)-1], []byte(`,"max_input_amount":"1"}`)...)
			case "fraction":
				raw = bytes.Replace(raw, []byte(`"1000000"`), []byte(`"1.5"`), 1)
			case "overflow":
				raw = bytes.Replace(raw, []byte(`"1000000"`), []byte(`"18446744073709551616"`), 1)
			case "numeric":
				raw = bytes.Replace(raw, []byte(`"1000000"`), []byte(`1000000`), 1)
			}
			if err := os.WriteFile(boundsPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := runProposalBundleCheck(args, io.Discard); err == nil {
				t.Fatal("invalid frozen bounds accepted")
			}
		})
	}
}

func TestPaperBundleReplaysAdaptiveFirstSignal(t *testing.T) {
	_, signing, _ := testJupiterPolicySet(t)
	p := adaptiveShadowSearchPolicy()
	p.FeeLamports, p.SlippageBPS, p.Observe = signing.Jupiter.MaxFeeLamports, signing.Jupiter.MaxSlippageBPS, signing.Source
	p.Adaptive.MinimumSignalBPS = 1_000
	route := *signing.Jupiter
	route.OutputMint, route.MinOutputAmount = p.QuoteRoute.OutputMint, 72_900
	candidate := mainnetCandidateForPolicy(t, route)
	dir := privateTestDirectory(t)
	primary := &shadowSearchReader{identity: p.Trigger.PrimarySourceSHA256}
	secondary := &shadowSearchReader{identity: p.Trigger.SecondarySourceSHA256}
	pegPrimary := &shadowSearchReader{identity: p.QuotePeg.PrimarySourceSHA256, price: 1_000_000}
	pegSecondary := &shadowSearchReader{identity: p.QuotePeg.SecondarySourceSHA256, price: 1_000_000}
	log, err := newDailyJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	runner, err := shadow.NewRunner(p, primary, secondary, shadowSearchQuoter(func(sell bool, amount uint64) shadow.Quote {
		if !sell || amount != candidate.Request.InputAmount {
			t.Fatal("adaptive signal changed direction or size")
		}
		return shadow.Quote{InputAmount: amount, EstimatedOutput: candidate.Quote.EstimatedOutput, MinimumOutput: candidate.Quote.MinimumOutput}
	}), log, pegPrimary, pegSecondary)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for i, price := range []uint64{100_000_000, 90_000_000, 81_000_000, 72_900_000} {
		at := start.Add(time.Duration(i) * p.Tick())
		primary.price, primary.at, secondary.price, secondary.at = price, at, price, at
		pegPrimary.at, pegSecondary.at = at, at
		tick, err := runner.Step(t.Context(), at)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && tick.Event != shadow.EventWaiting {
			t.Fatalf("expected adaptive warmup, got %+v", tick)
		}
		if i == 3 && (tick.Event != shadow.EventSignal || tick.Decision == nil || tick.Decision.Strategy != shadow.StrategyMomentum) {
			t.Fatalf("expected adaptive momentum signal, got %+v", tick)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "shadow-2026-09-05.jsonl")
	ticks, err := readShadowTicks(journalPath, p)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := proposalcheck.PaperEvidenceSHA256(ticks)
	if err != nil {
		t.Fatal(err)
	}
	policyPath, boundsPath := filepath.Join(dir, "policy.json"), filepath.Join(dir, "bounds.json")
	writeJSON(t, policyPath, p)
	writeJSON(t, boundsPath, proposalcheck.PaperIntentBounds{PolicySHA256: pin, EvidenceSHA256: digest, MaxInputAmount: p.InputAmount, NativeBudgetLamports: 20_000_000, ReserveLamports: 1_000_000})
	if receipt, err := checkPaperBundle(policyPath, journalPath, boundsPath, route, candidate); err != nil || receipt == nil {
		t.Fatalf("adaptive unsigned handoff: %+v, %v", receipt, err)
	}
}

func TestProposalBundlePaperFlagsAreAllOrNone(t *testing.T) {
	for mask := 1; mask < 7; mask++ {
		args := []string{"--candidate", "/missing-candidate", "--policy-dir", "/missing-policy"}
		for i, flag := range []string{"--paper-policy", "--paper-journal", "--paper-bounds"} {
			if mask&(1<<i) != 0 {
				args = append(args, flag, "/paper-"+flag[2:])
			}
		}
		if err := runProposalBundleCheck(args, io.Discard); err == nil || !strings.Contains(err.Error(), "together") {
			t.Fatalf("partial flags did not fail before IO: %v", err)
		}
	}
}
