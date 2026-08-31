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
  mithril-agent shadow market collect --market NAME --observe ADDR --journal PATH [--once]
  mithril-agent shadow market evaluate --journal PATH --out PATH

Collect attempts one immutable, hash-chained observation per minute; missed
buckets count unavailable. Evaluate
checks the latest 30 complete UTC days from that exact durable journal prefix
and writes a new artifact without replacing an existing file. Qualification
covers market-data and route quality only; it does not start a paper strategy.

Both commands are keyless and cannot sign or submit.
Allowlisted market: WIF/USDC`

const maxMarketAdmissionArtifactBytes = 1 << 20

func runShadowMarket(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, shadowMarketUsage)
		return err
	}
	switch args[0] {
	case "collect":
		return runShadowMarketCollect(ctx, args[1:], output)
	case "evaluate":
		return runShadowMarketEvaluate(args[1:], output)
	default:
		return errors.New("shadow market expects collect or evaluate")
	}
}

func runShadowMarketCollect(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow market collect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	market := flags.String("market", "", "allowlisted market")
	observe := flags.String("observe", "", "watch-only quote address")
	journalPath := flags.String("journal", "", "absolute evidence journal path")
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
	candidate, ok := marketadmission.Lookup(*market)
	if !ok {
		return errors.New("--market must be WIF/USDC")
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
		ctx, output, store, collector, opening, lastBucket, *once,
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
		if _, err := store.Append(
			observation.ObservedAt,
			marketadmission.EventObserved,
			bucket.Format(time.RFC3339),
			observation,
		); err != nil {
			return err
		}
		lastBucket = bucket
		if once {
			return writeShadowMarketJSON(output, observation)
		}
	}
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

func currentMarketAdmission(artifact marketadmission.Artifact, now time.Time) bool {
	return !now.IsZero() && artifact.Through.Equal(now.UTC().Truncate(24*time.Hour))
}

func writeShadowMarketJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
