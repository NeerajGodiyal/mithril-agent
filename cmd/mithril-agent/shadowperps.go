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
	"strings"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/perpspaper"
)

const (
	shadowPerpsTapeVersion   uint32 = 3
	shadowPerpsStatusVersion uint32 = 3
	shadowPerpsMaxFrames            = 1_500
	shadowPerpsMaxFileBytes  int64  = 16 << 20
	shadowPerpsModel                = "hyperliquid_causal_sampled_context_stress_v3"
	shadowPerpsMaxClockSkew         = 5 * time.Second
	shadowPerpsMaxBookAge           = 30 * time.Second
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
	Version          uint32                 `json:"version"`
	ObservedAt       time.Time              `json:"observed_at"`
	PaperOnly        bool                   `json:"paper_only"`
	ExecutionEnabled bool                   `json:"execution_enabled"`
	AccountingModel  string                 `json:"accounting_model"`
	Environment      perpspaper.Environment `json:"environment"`
	Symbol           perpspaper.Symbol      `json:"symbol"`
	RiskArm          perpspaper.RiskArm     `json:"risk_arm"`
	Frames           int                    `json:"frames"`
	NewFrame         bool                   `json:"new_frame"`
	LastAction       string                 `json:"last_action,omitempty"`
	LastDecision     *perpspaper.Decision   `json:"last_decision,omitempty"`
	State            perpspaper.State       `json:"state"`
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
) error {
	flags := flag.NewFlagSet("shadow perps-paper-run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	environmentText := flags.String("environment", string(perpspaper.Mainnet), "Hyperliquid environment")
	symbolsText := flags.String("symbols", "SOL,BTC,ETH", "comma-separated paper markets")
	armText := flags.String("arm", string(perpspaper.Balanced), "paper risk arm")
	paperUSD := flags.String("paper-usd-per-market", "100", "simulated collateral per market")
	stateDir := flags.String("state-dir", "", "private paper state directory")
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
	if err := os.Mkdir(*stateDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create perps paper state directory: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(*stateDir); err != nil {
		return errors.New("perps paper state directory is not trusted")
	}
	info, err := os.Lstat(*stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("perps paper state directory must already be private mode 0700")
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
					runCtx, reader, *stateDir, environment, symbol, arm, collateral,
					assets[symbol], cycleNow, now,
				)
			}()
		}
		updates.Wait()
		for index, symbol := range symbols {
			status, err := statuses[index], errorsByMarket[index]
			if err != nil {
				if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
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
		if *once {
			return nil
		}
		timer := time.NewTimer(*cadence)
		select {
		case <-runCtx.Done():
			timer.Stop()
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return runCtx.Err()
		case <-timer.C:
		}
	}
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
	environment perpspaper.Environment,
	symbol perpspaper.Symbol,
	arm perpspaper.RiskArm,
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
			ctx, reader, stateDir, environment, symbol, arm, collateral, asset, now, clock,
		)
		return updateErr
	})
	return status, err
}

func updateShadowPerpsMarketUnlocked(
	ctx context.Context,
	reader shadowPerpsReader,
	stateDir string,
	environment perpspaper.Environment,
	symbol perpspaper.Symbol,
	arm perpspaper.RiskArm,
	collateral uint64,
	asset perpspaper.AssetMeta,
	now time.Time,
	clock func() time.Time,
) (shadowPerpsStatus, error) {
	config := shadowPerpsTapeConfig{
		Environment: environment, Symbol: symbol, RiskArm: arm,
		StartingCollateralMicros: collateral, VenueMaxLeverage: asset.MaxLeverage,
		VenueSzDecimals: asset.SzDecimals,
	}
	name := strings.ToLower(string(symbol))
	tapePath := filepath.Join(stateDir, name+"-tape.json")
	statusPath := filepath.Join(stateDir, name+"-status.json")
	tape, replay, err := readShadowPerpsTape(tapePath, config)
	if err != nil {
		return shadowPerpsStatus{}, err
	}
	closedEnd := now.Truncate(time.Minute).Add(-time.Millisecond)
	if closedEnd.UnixMilli() <= 0 {
		return shadowPerpsStatus{}, errors.New("system time has no completed one-minute candle")
	}
	if len(tape.Frames) > 0 {
		last := tape.Frames[len(tape.Frames)-1]
		lastClose := last.Candles[len(last.Candles)-1].CloseTime
		switch {
		case lastClose == closedEnd.UnixMilli():
			status := buildShadowPerpsStatus(config, replay, len(tape.Frames), false, now)
			return status, writeShadowPerpsJSON(statusPath, status)
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
		lastClose := tape.Frames[len(tape.Frames)-1].Candles[len(tape.Frames[len(tape.Frames)-1].Candles)-1].CloseTime
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
		replay, err = perpspaper.ReplayTape(config.replayConfig(), tape.Frames)
		if err != nil {
			return shadowPerpsStatus{}, err
		}
		if err := writeShadowPerpsJSON(tapePath, tape); err != nil {
			return shadowPerpsStatus{}, err
		}
	}
	status := buildShadowPerpsStatus(config, replay, len(tape.Frames), newFrame, now)
	if err := writeShadowPerpsJSON(statusPath, status); err != nil {
		return shadowPerpsStatus{}, err
	}
	return status, nil
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
	if stored.Version != want.Version || !stored.PaperOnly || stored.ExecutionEnabled || stored.AccountingModel != want.AccountingModel || stored.Config != config || len(stored.Frames) == 0 || len(stored.Frames) > shadowPerpsMaxFrames {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, errors.New("stored paper tape identity or bounds are invalid")
	}
	replay, err := perpspaper.ReplayTape(config.replayConfig(), stored.Frames)
	if err != nil {
		return shadowPerpsTape{}, perpspaper.TapeReplay{}, fmt.Errorf("verify stored paper tape: %w", err)
	}
	return stored, replay, nil
}

func (config shadowPerpsTapeConfig) replayConfig() perpspaper.ReplayConfig {
	return perpspaper.ReplayConfig{
		StartingCollateralMicros: config.StartingCollateralMicros,
		Symbol:                   config.Symbol, RiskArm: config.RiskArm,
		VenueMaxLeverage: config.VenueMaxLeverage, VenueSzDecimals: config.VenueSzDecimals,
	}
}

func buildShadowPerpsStatus(config shadowPerpsTapeConfig, replay perpspaper.TapeReplay, frames int, newFrame bool, now time.Time) shadowPerpsStatus {
	status := shadowPerpsStatus{
		Version: shadowPerpsStatusVersion, ObservedAt: now.UTC(), PaperOnly: true,
		AccountingModel: shadowPerpsModel, Environment: config.Environment,
		Symbol: config.Symbol, RiskArm: config.RiskArm, Frames: frames,
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
