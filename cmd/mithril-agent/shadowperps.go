package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const (
	shadowPerpsTapeVersion     uint32 = 4
	shadowPerpsStatusVersion   uint32 = 4
	shadowPerpsMaxFrames              = 1_500
	shadowPerpsMaxFileBytes    int64  = 16 << 20
	shadowPerpsModel                  = "hyperliquid_causal_sampled_context_stress_v4"
	shadowPerpsLegacyModel            = "hyperliquid_causal_sampled_context_stress_v3"
	shadowPerpsCandleSettleLag        = 2 * time.Minute
	shadowPerpsMaxClockSkew           = 5 * time.Second
	shadowPerpsMaxBookAge             = 30 * time.Second
	shadowPerpsMaxArchivedRuns        = 8
)

const shadowPerpsPaperUsage = `Usage: mithril-agent shadow perps-paper-run --state-dir PATH [options]

Runs a signer-free perpetual-futures paper experiment from public market data
and fully closed candles. It never submits an order. Marking uses sampled venue
mark prices; funding uses the latest prior sampled oracle, so results remain a
causal sampled approximation rather than an exact venue account statement.

Options:
  --environment NAME       Hyperliquid mainnet or testnet (default mainnet)
  --symbols LIST           comma-separated SOL, BTC, and/or ETH (default SOL,BTC,ETH)
  --arm NAME               conservative, balanced, or experimental (default balanced)
  --paper-usd-per-market N simulated collateral for each market (default 100)
  --cadence DURATION       public-data polling interval (default 15s)
  --duration DURATION      bounded lifetime of this invocation (default 6h)
  --archive-dir PATH       private directory that retains each previous run
  --once                   collect at most one new closed-candle frame and exit`

type shadowPerpsReader interface {
	MetaAndAssetContexts(context.Context) (perpspaper.MetaAndAssetContexts, error)
	Candles(context.Context, perpspaper.Symbol, string, int64, int64) ([]perpspaper.Candle, error)
	Book(context.Context, perpspaper.Symbol) (perpspaper.L2Book, error)
	FundingHistory(context.Context, perpspaper.Symbol, int64, int64) ([]perpspaper.Funding, error)
}

type shadowPerpsReaderFactory func(perpspaper.Environment) (shadowPerpsReader, error)

type shadowPerpsTapeConfig struct {
	Environment              perpspaper.Environment `json:"environment"`
	Symbol                   perpspaper.Symbol      `json:"symbol"`
	RiskArm                  perpspaper.RiskArm     `json:"risk_arm"`
	StartingCollateralMicros uint64                 `json:"starting_collateral_micros"`
	VenueMaxLeverage         uint32                 `json:"venue_max_leverage"`
	VenueSzDecimals          uint8                  `json:"venue_sz_decimals"`
	DecisionMode             string                 `json:"decision_mode,omitempty"`
	Strategy                 perpspaper.Strategy    `json:"strategy,omitempty"`
	PlanSHA256               string                 `json:"plan_sha256,omitempty"`
	QualificationInputSHA256 string                 `json:"qualification_input_sha256,omitempty"`
}

type shadowPerpsTape struct {
	Version          uint32                 `json:"version"`
	PaperOnly        bool                   `json:"paper_only"`
	ExecutionEnabled bool                   `json:"execution_enabled"`
	AccountingModel  string                 `json:"accounting_model"`
	Config           shadowPerpsTapeConfig  `json:"config"`
	Frames           []perpspaper.TapeFrame `json:"frames"`
}

type shadowPerpsStatus struct {
	Version                  uint32                 `json:"version"`
	ObservedAt               time.Time              `json:"observed_at"`
	PaperOnly                bool                   `json:"paper_only"`
	ExecutionEnabled         bool                   `json:"execution_enabled"`
	AccountingModel          string                 `json:"accounting_model"`
	Environment              perpspaper.Environment `json:"environment"`
	Symbol                   perpspaper.Symbol      `json:"symbol"`
	RiskArm                  perpspaper.RiskArm     `json:"risk_arm"`
	DecisionMode             string                 `json:"decision_mode"`
	Strategy                 perpspaper.Strategy    `json:"strategy,omitempty"`
	PlanSHA256               string                 `json:"plan_sha256"`
	QualificationInputSHA256 string                 `json:"qualification_input_sha256,omitempty"`
	Frames                   int                    `json:"frames"`
	NewFrame                 bool                   `json:"new_frame"`
	LastAction               string                 `json:"last_action,omitempty"`
	LastDecision             *perpspaper.Decision   `json:"last_decision,omitempty"`
	State                    perpspaper.State       `json:"state"`
}

func runShadowPerpsPaper(ctx context.Context, args []string, output io.Writer) error {
	return runShadowPerpsPaperWith(ctx, args, output, time.Now, func(environment perpspaper.Environment) (shadowPerpsReader, error) {
		return perpspaper.NewHyperliquidClient(environment, nil)
	})
}

func runShadowPerpsPaperWith(
	ctx context.Context,
	args []string,
	output io.Writer,
	now func() time.Time,
	newReader shadowPerpsReaderFactory,
) (returnErr error) {
	flags := flag.NewFlagSet("shadow perps-paper-run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	environmentText := flags.String("environment", string(perpspaper.Mainnet), "Hyperliquid environment")
	symbolsText := flags.String("symbols", "SOL,BTC,ETH", "comma-separated paper markets")
	armText := flags.String("arm", string(perpspaper.Balanced), "paper risk arm")
	paperUSD := flags.String("paper-usd-per-market", "100", "simulated collateral per market")
	stateDir := flags.String("state-dir", "", "private paper state directory")
	archiveDir := flags.String("archive-dir", "", "private previous-run archive directory")
	cadence := flags.Duration("cadence", 15*time.Second, "public-data polling cadence")
	duration := flags.Duration("duration", 6*time.Hour, "bounded invocation lifetime")
	once := flags.Bool("once", false, "collect at most one frame")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPerpsPaperUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *stateDir == "" {
		return errors.New("shadow perps-paper-run requires --state-dir and takes no positional arguments")
	}
	if !filepath.IsAbs(*stateDir) || filepath.Clean(*stateDir) != *stateDir {
		return errors.New("shadow perps-paper-run requires a clean absolute --state-dir")
	}
	if *archiveDir != "" && (!filepath.IsAbs(*archiveDir) || filepath.Clean(*archiveDir) != *archiveDir ||
		filepath.Dir(*archiveDir) != filepath.Dir(*stateDir) || *archiveDir == *stateDir) {
		return errors.New("shadow perps-paper-run requires --archive-dir beside --state-dir")
	}
	if *cadence < time.Second || *cadence > 30*time.Second {
		return errors.New("shadow perps-paper-run cadence must be between 1s and 30s")
	}
	if *duration < time.Minute || *duration > 24*time.Hour {
		return errors.New("shadow perps-paper-run duration must be between 1m and 24h")
	}
	environment := perpspaper.Environment(*environmentText)
	if environment != perpspaper.Mainnet && environment != perpspaper.Testnet {
		return fmt.Errorf("unsupported Hyperliquid environment %q", environment)
	}
	arm := perpspaper.RiskArm(*armText)
	if arm != perpspaper.Conservative && arm != perpspaper.Balanced && arm != perpspaper.Experimental {
		return fmt.Errorf("unsupported paper risk arm %q", arm)
	}
	symbols, err := parseShadowPerpsSymbols(*symbolsText)
	if err != nil {
		return err
	}
	collateral, err := parseUSDThreshold(*paperUSD, "paper collateral")
	if err != nil || collateral > perpspaper.MaxStartingCollateralMicros {
		return errors.New("paper collateral must be a positive USD amount with at most six decimals")
	}
	runCtx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	reader, err := newReader(environment)
	if err != nil {
		return err
	}
	metadata, err := reader.MetaAndAssetContexts(runCtx)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil
		}
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		return fmt.Errorf("read Hyperliquid paper metadata: %w", err)
	}
	assets := make(map[perpspaper.Symbol]perpspaper.AssetMeta, len(metadata.Universe))
	for _, asset := range metadata.Universe {
		assets[asset.Name] = asset
	}
	for _, symbol := range symbols {
		if _, ok := assets[symbol]; !ok {
			return fmt.Errorf("Hyperliquid metadata has no active %s perpetual market", symbol)
		}
	}

	startedAt := now().UTC()
	publishQualification := false
	if *archiveDir != "" {
		if err := prepareShadowPerpsRun(*stateDir, *archiveDir, startedAt); err != nil {
			return err
		}
		defer func() {
			if !publishQualification {
				return
			}
			returnErr = errors.Join(returnErr, finalizeAndPublishShadowPerps(
				*stateDir, shadowPerpsPublishedDir(*stateDir), symbols, now().UTC()))
		}()
	} else if err := os.Mkdir(*stateDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create perps paper state directory: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(*stateDir); err != nil {
		return errors.New("perps paper state directory is not trusted")
	}
	info, err := os.Lstat(*stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("perps paper state directory must already be private mode 0700")
	}
	plans := make(map[perpspaper.Symbol]shadowPerpsPlan, len(symbols))
	planDigests := make(map[perpspaper.Symbol]string, len(symbols))
	for _, symbol := range symbols {
		asset := assets[symbol]
		plan, digest, err := loadOrCreateShadowPerpsPlan(
			*stateDir, environment,
			perpspaper.QualificationConfig{
				StartingCollateralMicros: collateral, Symbol: symbol,
				VenueMaxLeverage: asset.MaxLeverage, VenueSzDecimals: asset.SzDecimals,
			},
			arm, startedAt,
		)
		if err != nil {
			return fmt.Errorf("load %s paper plan: %w", symbol, err)
		}
		plans[symbol], planDigests[symbol] = plan, digest
	}

	encoder := json.NewEncoder(output)
	for {
		statuses := make([]shadowPerpsStatus, len(symbols))
		errorsByMarket := make([]error, len(symbols))
		cycleNow := now().UTC()
		var updates sync.WaitGroup
		for index, symbol := range symbols {
			updates.Add(1)
			go func() {
				defer updates.Done()
				statuses[index], errorsByMarket[index] = updateShadowPerpsMarket(
					runCtx, reader, *stateDir, plans[symbol], planDigests[symbol], symbol, collateral,
					assets[symbol], cycleNow, now,
				)
			}()
		}
		updates.Wait()
		for index, symbol := range symbols {
			status, err := statuses[index], errorsByMarket[index]
			if err != nil {
				if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
					publishQualification = true
					return nil
				}
				if runCtx.Err() != nil {
					return runCtx.Err()
				}
				return fmt.Errorf("update %s paper market: %w", symbol, err)
			}
			if status.NewFrame || *once {
				if err := encoder.Encode(status); err != nil {
					return err
				}
			}
		}
		if *archiveDir != "" {
			if err := publishShadowPerpsStatuses(*stateDir, shadowPerpsPublishedDir(*stateDir), symbols); err != nil {
				return err
			}
		}
		if *once {
			publishQualification = true
			return nil
		}
		timer := time.NewTimer(*cadence)
		select {
		case <-runCtx.Done():
			timer.Stop()
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				publishQualification = true
				return nil
			}
			return runCtx.Err()
		case <-timer.C:
		}
	}
}

func prepareShadowPerpsRun(stateDir, archiveDir string, startedAt time.Time) error {
	if err := os.Mkdir(archiveDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create perps paper archive directory: %w", err)
	}
	publishedDir := shadowPerpsPublishedDir(stateDir)
	if err := os.Mkdir(publishedDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create perps paper published directory: %w", err)
	}
	for _, directory := range []string{filepath.Dir(stateDir), archiveDir, publishedDir} {
		if err := secureexec.ValidateProtectedDirectory(directory); err != nil {
			return errors.New("perps paper archive directory is not trusted")
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return errors.New("perps paper archive directory must be private mode 0700")
		}
	}
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			return err
		}
		return pruneShadowPerpsArchives(archiveDir, shadowPerpsMaxArchivedRuns)
	}
	if err != nil {
		return fmt.Errorf("read previous perps paper run: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(stateDir); err != nil {
		return errors.New("previous perps paper run is not trusted")
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("previous perps paper run must be private mode 0700")
	}
	if len(entries) == 0 {
		return pruneShadowPerpsArchives(archiveDir, shadowPerpsMaxArchivedRuns)
	}
	if err := preserveCompletedShadowPerpsTapes(stateDir); err != nil {
		return err
	}
	target := filepath.Join(archiveDir, startedAt.Format("20060102T150405.000000000Z"))
	if err := securefile.RenameNoReplace(stateDir, target); err != nil {
		return fmt.Errorf("archive previous perps paper run: %w", err)
	}
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		return fmt.Errorf("create current perps paper run: %w", err)
	}
	return pruneShadowPerpsArchives(archiveDir, shadowPerpsMaxArchivedRuns)
}

func finalizeAndPublishShadowPerps(stateDir, publishedDir string, symbols []perpspaper.Symbol, endedAt time.Time) error {
	finalizeErr := finalizeShadowPerpsRun(stateDir, symbols, endedAt)
	if finalizeErr != nil {
		var researchErr *shadowPerpsResearchError
		if !errors.As(finalizeErr, &researchErr) {
			return finalizeErr
		}
	}
	return errors.Join(finalizeErr, publishShadowPerpsStatuses(stateDir, publishedDir, symbols))
}

func shadowPerpsPublishedDir(stateDir string) string {
	return filepath.Join(filepath.Dir(stateDir), "published")
}

func publishShadowPerpsStatuses(stateDir, publishedDir string, symbols []perpspaper.Symbol) error {
	statuses := make(map[perpspaper.Symbol][]byte, len(symbols))
	for _, symbol := range symbols {
		name := strings.ToLower(string(symbol)) + "-paper-status.json"
		raw, err := securefile.ReadPrivate(filepath.Join(stateDir, name), shadowPerpsMaxFileBytes)
		if err != nil {
			return fmt.Errorf("read %s published paper status: %w", symbol, err)
		}
		var snapshot paperstatus.Snapshot
		if err := strictjson.Decode(raw, &snapshot); err != nil || paperstatus.ValidateSnapshot(snapshot) != nil {
			return fmt.Errorf("validate %s published paper status", symbol)
		}
		statuses[symbol] = raw
	}
	for _, symbol := range symbols {
		name := strings.ToLower(string(symbol)) + "-paper-status.json"
		raw := statuses[symbol]
		if err := securefile.ReplacePrivate(filepath.Join(publishedDir, name), raw, shadowPerpsMaxFileBytes); err != nil {
			return fmt.Errorf("publish %s paper status: %w", symbol, err)
		}
	}
	return nil
}

func pruneShadowPerpsArchives(archiveDir string, keep int) error {
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		return fmt.Errorf("read perps paper archives: %w", err)
	}
	var names []string
	for _, entry := range entries {
		parsed, parseErr := time.Parse("20060102T150405.000000000Z", entry.Name())
		info, statErr := entry.Info()
		if parseErr != nil || parsed.Format("20060102T150405.000000000Z") != entry.Name() ||
			statErr != nil || !entry.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errors.New("perps paper archive contains an unexpected entry")
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names[:max(0, len(names)-keep)] {
		if err := os.RemoveAll(filepath.Join(archiveDir, name)); err != nil {
			return fmt.Errorf("remove expired perps paper archive: %w", err)
		}
	}
	return nil
}

func finalizeShadowPerpsRun(stateDir string, symbols []perpspaper.Symbol, endedAt time.Time) error {
	var result, researchResult error
	for _, symbol := range symbols {
		name := strings.ToLower(string(symbol))
		tapePath := filepath.Join(stateDir, name+"-tape.json")
		raw, err := securefile.ReadPrivate(tapePath, shadowPerpsMaxFileBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("read %s final paper tape: %w", symbol, err))
			continue
		}
		var header shadowPerpsTape
		if err := strictjson.Decode(raw, &header); err != nil {
			result = errors.Join(result, fmt.Errorf("decode %s final paper tape", symbol))
			continue
		}
		tape, replay, err := readShadowPerpsTape(tapePath, header.Config)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		qualification, err := perpspaper.QualifyTournament(tape.Config.qualificationConfig(), tape.Frames)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("qualify %s paper run: %w", symbol, err))
			continue
		}
		var walkForward *perpspaper.WalkForwardQualification
		var selection *shadowPerpsPlanReceipt
		var outcomeTapeSHA256 string
		var finalTapeSHA256 string
		if tape.Version == shadowPerpsTapeVersion {
			_, finalTapeSHA256, err = canonicalShadowPerpsTape(tape)
			if err != nil {
				result = errors.Join(result, fmt.Errorf("hash %s final paper tape: %w", symbol, err))
				continue
			}
		}
		if qualification.Frames >= qualification.MinimumFrames {
			sealedPath, sealErr := sealShadowPerpsTape(stateDir, tape)
			if sealErr != nil {
				preserveErr := fmt.Errorf("preserve %s paper tape: %w", symbol, sealErr)
				if tape.Version == shadowPerpsTapeVersion {
					result = errors.Join(result, preserveErr)
					continue
				}
				researchResult = errors.Join(researchResult, preserveErr)
			} else {
				finalTapeSHA256 = strings.TrimSuffix(filepath.Base(sealedPath), ".json")
			}
		}
		if tape.Version == shadowPerpsTapeVersion {
			walkForward, _, _, err = evaluateAndRecordShadowPerpsFinalization(
				stateDir, tape, finalTapeSHA256, replay, qualification, endedAt.UTC(),
			)
			if err != nil {
				result = errors.Join(result, fmt.Errorf("evaluate and record %s finalization: %w", symbol, err))
				continue
			}
		} else if qualification.Frames >= qualification.MinimumFrames && finalTapeSHA256 != "" {
			walkForward, err = qualifyShadowPerpsCorpus(stateDir, tape.Config)
			if err != nil {
				qualifyErr := fmt.Errorf("multi-tape qualify %s paper corpus: %w", symbol, err)
				researchResult = errors.Join(researchResult, qualifyErr)
				walkForward = nil
			}
		}
		if walkForward != nil {
			outcomeTapeSHA256 = shadowPerpsOutcomeTapeSHA256(tape.Config, finalTapeSHA256, walkForward)
			if err := writeShadowPerpsJSON(filepath.Join(stateDir, name+"-walk-forward.json"), walkForward); err != nil {
				researchResult = errors.Join(researchResult, err)
				walkForward = nil
			} else {
				if walkForward.EligibleForPaperExperiment && walkForward.Candidate != nil {
					receipt, selectErr := selectQualifiedShadowPerpsPlan(
						stateDir, tape.Config.Environment, tape.Config.PlanSHA256, *walkForward, endedAt,
					)
					if selectErr != nil {
						researchResult = errors.Join(researchResult, fmt.Errorf("select %s next paper plan: %w", symbol, selectErr))
					} else {
						selection = &receipt
						if err := writeShadowPerpsJSON(filepath.Join(stateDir, name+"-plan-selection.json"), receipt); err != nil {
							researchResult = errors.Join(researchResult, err)
						}
					}
				}
				if walkForward.TrainingLeader != nil {
					advisory, advisoryErr := perpspaper.EvaluateOneFrameExecutionDelay(
						walkForward.Config, tape.Frames, walkForward.InputSHA256,
						finalTapeSHA256, *walkForward.TrainingLeader,
					)
					if advisoryErr == nil {
						_, _ = writeShadowPerpsExecutionDelayAdvisory(
							stateDir, symbol, *walkForward.TrainingLeader, advisory,
						)
					}
				}
			}
		}
		if err := writeShadowPerpsJSON(filepath.Join(stateDir, name+"-qualification.json"), qualification); err != nil {
			result = errors.Join(result, err)
			continue
		}
		status := buildShadowPerpsStatus(tape.Config, replay, len(tape.Frames), false, endedAt)
		if err := writeShadowPerpsJSON(filepath.Join(stateDir, name+"-status.json"), status); err != nil {
			result = errors.Join(result, err)
			continue
		}
		writer, err := paperstatus.OpenWriter(filepath.Join(stateDir, name+"-paper-status.json"))
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		eventID := "perps-qualification/" + string(symbol) + "/" + qualification.InputSHA256
		message := shadowPerpsQualificationMessage(qualification)
		if walkForward != nil {
			eventID = "perps-walk-forward/" + string(symbol) + "/" + walkForward.InputSHA256
			message = shadowPerpsWalkForwardMessage(*walkForward)
			if selection != nil && selection.PointerUpdated {
				message += "\nNext paper test: " + string(selection.Strategy) + " · " + string(selection.RiskArm)
			}
		}
		if err := writer.Append(endedAt, paperstatus.KindExperimentDone, eventID, message); err != nil {
			result = errors.Join(result, err)
			continue
		}
		current, summary, err := shadowPerpsCurrent(tape.Config, replay, endedAt)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if outcomeTapeSHA256 != "" {
			summary.PerpsPlanOutcome = &paperstatus.PerpsPlanOutcome{
				TapeSHA256: outcomeTapeSHA256,
				Result: shadowPerpsOutcomeResult(
					replay.State.EquityMicros - int64(replay.State.StartingCollateralMicros),
				),
			}
		}
		applyShadowPerpsQualification(&summary, qualification)
		current += "\nCheckpoint: " + shadowPerpsQualificationLabel(qualification)
		if walkForward != nil {
			applyShadowPerpsWalkForward(&summary, *walkForward)
			current += "\nResearch pack: " + shadowPerpsWalkForwardLabel(*walkForward)
			if selection != nil && selection.PointerUpdated {
				current += "\nNext test: " + string(selection.Strategy) + " · " + string(selection.RiskArm)
			}
		}
		if err := writer.UpdateCurrentSummary(endedAt, current, &summary); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result != nil {
		return errors.Join(result, researchResult)
	}
	if researchResult != nil {
		return &shadowPerpsResearchError{err: researchResult}
	}
	return nil
}

func applyShadowPerpsQualification(summary *paperstatus.CurrentSummary, qualification perpspaper.Qualification) {
	summary.QualificationTracked = true
	summary.QualificationOutcome = qualification.Outcome
	summary.QualificationSHA256 = qualification.InputSHA256
	summary.QualificationTapes = 1
	summary.QualificationFrames = qualification.Frames
	summary.QualificationMinimumFrames = qualification.MinimumFrames
	summary.QualificationTrainingFrames = qualification.TrainingFrames
	summary.QualificationHoldoutFrames = qualification.HoldoutFrames
	if qualification.TrainingLeader != nil {
		summary.QualificationStrategy = string(qualification.TrainingLeader.Strategy)
		summary.QualificationRiskProfile = string(qualification.TrainingLeader.RiskArm)
	}
	if qualification.Holdout != nil {
		summary.QualificationHoldoutEvaluated = true
		if qualification.Holdout.Score != nil {
			summary.QualificationHoldoutScored = true
			summary.QualificationHoldoutMicros = qualification.Holdout.Score.NetPnLMicros
		}
	}
	if qualification.Stress != nil {
		summary.QualificationStressEvaluated = true
		if qualification.Stress.Score != nil {
			summary.QualificationStressScored = true
			summary.QualificationStressMicros = qualification.Stress.Score.NetPnLMicros
		}
	}
}

func shadowPerpsQualificationLabel(qualification perpspaper.Qualification) string {
	switch qualification.Outcome {
	case "insufficient_evidence":
		return fmt.Sprintf("collecting (%d/%d frames)", qualification.Frames, qualification.MinimumFrames)
	case "no_training_candidate":
		return "no training plan passed every evidence and safety check"
	case "candidate_rejected":
		return "training leader did not pass held-out checks"
	default:
		return "candidate retained for another paper test"
	}
}

func shadowPerpsQualificationMessage(qualification perpspaper.Qualification) string {
	message := "PAPER · 🧪 PERPS CHECKPOINT COMPLETE\n" + shadowPerpsQualificationLabel(qualification)
	if qualification.TrainingLeader != nil {
		risk := string(qualification.TrainingLeader.RiskArm)
		if qualification.TrainingLeader.RiskArm == perpspaper.Experimental {
			risk = "aggressive"
		}
		message += "\nCandidate checked: " + string(qualification.TrainingLeader.Strategy) + " · " + risk
	}
	if qualification.Holdout != nil && qualification.Holdout.Score != nil {
		message += "\nHeld-out replay: " + formatPerpsResult(qualification.Holdout.Score.NetPnLMicros)
	}
	if qualification.Stress != nil && qualification.Stress.Score != nil {
		message += "\nHigher-cost result: " + formatPerpsResult(qualification.Stress.Score.NetPnLMicros)
	}
	return message + "\nNo real order was sent."
}

func parseShadowPerpsSymbols(value string) ([]perpspaper.Symbol, error) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > 3 {
		return nil, errors.New("paper symbols must be a comma-separated subset of SOL,BTC,ETH")
	}
	seen := make(map[perpspaper.Symbol]bool, len(parts))
	result := make([]perpspaper.Symbol, 0, len(parts))
	for _, part := range parts {
		symbol := perpspaper.Symbol(part)
		if symbol == "" || part != strings.TrimSpace(part) || seen[symbol] ||
			(symbol != perpspaper.SOL && symbol != perpspaper.BTC && symbol != perpspaper.ETH) {
			return nil, errors.New("paper symbols must be a comma-separated subset of SOL,BTC,ETH without duplicates")
		}
		seen[symbol] = true
		result = append(result, symbol)
	}
	return result, nil
}

func updateShadowPerpsMarket(
	ctx context.Context,
	reader shadowPerpsReader,
	stateDir string,
	plan shadowPerpsPlan,
	planSHA256 string,
	symbol perpspaper.Symbol,
	collateral uint64,
	asset perpspaper.AssetMeta,
	now time.Time,
	clock func() time.Time,
) (shadowPerpsStatus, error) {
	var status shadowPerpsStatus
	name := strings.ToLower(string(symbol))
	err := withShadowLifecycleLock(filepath.Join(stateDir, name+"-runner.lock"), func() error {
		var updateErr error
		status, updateErr = updateShadowPerpsMarketUnlocked(
			ctx, reader, stateDir, plan, planSHA256, symbol, collateral, asset, now, clock,
		)
		return updateErr
	})
	return status, err
}

func updateShadowPerpsMarketUnlocked(
	ctx context.Context,
	reader shadowPerpsReader,
	stateDir string,
	plan shadowPerpsPlan,
	planSHA256 string,
	symbol perpspaper.Symbol,
	collateral uint64,
	asset perpspaper.AssetMeta,
	now time.Time,
	clock func() time.Time,
) (shadowPerpsStatus, error) {
	config := shadowPerpsTapeConfig{
		Environment: plan.Environment, Symbol: symbol, RiskArm: plan.Key.RiskArm,
		StartingCollateralMicros: collateral, VenueMaxLeverage: asset.MaxLeverage,
		VenueSzDecimals: asset.SzDecimals, DecisionMode: plan.DecisionMode,
		Strategy: plan.Key.Strategy, PlanSHA256: planSHA256,
		QualificationInputSHA256: plan.QualificationInputSHA256,
	}
	name := strings.ToLower(string(symbol))
	tapePath := filepath.Join(stateDir, name+"-tape.json")
	statusPath := filepath.Join(stateDir, name+"-status.json")
	tape, replay, err := readShadowPerpsTape(tapePath, config)
	if err != nil {
		return shadowPerpsStatus{}, err
	}
	closedEnd := shadowPerpsSettledCandleEnd(now)
	if closedEnd.UnixMilli() <= 0 {
		return shadowPerpsStatus{}, errors.New("system time has no completed one-minute candle")
	}
	if len(tape.Frames) > 0 {
		last := tape.Frames[len(tape.Frames)-1]
		lastClose := last.Candles[len(last.Candles)-1].CloseTime
		switch {
		case lastClose == closedEnd.UnixMilli():
			status := buildShadowPerpsStatus(config, replay, len(tape.Frames), false, now)
			return status, writeShadowPerpsCycle(statusPath, filepath.Join(stateDir, name+"-paper-status.json"), status, config, replay, now)
		case lastClose > closedEnd.UnixMilli():
			return shadowPerpsStatus{}, errors.New("stored paper tape is ahead of the current completed candle")
		case closedEnd.UnixMilli()-lastClose > int64(time.Minute/time.Millisecond):
			return shadowPerpsStatus{}, errors.New("paper runner missed a closed minute; start a new experiment instead of collapsing the gap")
		}
	}
	candles, err := reader.Candles(ctx, symbol, "1m", closedEnd.Add(-4*time.Minute).Add(time.Millisecond).UnixMilli(), closedEnd.UnixMilli())
	if err != nil {
		return shadowPerpsStatus{}, fmt.Errorf("read closed candles: %w", err)
	}
	if len(candles) < 2 {
		return shadowPerpsStatus{}, errors.New("Hyperliquid returned fewer than two closed candles")
	}
	candles = append([]perpspaper.Candle(nil), candles[len(candles)-2:]...)
	latestClose := candles[len(candles)-1].CloseTime
	if len(tape.Frames) == 0 && latestClose != closedEnd.UnixMilli() {
		return shadowPerpsStatus{}, errors.New("Hyperliquid did not return the latest completed one-minute candle")
	}
	newFrame := len(tape.Frames) == 0 || latestClose > tape.Frames[len(tape.Frames)-1].Candles[len(tape.Frames[len(tape.Frames)-1].Candles)-1].CloseTime
	if len(tape.Frames) > 0 && newFrame {
		previousCandle := tape.Frames[len(tape.Frames)-1].Candles[len(tape.Frames[len(tape.Frames)-1].Candles)-1]
		if candles[0] != previousCandle {
			return shadowPerpsStatus{}, errors.New("Hyperliquid changed a previously observed settled candle")
		}
		lastClose := previousCandle.CloseTime
		if latestClose != lastClose+int64(time.Minute/time.Millisecond) {
			return shadowPerpsStatus{}, errors.New("paper candle sequence has a gap")
		}
	}
	if newFrame {
		if len(tape.Frames) >= shadowPerpsMaxFrames {
			return shadowPerpsStatus{}, errors.New("paper tape reached 1500 frames; start a new bounded experiment directory")
		}
		metadata, metadataErr := reader.MetaAndAssetContexts(ctx)
		if metadataErr != nil {
			return shadowPerpsStatus{}, fmt.Errorf("read current mark and oracle context: %w", metadataErr)
		}
		currentAsset, context, metadataErr := shadowPerpsMarketContext(metadata, symbol)
		if metadataErr != nil {
			return shadowPerpsStatus{}, fmt.Errorf("read current mark and oracle context: %w", metadataErr)
		}
		if currentAsset != asset {
			return shadowPerpsStatus{}, errors.New("Hyperliquid paper market metadata changed; start a new experiment")
		}
		contextObservedAt := clock().UTC()
		book, bookErr := reader.Book(ctx, symbol)
		if bookErr != nil {
			return shadowPerpsStatus{}, fmt.Errorf("read visible order book: %w", bookErr)
		}
		bookObservedAt := clock().UTC()
		if bookObservedAt.Before(contextObservedAt) ||
			bookObservedAt.Sub(contextObservedAt) > shadowPerpsMaxBookAge {
			return shadowPerpsStatus{}, errors.New("Hyperliquid paper context collection time is invalid")
		}
		bookTime := time.UnixMilli(book.Time)
		if bookTime.After(bookObservedAt.Add(shadowPerpsMaxClockSkew)) {
			return shadowPerpsStatus{}, errors.New("Hyperliquid paper book is ahead of the local clock")
		}
		if bookTime.Before(bookObservedAt.Add(-shadowPerpsMaxBookAge)) {
			return shadowPerpsStatus{}, errors.New("Hyperliquid paper book is stale")
		}
		if book.Time < latestClose {
			status := buildShadowPerpsStatus(config, replay, len(tape.Frames), false, now)
			return status, writeShadowPerpsCycle(statusPath, filepath.Join(stateDir, name+"-paper-status.json"), status, config, replay, now)
		}
		funding := []perpspaper.Funding(nil)
		if len(tape.Frames) > 0 {
			lastBook := tape.Frames[len(tape.Frames)-1].Book.Time
			if book.Time > lastBook+1 {
				funding, err = reader.FundingHistory(ctx, symbol, lastBook+1, book.Time)
				if err != nil {
					return shadowPerpsStatus{}, fmt.Errorf("read causal funding history: %w", err)
				}
			}
		}
		tape.Frames = append(tape.Frames, perpspaper.TapeFrame{
			Candles: candles,
			Context: perpspaper.PriceContext{
				Symbol: symbol, MarkPx: context.MarkPx, OraclePx: context.OraclePx,
				ReceivedAt: contextObservedAt.UnixMilli(),
			},
			Book: book, Funding: funding,
		})
		replay, err = replayShadowPerpsTape(config, tape.Frames)
		if err != nil {
			return shadowPerpsStatus{}, err
		}
		if err := writeShadowPerpsJSON(tapePath, tape); err != nil {
			return shadowPerpsStatus{}, err
		}
	}
	status := buildShadowPerpsStatus(config, replay, len(tape.Frames), newFrame, now)
	if err := writeShadowPerpsCycle(statusPath, filepath.Join(stateDir, name+"-paper-status.json"), status, config, replay, now); err != nil {
		return shadowPerpsStatus{}, err
	}
	return status, nil
}

func shadowPerpsSettledCandleEnd(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(-shadowPerpsCandleSettleLag - time.Millisecond)
}

func writeShadowPerpsCycle(
	statusPath, paperStatusPath string,
	status shadowPerpsStatus,
	config shadowPerpsTapeConfig,
	replay perpspaper.TapeReplay,
	now time.Time,
) error {
	if err := writeShadowPerpsJSON(statusPath, status); err != nil {
		return err
	}
	writer, err := paperstatus.OpenWriter(paperStatusPath)
	if err != nil {
		return err
	}
	if len(replay.Results) > 0 {
		last := replay.Results[len(replay.Results)-1]
		kind, message := "", ""
		switch last.Action {
		case "opened":
			kind = paperstatus.KindOrderFilled
			direction := "price down"
			if last.Decision.Direction == perpspaper.Direction(perpspaper.Long) {
				direction = "price up"
			}
			message = fmt.Sprintf("PAPER · 🟣 PERPS POSITION OPENED\nDirection: %s\nPaper size: %s\nFilled near: %s\nNo real order was sent.", direction, formatPerpsFillNotional(config.Symbol, last.Fill), formatPerpsFillPrice(last.Fill))
		case "closed":
			kind = paperstatus.KindOrderFilled
			message = fmt.Sprintf("PAPER · 🔵 PERPS POSITION CLOSED\n%s\nPaper size: %s\nFilled near: %s\nNo real order was sent.", shadowPerpsCompletedTradeLine(replay.Records), formatPerpsFillNotional(config.Symbol, last.Fill), formatPerpsFillPrice(last.Fill))
		case "liquidated":
			kind = paperstatus.KindRiskHalted
			message = "PAPER · 🔴 PAPER POSITION LIQUIDATED\n" + shadowPerpsCompletedTradeLine(replay.Records) + "\nThe simulated maintenance-margin rule closed the position.\nNo real order was sent."
		}
		if kind != "" {
			key := fmt.Sprintf("perps/%s/%s/%d", config.Symbol, last.Action, len(replay.Results))
			if err := writer.Append(now, kind, key, message); err != nil {
				return err
			}
		}
	}
	current, summary, err := shadowPerpsCurrent(config, replay, now)
	if err != nil {
		return err
	}
	return writer.UpdateCurrentSummary(now, current, &summary)
}

func shadowPerpsCompletedTradeLine(records []perpspaper.Record) string {
	entry := -1
	for index := len(records) - 1; index > 0; index-- {
		if records[index].Command.Type == perpspaper.OrderFilled && records[index-1].Command.Type == perpspaper.OrderPlaced {
			entry = index - 1
			break
		}
	}
	if entry < 1 {
		return "This completed trade: result unavailable"
	}
	before, beforeErr := perpspaper.Replay(records[:entry])
	after, afterErr := perpspaper.Replay(records)
	if beforeErr != nil || afterErr != nil || after.State().Position != nil {
		return "This completed trade: result unavailable"
	}
	return "This completed trade: " + formatPaperSignedChange(after.State().BalanceMicros-before.State().BalanceMicros, "USD")
}

func shadowPerpsCurrent(
	config shadowPerpsTapeConfig,
	replay perpspaper.TapeReplay,
	now time.Time,
) (string, paperstatus.CurrentSummary, error) {
	state := replay.State
	if !state.Initialized || state.FeesPaidMicros > math.MaxInt64 ||
		state.StartingCollateralMicros > math.MaxInt64 ||
		state.BalanceMicros < math.MinInt64+int64(state.StartingCollateralMicros) ||
		state.EquityMicros < math.MinInt64+int64(state.StartingCollateralMicros) {
		return "", paperstatus.CurrentSummary{}, errors.New("perps paper state cannot be represented in operator status")
	}
	projectedEquity := state.EquityMicros
	insolvent := projectedEquity < 0
	deficit := uint64(0)
	if insolvent {
		if state.Position != nil {
			return "", paperstatus.CurrentSummary{}, errors.New("negative perps paper equity still has an open position")
		}
		deficit = uint64(-(projectedEquity + 1)) + 1
		if deficit > math.MaxInt64 {
			return "", paperstatus.CurrentSummary{}, errors.New("perps paper deficit cannot be represented in operator status")
		}
		projectedEquity = 0
	}
	checks, signals, trades, turnover := uint64(len(replay.Results)), uint64(0), uint64(0), uint64(0)
	for _, result := range replay.Results {
		if result.Action != "flat" && result.Action != "marked" && result.Action != "liquidated" {
			signals++
		}
		if result.Action == "opened" || result.Action == "closed" {
			trades++
			notional, err := perpspaper.FilledNotionalMicros(config.Symbol, *result.Fill)
			if err != nil || turnover > math.MaxUint64-notional {
				return "", paperstatus.CurrentSummary{}, errors.New("perps paper turnover cannot be represented in operator status")
			}
			turnover += notional
		}
	}
	position := "No position open"
	positionDirection := "flat"
	if state.Position != nil {
		position = "Price-down position open"
		positionDirection = "short"
		if state.Position.Side == perpspaper.Long {
			position = "Price-up position open"
			positionDirection = "long"
		}
	}
	leverageBPS := min(uint32(10_000), config.VenueMaxLeverage*10_000)
	if len(replay.Results) > 0 {
		leverageBPS = min(replay.Results[len(replay.Results)-1].Decision.LeverageBPS, config.VenueMaxLeverage*10_000)
	}
	if state.Position != nil {
		leverageBPS = state.Position.LeverageBPS
	}
	current := fmt.Sprintf(
		"PAPER · %s perpetuals · %s\nTotal paper value now: %s\nResult this run: %s\nFunding: %s · Fees: %s",
		config.Symbol, position, formatPerpsUSD(projectedEquity),
		formatPerpsResult(state.EquityMicros-int64(state.StartingCollateralMicros)),
		formatPerpsUSD(state.FundingPnLMicros), formatPerpsUSD(int64(state.FeesPaidMicros)),
	)
	if insolvent {
		current += "\nSimulated deficit after liquidation: " + formatPerpsUSD(state.EquityMicros)
	}
	stateName, decisionReason := "watching", "watching"
	if insolvent {
		stateName, decisionReason = "paused", "risk_halt"
	}
	return current, paperstatus.CurrentSummary{
		Market: string(config.Symbol) + "-PERP", Instrument: "perpetual",
		RiskProfile: string(config.RiskArm), PositionDirection: positionDirection,
		LeverageBPS: leverageBPS, ValueUnit: "USD", Day: now.Format("2006-01-02"),
		TickSeconds: 60, OpeningEquityMicros: state.StartingCollateralMicros,
		EquityMicros: uint64(projectedEquity), DeficitMicros: deficit,
		HoldBenchmarkMicros: state.StartingCollateralMicros,
		AccountingTracked:   true,
		RealizedMicros:      state.BalanceMicros - int64(state.StartingCollateralMicros),
		UnrealizedMicros:    state.UnrealizedPnLMicros, FeesMicros: int64(state.FeesPaidMicros),
		FundingTracked: true, FundingMicros: state.FundingPnLMicros,
		TurnoverMicros: turnover, Checks: checks, Signals: signals, Trades: trades, PriceMicros: state.LastMarkPriceMicros,
		State: stateName, Strategy: shadowPerpsCurrentStrategy(config),
		DecisionSource: shadowPerpsDecisionSource(config), ProposalSource: shadowPerpsProposalSource(config),
		RunPlanSHA256:  config.PlanSHA256,
		DecisionReason: decisionReason, RiskHalted: insolvent,
	}, nil
}

func shadowPerpsCurrentStrategy(config shadowPerpsTapeConfig) string {
	if config.DecisionMode == shadowPerpsDecisionSelected {
		return string(config.Strategy)
	}
	return "fixed"
}

func shadowPerpsDecisionSource(config shadowPerpsTapeConfig) string {
	if !validLowerSHA256(config.PlanSHA256) {
		return ""
	}
	if config.DecisionMode == shadowPerpsDecisionSelected {
		return "selected_paper_plan"
	}
	return "legacy_fixed_policy"
}

func shadowPerpsProposalSource(config shadowPerpsTapeConfig) string {
	if !validLowerSHA256(config.PlanSHA256) {
		return ""
	}
	if config.DecisionMode == shadowPerpsDecisionSelected {
		return "deterministic_search"
	}
	return "built_in"
}

func shadowPerpsOutcomeResult(runResultMicros int64) string {
	switch {
	case runResultMicros > 0:
		return "gain"
	case runResultMicros < 0:
		return "loss"
	default:
		return "flat"
	}
}

func shadowPerpsOutcomeTapeSHA256(
	config shadowPerpsTapeConfig,
	sealedSHA256 string,
	walkForward *perpspaper.WalkForwardQualification,
) string {
	if config.DecisionMode != shadowPerpsDecisionSelected || !validLowerSHA256(sealedSHA256) ||
		walkForward == nil || len(walkForward.Tapes) < 2 ||
		walkForward.Tapes[len(walkForward.Tapes)-1].ContentSHA256 != sealedSHA256 {
		return ""
	}
	return sealedSHA256
}

func formatPerpsFillNotional(symbol perpspaper.Symbol, fill *perpspaper.Fill) string {
	if fill == nil {
		return "unavailable"
	}
	notional, err := perpspaper.FilledNotionalMicros(symbol, *fill)
	if err != nil || notional > math.MaxInt64 {
		return "unavailable"
	}
	return formatPerpsUSD(int64(notional))
}

func formatPerpsFillPrice(fill *perpspaper.Fill) string {
	if fill == nil || fill.AveragePriceMicros > math.MaxInt64 {
		return "unavailable"
	}
	return formatPerpsUSD(int64(fill.AveragePriceMicros))
}

func formatPerpsUSD(value int64) string {
	negative := value < 0
	magnitude := uint64(value)
	if negative {
		magnitude = uint64(-(value + 1)) + 1
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s$%d.%02d", sign, magnitude/1_000_000, magnitude%1_000_000/10_000)
}

func formatPerpsResult(value int64) string {
	if value > 0 {
		return "up " + formatPerpsUSD(value)
	}
	if value < 0 {
		return "down " + strings.TrimPrefix(formatPerpsUSD(value), "-")
	}
	return "unchanged"
}

func shadowPerpsMarketContext(
	metadata perpspaper.MetaAndAssetContexts,
	symbol perpspaper.Symbol,
) (perpspaper.AssetMeta, perpspaper.AssetContext, error) {
	if len(metadata.Universe) == 0 || len(metadata.Universe) != len(metadata.Contexts) {
		return perpspaper.AssetMeta{}, perpspaper.AssetContext{}, errors.New("Hyperliquid metadata and contexts do not align")
	}
	var asset perpspaper.AssetMeta
	var context perpspaper.AssetContext
	found := false
	for index := range metadata.Universe {
		if metadata.Universe[index].Name != symbol {
			continue
		}
		if found {
			return perpspaper.AssetMeta{}, perpspaper.AssetContext{}, errors.New("Hyperliquid returned duplicate paper market context")
		}
		asset, context, found = metadata.Universe[index], metadata.Contexts[index], true
	}
	if !found {
		return perpspaper.AssetMeta{}, perpspaper.AssetContext{}, errors.New("Hyperliquid returned no current paper market context")
	}
	return asset, context, nil
}

func readShadowPerpsTape(path string, config shadowPerpsTapeConfig) (shadowPerpsTape, perpspaper.TapeReplay, error) {
	want := shadowPerpsTape{Version: shadowPerpsTapeVersion, PaperOnly: true, AccountingModel: shadowPerpsModel, Config: config}
	raw, err := securefile.ReadPrivate(path, shadowPerpsMaxFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return want, perpspaper.TapeReplay{}, nil
	}
	if err != nil {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, fmt.Errorf("read paper tape: %w", err)
	}
	var stored shadowPerpsTape
	if err := strictjson.Decode(raw, &stored); err != nil {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, fmt.Errorf("decode paper tape: %w", err)
	}
	if stored.Version == 1 || stored.Version == 2 {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, errors.New("stored paper tape v1/v2 lacks causal sampled context timing and cannot be migrated; start a new experiment directory")
	}
	legacy := stored.Version == 3 && stored.AccountingModel == shadowPerpsLegacyModel &&
		stored.Config.DecisionMode == "" && stored.Config.Strategy == "" &&
		stored.Config.PlanSHA256 == "" && stored.Config.QualificationInputSHA256 == ""
	current := stored.Version == want.Version && stored.AccountingModel == want.AccountingModel &&
		(stored.Config.DecisionMode == shadowPerpsDecisionLegacy ||
			stored.Config.DecisionMode == shadowPerpsDecisionSelected) &&
		validLowerSHA256(stored.Config.PlanSHA256)
	if (!legacy && !current) || !stored.PaperOnly || stored.ExecutionEnabled ||
		stored.Config != config || len(stored.Frames) == 0 || len(stored.Frames) > shadowPerpsMaxFrames {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, errors.New("stored paper tape identity or bounds are invalid")
	}
	replay, err := replayShadowPerpsTape(stored.Config, stored.Frames)
	if err != nil {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, fmt.Errorf("verify stored paper tape: %w", err)
	}
	return stored, replay, nil
}

func replayShadowPerpsTape(config shadowPerpsTapeConfig, frames []perpspaper.TapeFrame) (perpspaper.TapeReplay, error) {
	switch config.DecisionMode {
	case "", shadowPerpsDecisionLegacy:
		return perpspaper.ReplayTape(config.replayConfig(), frames)
	case shadowPerpsDecisionSelected:
		return perpspaper.ReplaySelected(config.replayConfig(), frames, perpspaper.QualificationKey{
			RiskArm: config.RiskArm, Strategy: config.Strategy,
		})
	default:
		return perpspaper.TapeReplay{}, errors.New("perps paper tape has an unsupported decision mode")
	}
}

func (config shadowPerpsTapeConfig) replayConfig() perpspaper.ReplayConfig {
	return perpspaper.ReplayConfig{
		StartingCollateralMicros: config.StartingCollateralMicros,
		Symbol:                   config.Symbol, RiskArm: config.RiskArm,
		VenueMaxLeverage: config.VenueMaxLeverage, VenueSzDecimals: config.VenueSzDecimals,
	}
}

func (config shadowPerpsTapeConfig) qualificationConfig() perpspaper.QualificationConfig {
	return perpspaper.QualificationConfig{
		StartingCollateralMicros: config.StartingCollateralMicros,
		Symbol:                   config.Symbol, VenueMaxLeverage: config.VenueMaxLeverage,
		VenueSzDecimals: config.VenueSzDecimals,
	}
}

func buildShadowPerpsStatus(config shadowPerpsTapeConfig, replay perpspaper.TapeReplay, frames int, newFrame bool, now time.Time) shadowPerpsStatus {
	status := shadowPerpsStatus{
		Version: shadowPerpsStatusVersion, ObservedAt: now.UTC(), PaperOnly: true,
		AccountingModel: shadowPerpsModel, Environment: config.Environment,
		Symbol: config.Symbol, RiskArm: config.RiskArm, Frames: frames,
		DecisionMode: config.DecisionMode, Strategy: config.Strategy,
		PlanSHA256: config.PlanSHA256, QualificationInputSHA256: config.QualificationInputSHA256,
		NewFrame: newFrame, State: replay.State,
	}
	if len(replay.Results) > 0 {
		last := replay.Results[len(replay.Results)-1]
		status.LastAction = last.Action
		decision := last.Decision
		status.LastDecision = &decision
	}
	return status
}

func writeShadowPerpsJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(path, append(encoded, '\n'), shadowPerpsMaxFileBytes); err != nil {
		return fmt.Errorf("write perps paper state: %w", err)
	}
	return nil
}
