package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchObservationsUsage = `Usage: mithril-agent research observations --policy PATH --journal-dir PATH [--explain-unavailable]

Emits host-verified paper observations for the immediately preceding complete UTC
day. Requires the exact current Mainnet SOL/USDC or JUP/USDC policy, paired market
source evidence, and at least 95% observable coverage. This read-only artifact is
advisory: it cannot authorize, promote, or execute a trade.
With --explain-unavailable, verified low coverage emits a separate diagnostic and
still exits unsuccessfully. That diagnostic is not a recorded-basis artifact.`

type researchCoverageError struct {
	Kind                  string `json:"kind"`
	Reason                string `json:"reason"`
	Market                string `json:"market"`
	Day                   string `json:"day"`
	ObservableBPS         int32  `json:"observable_bps"`
	RequiredObservableBPS int32  `json:"required_observable_bps"`
}

func (*researchCoverageError) Error() string {
	return "recorded observations lack 95% observable coverage"
}

func runResearchObservations(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research observations", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "exact current paper policy")
	journalDir := flags.String("journal-dir", "", "private daily paper journal directory")
	explainUnavailable := flags.Bool("explain-unavailable", false, "emit a nonqualifying diagnostic for verified low coverage")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchObservationsUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*policyPath) || !cleanResearchPath(*journalDir) {
		return errors.New("research observations requires clean absolute --policy and --journal-dir paths")
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	artifact, err := buildResearchObservations(policy, *journalDir, now())
	if err != nil {
		var coverage *researchCoverageError
		if *explainUnavailable && errors.As(err, &coverage) {
			return errors.Join(err, json.NewEncoder(output).Encode(coverage))
		}
		return err
	}
	return json.NewEncoder(output).Encode(artifact)
}

func buildResearchObservations(policy shadow.Policy, journalDir string, now time.Time) (researchpacket.RecordedObservations, error) {
	day, err := readResearchDay(policy, journalDir, now)
	if err != nil {
		return researchpacket.RecordedObservations{}, err
	}
	report, provenance := day.report, day.provenance
	if !report.Trustworthy() {
		if report.ObservableBPS < 9500 {
			return researchpacket.RecordedObservations{}, &researchCoverageError{
				Kind: "recorded_paper_observations_unavailable", Reason: "coverage_below_threshold",
				Market: shadowMarketPair(policy), Day: provenance.Day,
				ObservableBPS: report.ObservableBPS, RequiredObservableBPS: 9500,
			}
		}
		return researchpacket.RecordedObservations{}, errors.New("recorded observations lack 95% observable coverage")
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return researchpacket.RecordedObservations{}, err
	}
	artifact := researchpacket.RecordedObservations{
		Version: 1, Kind: "recorded_paper_observations", PaperOnly: true, AdvisoryOnly: true,
		Market: shadowMarketPair(policy), PolicySHA256: fingerprint,
		ObservedFrom: report.From, ObservedThrough: report.To,
		Journal: researchpacket.RecordedJournal{
			Day: provenance.Day, Records: provenance.Records, ChainHeadSHA256: provenance.ChainHeadSHA256,
		},
		Metrics: researchpacket.ObservationMetrics{
			ObservableBPS: uint32(report.ObservableBPS), Signals: report.Counts.Signals, Fills: report.Counts.Fills,
			VersusHoldMicros: report.VersusHoldMicros, MaxDrawdownMicros: report.MaxDrawdownMicros,
		},
	}
	return artifact.Seal()
}

type researchDay struct {
	ticks      []shadow.Tick
	provenance shadowJournalProvenance
	report     shadow.Report
}

// readResearchDay verifies observations before either consumer can expose any
// measurements. Coverage qualification remains the recorded-artifact gate.
func readResearchDay(policy shadow.Policy, journalDir string, now time.Time) (researchDay, error) {
	if now.IsZero() || policy.Validate() != nil || validateActiveShadowPolicy(policy) != nil ||
		policy.Cluster != shadow.Mainnet ||
		(shadowMarketPair(policy) != "SOL/USDC" && shadowMarketPair(policy) != "JUP/USDC") ||
		!cleanResearchPath(journalDir) || validatePrivateDirectory(journalDir) != nil {
		return researchDay{}, errors.New("recorded observations require a current Mainnet paper policy and private journal directory")
	}
	dayStart := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	day := dayStart.Format("2006-01-02")
	ticks, provenance, err := readShadowSearchJournal(filepath.Join(journalDir, "shadow-"+day+".jsonl"), day, policy)
	if err != nil {
		return researchDay{}, err
	}
	replayed, err := shadow.Replay(policy, ticks)
	if err != nil {
		return researchDay{}, err
	}
	report, err := shadow.BuildReport(policy, replayed.Ledger, replayed.Counts, replayed.Stats,
		replayed.ClosingPrice, dayStart, replayed.PeriodEnd)
	if err != nil {
		return researchDay{}, err
	}
	report.ObservableBPS = shadowWalkForwardObservableBPS(ticks, dayStart, policy.Tick())
	return researchDay{ticks: ticks, provenance: provenance, report: report}, nil
}

func verifyResearchObservations(artifact researchpacket.RecordedObservations, policy shadow.Policy, journalDir string, now time.Time) error {
	if artifact.Validate() != nil || !artifact.CurrentAt(now) {
		return errors.New("recorded observations are invalid or outside the current observation window")
	}
	expected, err := buildResearchObservations(policy, journalDir, now)
	if err != nil {
		return err
	}
	if artifact.ContentSHA256 != expected.ContentSHA256 {
		return errors.New("recorded observations do not match the current policy and verified journal")
	}
	return nil
}
