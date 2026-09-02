package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const shadowReviewUsage = `Usage: mithril-agent shadow review --policy PATH --dir PATH --days N [--json]

Recomputes the immediately preceding N complete UTC days from their hash-chained
journals. Every day must use the same Mainnet policy, be complete, and have at
least 95% observable coverage. This checks evidence quality; it does not decide
whether the strategy is profitable and cannot sign, submit, or enable trading.

  --policy PATH  the Mainnet shadow policy the run used
  --dir PATH     directory containing daily shadow journals
  --days N       consecutive complete UTC days the operator requires (1..3650)
  --json         emit the summary as JSON`

type shadowReviewResult struct {
	Version                  uint32    `json:"version"`
	Status                   string    `json:"status"`
	EvaluationMode           string    `json:"evaluation_mode"`
	PolicySHA256             string    `json:"policy_sha256"`
	From                     time.Time `json:"from"`
	To                       time.Time `json:"to"`
	CompleteDays             uint32    `json:"complete_days"`
	MinimumObservableBPS     int32     `json:"minimum_observable_bps"`
	Signals                  uint64    `json:"signals"`
	Fills                    uint64    `json:"fills"`
	Filtered                 uint64    `json:"filtered"`
	Refused                  uint64    `json:"refused"`
	Missed                   uint64    `json:"missed"`
	Unobservable             uint64    `json:"unobservable"`
	VersusHoldMicros         int64     `json:"versus_hold_micros"`
	MaximumDrawdownMicros    uint64    `json:"maximum_drawdown_micros"`
	RequiresOperatorDecision bool      `json:"requires_operator_decision"`
	ExecutionEnabled         bool      `json:"execution_enabled"`
}

func runShadowReview(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow review", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "Mainnet shadow policy JSON")
	directory := flags.String("dir", "", "journal directory")
	days := flags.Uint("days", 0, "required consecutive complete UTC days")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowReviewUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *days == 0 || *days > 3650 || *directory == "" ||
		!filepath.IsAbs(*directory) || filepath.Clean(*directory) != *directory {
		return errors.New("shadow review requires an absolute clean --dir and explicit --days from 1 to 3650")
	}
	policy, err := loadShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	if policy.Cluster != shadow.Mainnet {
		return errors.New("only Mainnet shadow evidence can support a strategy review")
	}
	reports, err := loadShadowReviewReports(policy, *directory, uint32(*days), time.Now().UTC())
	if err != nil {
		return err
	}
	result, err := summarizeShadowReview(policy, reports)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	return renderShadowReview(output, result)
}

// checkProposalShadowEvidence replays the journals rather than trusting a saved
// summary, then binds their wallet, route, size, slippage, and fee assumptions
// to the protected canary policy. It proves evidence completeness, not edge.
var checkProposalShadowEvidence = func(
	signing signer.Policy,
	policyPath, directory string,
	days uint32,
	now time.Time,
) (shadowReviewResult, error) {
	policy, err := loadShadowPolicy(policyPath)
	if err != nil {
		return shadowReviewResult{}, errors.New("read Mainnet canary shadow policy")
	}
	if policy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional {
		return shadowReviewResult{}, errors.New("development paper evidence cannot support a Mainnet proposal")
	}
	if signing.Jupiter == nil || policy.Cluster != shadow.Mainnet ||
		policy.QuoteRoute.Provider != shadow.QuoteJupiter ||
		policy.Observe != signing.Source ||
		policy.QuoteRoute.InputMint != signing.Jupiter.InputMint ||
		policy.QuoteRoute.OutputMint != signing.Jupiter.OutputMint ||
		policy.InputAmount != signing.Jupiter.MaxInputAmount ||
		policy.SlippageBPS < signing.Jupiter.MaxSlippageBPS ||
		policy.FeeLamports < signing.Jupiter.MaxFeeLamports {
		return shadowReviewResult{}, errors.New("shadow evidence does not conservatively match the protected Mainnet route")
	}
	reports, err := loadShadowReviewReports(policy, directory, days, now)
	if err != nil {
		return shadowReviewResult{}, err
	}
	return summarizeShadowReview(policy, reports)
}

func loadShadowReviewReports(
	policy shadow.Policy,
	directory string,
	days uint32,
	now time.Time,
) ([]shadow.Report, error) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	reports := make([]shadow.Report, 0, days)
	for remaining := days; remaining > 0; remaining-- {
		from := today.AddDate(0, 0, -int(remaining))
		day := from.Format("2006-01-02")
		ticks, err := readShadowTicks(filepath.Join(directory, "shadow-"+day+".jsonl"), policy)
		if err != nil {
			return nil, fmt.Errorf("required shadow evidence for %s is unavailable: %w", day, err)
		}
		replayed, err := shadow.Replay(policy, ticks)
		if err != nil {
			return nil, fmt.Errorf("required shadow evidence for %s is invalid: %w", day, err)
		}
		to, err := shadowReportEnd(from, replayed.PeriodEnd)
		if err != nil || !to.Equal(from.Add(24*time.Hour)) {
			return nil, fmt.Errorf("required shadow evidence for %s is not a complete UTC day", day)
		}
		report, err := shadow.BuildReport(
			policy, replayed.Ledger, replayed.Counts, replayed.Stats,
			replayed.ClosingPrice, from, to,
		)
		if err != nil {
			return nil, fmt.Errorf("required shadow evidence for %s cannot be scored: %w", day, err)
		}
		if !report.Trustworthy() {
			return nil, fmt.Errorf(
				"required shadow evidence for %s has only %d.%02d%% observable coverage",
				day, report.ObservableBPS/100, report.ObservableBPS%100,
			)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func summarizeShadowReview(
	policy shadow.Policy,
	reports []shadow.Report,
) (shadowReviewResult, error) {
	if len(reports) == 0 {
		return shadowReviewResult{}, errors.New("shadow review needs at least one complete report")
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return shadowReviewResult{}, err
	}
	result := shadowReviewResult{
		Version: 1, Status: "strategy_evidence_complete_not_approved",
		EvaluationMode: shadow.EvaluationResetDaily,
		PolicySHA256:   fingerprint, From: reports[0].From,
		To: reports[len(reports)-1].To, CompleteDays: uint32(len(reports)),
		MinimumObservableBPS:     math.MaxInt32,
		RequiresOperatorDecision: true,
	}
	for index, report := range reports {
		if report.Version != policy.Version || report.Cluster != policy.Cluster ||
			report.Cluster != shadow.Mainnet || report.Market != policy.Market ||
			!report.Trustworthy() || !report.To.Equal(report.From.Add(24*time.Hour)) ||
			(index > 0 && !report.From.Equal(reports[index-1].To)) {
			return shadowReviewResult{}, errors.New("shadow review reports are not consecutive complete trustworthy Mainnet days")
		}
		if report.ObservableBPS < result.MinimumObservableBPS {
			result.MinimumObservableBPS = report.ObservableBPS
		}
		if !addShadowReviewCounter(&result.Signals, report.Counts.Signals) ||
			!addShadowReviewCounter(&result.Fills, report.Counts.Fills) ||
			!addShadowReviewCounter(&result.Filtered, report.Counts.Filtered) ||
			!addShadowReviewCounter(&result.Refused, report.Counts.Refused) ||
			!addShadowReviewCounter(&result.Missed, report.Counts.Missed) ||
			!addShadowReviewCounter(&result.Unobservable, report.Counts.Unobservable) {
			return shadowReviewResult{}, errors.New("shadow review counters overflow")
		}
		if (report.VersusHoldMicros > 0 && result.VersusHoldMicros > math.MaxInt64-report.VersusHoldMicros) ||
			(report.VersusHoldMicros < 0 && result.VersusHoldMicros < math.MinInt64-report.VersusHoldMicros) {
			return shadowReviewResult{}, errors.New("shadow review result overflows")
		}
		result.VersusHoldMicros += report.VersusHoldMicros
		result.MaximumDrawdownMicros = max(result.MaximumDrawdownMicros, report.MaxDrawdownMicros)
	}
	return result, nil
}

func addShadowReviewCounter(total *uint64, value uint64) bool {
	if math.MaxUint64-*total < value {
		return false
	}
	*total += value
	return true
}

func renderShadowReview(output io.Writer, result shadowReviewResult) error {
	_, err := fmt.Fprintf(output,
		"Strategy evidence is complete for %d consecutive UTC day(s).\n"+
			"  period       %s to %s\n"+
			"  observability at least %d.%02d%% each day\n"+
			"  signals      %d; hypothetical fills %d; filtered %d; refused %d; missed %d\n"+
			"  summed daily-reset vs holding $%s; maximum single-day drawdown $%s\n\n"+
			"These reset-daily results are an operational canary and cannot be compounded.\n"+
			"This is evidence for an operator to review, not strategy approval.\n"+
			"Nothing was signed, submitted, or enabled.\n",
		result.CompleteDays, result.From.Format(time.RFC3339), result.To.Format(time.RFC3339),
		result.MinimumObservableBPS/100, result.MinimumObservableBPS%100,
		result.Signals, result.Fills, result.Filtered, result.Refused, result.Missed,
		formatSignedMicros(result.VersusHoldMicros),
		formatUnits(result.MaximumDrawdownMicros, 6),
	)
	return err
}
