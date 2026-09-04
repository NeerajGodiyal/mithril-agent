package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
)

const shadowMarketUsage = `Usage:
  mithril-agent shadow market collect --market NAME --observe ADDR --journal PATH [--dashboard-status PATH] [--once]
  mithril-agent shadow market curve --market NAME --observe ADDR
  mithril-agent shadow market diagnose --journal PATH [--hours 6]
  mithril-agent shadow market provisional --journal PATH --out PATH
  mithril-agent shadow market paper-check --policy PATH --provisional-artifact PATH --journal PATH [--dashboard-status PATH] [--result-out PATH] [--candidate-policy-out PATH]
  mithril-agent shadow market evaluate --journal PATH --out PATH

Collect attempts one immutable, hash-chained observation per minute; missed
 buckets count unavailable. Curve samples serial $10/$25/$50/$100 Jupiter
 round trips and emits diagnostic-only size evidence. Diagnose prints a recent 1-168 hour operational
summary but cannot qualify a market or create an artifact. Provisional writes
a paper-only six-hour checkpoint which expires quickly and cannot authorize a
proposal. Paper-check selects only on the first four hours, then runs normal
plus doubled-modelled-spread replays on the final two untouched hours. Its JSON is research-only and cannot
activate or promote a market. On a passing result, --candidate-policy-out can
write the exact immutable policy for further paper testing. Evaluate checks the latest 30 complete UTC days from that exact durable journal prefix
and writes a new artifact without replacing an existing file. Qualification
covers market-data and route quality only; it does not start a paper strategy.

Both commands are keyless and cannot sign or submit.
Allowlisted markets: WIF/USDC, JTO/USDC, PYTH/USDC`

const maxMarketAdmissionArtifactBytes = 1 << 20

func runShadowMarket(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, shadowMarketUsage)
		return err
	}
	switch args[0] {
	case "collect":
		return runShadowMarketCollect(ctx, args[1:], output)
	case "curve":
		return runShadowMarketCurve(ctx, args[1:], output)
	case "diagnose":
		return runShadowMarketDiagnose(args[1:], output)
	case "provisional":
		return runShadowMarketProvisional(args[1:], output)
	case "paper-check":
		return runShadowMarketPaperCheck(args[1:], output)
	case "evaluate":
		return runShadowMarketEvaluate(args[1:], output)
	default:
		return errors.New("shadow market expects collect, curve, diagnose, provisional, paper-check, or evaluate")
	}
}

func runShadowMarketProvisional(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market provisional", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	journalPath := flags.String("journal", "", "absolute evidence journal path")
	outPath := flags.String("out", "", "new paper-only evidence artifact")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowMarketUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *journalPath == "" || *outPath == "" {
		return errors.New("shadow market provisional requires --journal and --out")
	}
	if err := validateMarketAdmissionPath(*journalPath, "--journal"); err != nil {
		return err
	}
	if err := validateMarketAdmissionPath(*outPath, "--out"); err != nil {
		return err
	}
	verified, err := journal.Verify(*journalPath)
	if err != nil {
		if errors.Is(err, journal.ErrLocked) {
			return errors.New("stop the market collector before creating a provisional checkpoint")
		}
		return err
	}
	prefix := journal.DurablePrefix{
		Format: journal.Format, Bytes: verified.Bytes, Records: verified.Records,
		ChainHeadSHA256: verified.ChainHeadSHA256,
	}
	artifact, err := marketadmission.EvaluateProvisionalJournal(
		*journalPath, prefix, time.Now(),
	)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := securefile.CreatePrivate(*outPath, encoded, maxMarketAdmissionArtifactBytes); err != nil {
		return err
	}
	return writeShadowMarketJSON(output, struct {
		Market     string `json:"market"`
		Status     string `json:"status"`
		PaperReady bool   `json:"provisional_paper_ready"`
		Artifact   string `json:"artifact"`
	}{artifact.Candidate.Market, artifact.Status, artifact.ProvisionalPaperReady, *outPath})
}

func runShadowMarketDiagnose(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market diagnose", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	journalPath := flags.String("journal", "", "absolute evidence journal path")
	hours := flags.Uint("hours", 6, "recent completed hours, 1..168")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowMarketUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *journalPath == "" || *hours == 0 || *hours > 168 {
		return errors.New("shadow market diagnose requires --journal and --hours 1..168")
	}
	if err := validateMarketAdmissionPath(*journalPath, "--journal"); err != nil {
		return err
	}
	verified, err := journal.Verify(*journalPath)
	if err != nil {
		if errors.Is(err, journal.ErrLocked) {
			return errors.New("stop the market collector before diagnosing its journal")
		}
		return err
	}
	diagnostic, err := marketadmission.DiagnoseJournal(
		*journalPath,
		journal.DurablePrefix{
			Format: journal.Format, Bytes: verified.Bytes, Records: verified.Records,
			ChainHeadSHA256: verified.ChainHeadSHA256,
		},
		time.Now(), time.Duration(*hours)*time.Hour,
	)
	if err != nil {
		return err
	}
	return writeShadowMarketJSON(output, diagnostic)
}

func runShadowMarketCollect(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market collect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	market := flags.String("market", "", "allowlisted market")
	observe := flags.String("observe", "", "watch-only quote address")
	journalPath := flags.String("journal", "", "absolute evidence journal path")
	dashboardStatusPath := flags.String("dashboard-status", "", "optional sibling dashboard-status.json")
	once := flags.Bool("once", false, "collect one scheduled bucket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowMarketUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *market == "" || *observe == "" {
		return errors.New("shadow market collect requires --market, --observe, and --journal")
	}
	if err := validateMarketAdmissionPath(*journalPath, "--journal"); err != nil {
		return err
	}
	if err := validateMarketDashboardStatusPath(*dashboardStatusPath, *journalPath); err != nil {
		return err
	}
	candidate, ok := marketadmission.Lookup(*market)
	if !ok {
		return errors.New("--market must be WIF/USDC, JTO/USDC, or PYTH/USDC")
	}
	opening, err := marketadmission.NewOpening(
		candidate, *observe, marketadmission.DefaultThresholds(),
	)
	if err != nil {
		return err
	}
	collector, err := newMarketAdmissionCollector(opening)
	if err != nil {
		return err
	}
	store, err := journal.OpenRotating(*journalPath)
	if err != nil {
		return err
	}
	defer store.Close()
	lastBucket, err := prepareMarketAdmissionJournal(store, opening, time.Now().UTC())
	if err != nil {
		return err
	}
	var dashboardTracker *marketadmission.DiagnosticTracker
	if *dashboardStatusPath != "" {
		now := time.Now().UTC()
		dashboardTracker, err = marketadmission.NewDiagnosticTracker(
			opening, store.Records(), now,
		)
		if err != nil {
			return err
		}
		if err := writeMarketDashboardStatus(
			*dashboardStatusPath, dashboardTracker, now,
		); err != nil {
			return err
		}
	}
	if !*once {
		if err := writeShadowMarketJSON(output, struct {
			Market         string `json:"market"`
			State          string `json:"state"`
			CadenceSeconds uint64 `json:"cadence_seconds"`
		}{opening.Candidate.Market, "collecting", opening.Thresholds.CadenceSeconds}); err != nil {
			return err
		}
	}
	return collectMarketAdmission(
		ctx, output, store, collector, opening, lastBucket,
		dashboardTracker, *dashboardStatusPath, *once,
	)
}

func newMarketAdmissionCollector(
	opening marketadmission.Opening,
) (*marketadmission.Collector, error) {
	endpoint := os.Getenv(shadowEndpointEnvironment)
	if err := validateShadowEndpoint(endpoint); err != nil {
		return nil, err
	}
	reader := publicAccountReader(endpoint)
	marketPrimary, err := pricesource.NewPythPushFromSpec(
		reader, time.Now, opening.Candidate.Pyth,
	)
	if err != nil {
		return nil, err
	}
	marketSecondary, err := pricesource.NewKrakenFromSpec(nil, opening.Candidate.Kraken)
	if err != nil {
		return nil, err
	}
	usdcPrimary, err := pricesource.NewPythPushUSDC(reader, time.Now)
	if err != nil {
		return nil, err
	}
	solPrimary, err := pricesource.NewPythPush(reader, time.Now)
	if err != nil {
		return nil, err
	}
	quotes, err := jupiterquote.New(os.Getenv(jupiterAPIKeyEnvironment))
	if err != nil {
		return nil, err
	}
	return marketadmission.NewCollector(opening, marketadmission.Sources{
		Mint: reader, MarketPrimary: marketPrimary, MarketSecondary: marketSecondary,
		USDCPrimary: usdcPrimary, USDCSecondary: pricesource.NewKraken(nil),
		SOLPrimary: solPrimary, SOLSecondary: pricesource.NewKrakenSOL(nil),
		Quotes: quotes,
	}, time.Now)
}

func collectMarketAdmission(
	ctx context.Context,
	output io.Writer,
	store *journal.Store,
	collector *marketadmission.Collector,
	opening marketadmission.Opening,
	lastBucket time.Time,
	dashboardTracker *marketadmission.DiagnosticTracker,
	dashboardStatusPath string,
	once bool,
) error {
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	for {
		bucket := nextMarketAdmissionBucket(time.Now().UTC(), cadence)
		if !lastBucket.IsZero() && !bucket.After(lastBucket) {
			bucket = lastBucket.Add(cadence)
		}
		if err := waitForMarketAdmissionBucket(ctx, bucket); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if marketAdmissionBucketExpired(time.Now().UTC(), bucket, cadence) {
			lastBucket = bucket
			continue
		}
		observation := collector.RunTick(ctx, bucket)
		if err := observation.Validate(opening); err != nil {
			return fmt.Errorf("market evidence observation is invalid: %w", err)
		}
		if err := appendMarketAdmissionObservation(
			store, observation, dashboardTracker, dashboardStatusPath, time.Now().UTC(),
		); err != nil {
			return err
		}
		lastBucket = bucket
		if once {
			return writeShadowMarketJSON(output, observation)
		}
	}
}

func appendMarketAdmissionObservation(
	store *journal.Store,
	observation marketadmission.Observation,
	dashboardTracker *marketadmission.DiagnosticTracker,
	dashboardStatusPath string,
	now time.Time,
) error {
	if _, err := store.Append(
		observation.ObservedAt,
		marketadmission.EventObserved,
		observation.Bucket.Format(time.RFC3339),
		observation,
	); err != nil {
		return err
	}
	if dashboardStatusPath == "" {
		return nil
	}
	if err := dashboardTracker.Add(observation); err != nil {
		return err
	}
	return writeMarketDashboardStatus(dashboardStatusPath, dashboardTracker, now)
}

func writeMarketDashboardStatus(
	path string,
	tracker *marketadmission.DiagnosticTracker,
	now time.Time,
) error {
	if path == "" {
		return nil
	}
	status, err := tracker.Status(now)
	if err != nil {
		return err
	}
	status = preserveMarketDashboardPaperCheck(path, status)
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return securefile.ReplacePrivate(
		path, append(encoded, '\n'), marketadmission.MaxDashboardStatusBytes,
	)
}

func preserveMarketDashboardPaperCheck(
	path string,
	status marketadmission.DashboardStatus,
) marketadmission.DashboardStatus {
	raw, err := securefile.ReadPrivate(path, marketadmission.MaxDashboardStatusBytes)
	if err != nil {
		return status
	}
	previous, err := marketadmission.LoadDashboardStatus(raw)
	if err != nil || previous.PaperCheck == nil {
		return status
	}
	preserved, err := status.WithPaperCheck(*previous.PaperCheck)
	if err != nil {
		return status
	}
	return preserved
}

func marketAdmissionBucketExpired(now, bucket time.Time, cadence time.Duration) bool {
	return !now.UTC().Before(bucket.UTC().Add(cadence - 5*time.Second))
}

func waitForMarketAdmissionBucket(ctx context.Context, bucket time.Time) error {
	delay := time.Until(bucket)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextMarketAdmissionBucket(now time.Time, cadence time.Duration) time.Time {
	return now.UTC().Truncate(cadence).Add(cadence)
}

func prepareMarketAdmissionJournal(
	store *journal.Store,
	opening marketadmission.Opening,
	now time.Time,
) (time.Time, error) {
	records := store.Records()
	if len(records) == 0 {
		if _, err := store.Append(
			now.UTC(), marketadmission.EventOpened, opening.ContentSHA256, opening,
		); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, nil
	}
	return marketadmission.ValidateResume(records, opening)
}

func runShadowMarketEvaluate(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market evaluate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	journalPath := flags.String("journal", "", "absolute evidence journal path")
	outPath := flags.String("out", "", "new evidence artifact")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowMarketUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *journalPath == "" || *outPath == "" {
		return errors.New("shadow market evaluate requires --journal and --out")
	}
	if err := validateMarketAdmissionPath(*journalPath, "--journal"); err != nil {
		return err
	}
	if err := validateMarketAdmissionPath(*outPath, "--out"); err != nil {
		return err
	}
	verified, err := journal.Verify(*journalPath)
	if err != nil {
		if errors.Is(err, journal.ErrLocked) {
			return errors.New("stop the market collector before evaluating its journal")
		}
		return err
	}
	prefix := journal.DurablePrefix{
		Format: journal.Format, Bytes: verified.Bytes, Records: verified.Records,
		ChainHeadSHA256: verified.ChainHeadSHA256,
	}
	artifact, err := marketadmission.EvaluateJournal(*journalPath, prefix, time.Now())
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := securefile.CreatePrivate(*outPath, encoded, maxMarketAdmissionArtifactBytes); err != nil {
		return err
	}
	return writeShadowMarketJSON(output, struct {
		Market                 string `json:"market"`
		OperationallyQualified bool   `json:"operationally_qualified"`
		AvailabilityBPS        uint16 `json:"availability_bps"`
		ArtifactSHA256         string `json:"artifact_sha256"`
		Artifact               string `json:"artifact"`
	}{
		artifact.Candidate.Market, artifact.OperationallyQualified,
		artifact.AvailabilityBPS, artifact.ContentSHA256, *outPath,
	})
}

func validateMarketAdmissionPath(path, flagName string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New(flagName + " must be a clean absolute path")
	}
	return nil
}

func validateMarketDashboardStatusPath(path, journalPath string) error {
	if path == "" {
		return nil
	}
	if err := validateMarketAdmissionPath(path, "--dashboard-status"); err != nil {
		return err
	}
	want := filepath.Join(filepath.Dir(journalPath), "dashboard-status.json")
	if path != want || path == journalPath {
		return errors.New("--dashboard-status must be the journal sibling dashboard-status.json")
	}
	return nil
}

func loadQualifiedMarketAdmission(
	artifactPath, journalPath string,
	now time.Time,
) (marketadmission.Artifact, error) {
	if err := validateMarketAdmissionPath(artifactPath, "--admission-artifact"); err != nil {
		return marketadmission.Artifact{}, err
	}
	if err := validateMarketAdmissionPath(journalPath, "--admission-journal"); err != nil {
		return marketadmission.Artifact{}, err
	}
	var artifact marketadmission.Artifact
	if err := readStrictJSON(artifactPath, &artifact); err != nil || artifact.Validate() != nil {
		return marketadmission.Artifact{}, errors.New("market admission artifact is invalid")
	}
	if !artifact.OperationallyQualified {
		return marketadmission.Artifact{}, errors.New("market admission evidence is not qualified")
	}
	if !currentMarketAdmission(artifact, now) {
		return marketadmission.Artifact{}, errors.New("market admission evidence is not the current completed window")
	}
	if err := artifact.VerifyJournal(journalPath); err != nil {
		return marketadmission.Artifact{}, errors.New("market admission artifact does not match its journal")
	}
	return artifact, nil
}

func loadProvisionalMarketAdmission(
	artifactPath, journalPath string,
	now time.Time,
) (marketadmission.ProvisionalArtifact, error) {
	if err := validateMarketAdmissionPath(artifactPath, "--provisional-artifact"); err != nil {
		return marketadmission.ProvisionalArtifact{}, err
	}
	if err := validateMarketAdmissionPath(journalPath, "--provisional-journal"); err != nil {
		return marketadmission.ProvisionalArtifact{}, err
	}
	var artifact marketadmission.ProvisionalArtifact
	if err := readStrictJSON(artifactPath, &artifact); err != nil || artifact.Validate() != nil {
		return marketadmission.ProvisionalArtifact{}, errors.New("provisional market evidence artifact is invalid")
	}
	if !artifact.ProvisionalPaperReady {
		return marketadmission.ProvisionalArtifact{}, errors.New("provisional market evidence is not ready for paper testing")
	}
	if !artifact.Current(now) {
		return marketadmission.ProvisionalArtifact{}, errors.New("provisional market evidence is stale")
	}
	if err := artifact.VerifyJournal(journalPath); err != nil {
		return marketadmission.ProvisionalArtifact{}, errors.New("provisional market evidence does not match its journal")
	}
	return artifact, nil
}

func currentMarketAdmission(artifact marketadmission.Artifact, now time.Time) bool {
	return !now.IsZero() && artifact.Through.Equal(now.UTC().Truncate(24*time.Hour))
}

func writeShadowMarketJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
