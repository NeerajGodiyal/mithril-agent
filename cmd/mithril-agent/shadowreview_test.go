package main

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func completeShadowReport(from time.Time) shadow.Report {
	return shadow.Report{
		Version: shadow.Version, Cluster: shadow.Mainnet,
		From: from, To: from.Add(24 * time.Hour),
		Counts:        shadow.Counts{Ticks: 24, Signals: 2, Fills: 1},
		ExpectedTicks: 24, ObservableBPS: 10_000,
		QuotePegMinimumMicros: 990_000, QuotePegMaximumMicros: 1_010_000,
		VersusHoldMicros: 12_000, MaxDrawdownMicros: 3_000,
	}
}

func TestShadowReviewSummarizesEvidenceWithoutApprovingIt(t *testing.T) {
	policy := validShadowPolicy()
	from := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	first := completeShadowReport(from)
	second := completeShadowReport(from.Add(24 * time.Hour))
	second.Counts.Signals, second.Counts.Fills = 3, 2
	second.VersusHoldMicros, second.MaxDrawdownMicros = -4_000, 5_000

	result, err := summarizeShadowReview(policy, []shadow.Report{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "strategy_evidence_complete_not_approved" ||
		!result.RequiresOperatorDecision || result.ExecutionEnabled {
		t.Fatalf("review crossed the approval boundary: %+v", result)
	}
	if result.CompleteDays != 2 || result.Signals != 5 || result.Fills != 3 ||
		result.VersusHoldMicros != 8_000 || result.MaximumDrawdownMicros != 5_000 {
		t.Fatalf("wrong evidence summary: %+v", result)
	}
	var output bytes.Buffer
	if err := renderShadowReview(&output, result); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"not strategy approval", "Nothing was signed, submitted, or enabled"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("review output omitted %q:\n%s", text, output.String())
		}
	}
}

func TestShadowReviewRefusesIncompleteOrCherryPickedEvidence(t *testing.T) {
	policy := validShadowPolicy()
	from := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	first := completeShadowReport(from)

	tests := []struct {
		name    string
		reports []shadow.Report
	}{
		{name: "gap", reports: []shadow.Report{first, completeShadowReport(from.Add(48 * time.Hour))}},
		{name: "partial", reports: []shadow.Report{first, completeShadowReport(from.Add(25 * time.Hour))}},
		{name: "untrustworthy", reports: func() []shadow.Report {
			bad := completeShadowReport(from)
			bad.ObservableBPS = 9_499
			return []shadow.Report{bad}
		}()},
		{name: "devnet", reports: func() []shadow.Report {
			bad := completeShadowReport(from)
			bad.Cluster = shadow.Devnet
			return []shadow.Report{bad}
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := summarizeShadowReview(policy, test.reports); err == nil {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}

func TestShadowReviewLoadsTheImmediatelyPrecedingCompleteDays(t *testing.T) {
	root := t.TempDir()
	policy := validShadowPolicy()
	policy.TickSeconds = 3_600
	first := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	writeCompleteShadowDay(t, root, policy, first)
	writeCompleteShadowDay(t, root, policy, first.Add(24*time.Hour))

	reports, err := loadShadowReviewReports(
		policy, root, 2, first.Add(60*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || !reports[0].From.Equal(first) ||
		!reports[1].To.Equal(first.Add(48*time.Hour)) {
		t.Fatalf("loaded wrong days: %+v", reports)
	}
	missing := filepath.Join(root, "shadow-2026-08-09.jsonl")
	if _, err := loadShadowReviewReports(policy, root, 3, first.Add(60*time.Hour)); err == nil {
		t.Fatal("review skipped over a missing immediately preceding day")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only review created missing evidence: %v", err)
	}
}

func writeCompleteShadowDay(
	t *testing.T, directory string, policy shadow.Policy, from time.Time,
) {
	t.Helper()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(filepath.Join(directory, "shadow-"+from.Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append(from, shadow.EventOpened, "", shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	const price = uint64(190_000_000)
	ledger, err := shadow.NewLedger(policy, price)
	if err != nil {
		t.Fatal(err)
	}
	equity, err := ledger.EquityMicros(price)
	if err != nil {
		t.Fatal(err)
	}
	for hour := range 24 {
		at := from.Add(time.Duration(hour) * time.Hour)
		if _, err := store.Append(at, shadow.EventWaiting, "", shadow.Tick{
			At: at, Event: shadow.EventWaiting, PriceMicros: price,
			QuoteLowerMicros: policy.QuotePeg.MinimumMicros,
			QuoteUpperMicros: policy.QuotePeg.MaximumMicros,
			EquityMicros:     equity,
		}); err != nil {
			t.Fatal(err)
		}
	}
	end := from.Add(24*time.Hour - time.Nanosecond)
	if _, err := store.Append(end, shadow.EventClosed, "", shadow.Tick{
		At: end, Event: shadow.EventClosed, PeriodClose: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestShadowReviewHelpStatesItsSafetyBoundary(t *testing.T) {
	var output bytes.Buffer
	if err := runShadowReview([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"does not decide", "cannot sign, submit, or enable"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("help omitted %q:\n%s", text, output.String())
		}
	}
	if err := runShadowReview(nil, io.Discard); err == nil {
		t.Fatal("review accepted no explicit period or directory")
	}
}

func TestCanaryShadowEvidenceReplaysAndBindsTheProtectedRoute(t *testing.T) {
	_, signing, _ := testJupiterPolicySet(t)
	policy := validShadowPolicy()
	signing.Jupiter.OutputMint = mainnetUSDCMint
	signing.Jupiter.MaxSlippageBPS = 50
	signing.Jupiter.MaxFeeLamports = policy.FeeLamports
	policy.Observe = signing.Source
	policy.InputAmount = signing.Jupiter.MaxInputAmount
	policy.TickSeconds = 3_600

	directory := t.TempDir()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	writeCompleteShadowDay(t, directory, policy, now.Add(-36*time.Hour).Truncate(24*time.Hour))
	result, err := checkProposalShadowEvidence(
		signing, writeShadowPolicy(t, policy), directory, 1, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.CompleteDays != 1 || result.PolicySHA256 == "" ||
		result.ExecutionEnabled || !result.RequiresOperatorDecision {
		t.Fatalf("canary shadow evidence crossed its boundary: %+v", result)
	}

	for name, mutate := range map[string]func(*shadow.Policy){
		"wallet":   func(p *shadow.Policy) { p.Observe = p.QuoteRoute.InputMint },
		"amount":   func(p *shadow.Policy) { p.InputAmount-- },
		"slippage": func(p *shadow.Policy) { p.SlippageBPS = 49 },
		"fee":      func(p *shadow.Policy) { p.FeeLamports-- },
	} {
		t.Run(name, func(t *testing.T) {
			changed := policy
			mutate(&changed)
			if _, err := checkProposalShadowEvidence(
				signing, writeShadowPolicy(t, changed), directory, 1, now,
			); err == nil {
				t.Fatal("canary accepted shadow evidence for a different protected route")
			}
		})
	}
}

func TestFormatSignedMicrosHandlesMinimumInt64(t *testing.T) {
	if got := formatSignedMicros(math.MinInt64); got != "-9223372036854.775808" {
		t.Fatalf("minimum int64 formatted as %q", got)
	}
}
