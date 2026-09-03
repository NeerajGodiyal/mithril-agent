package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	legacyKrakenUSDCIdentity = "7855a0286ed3a5165a64d30e0fa8c8528180f8be6207511787ac36bf7f45e4bd"
	legacyKrakenSOLIdentity  = "c4038b13f76a1706be5e26d366cad9ef14d80ab1443e58a4f3189c83dcafd315"
	legacyKrakenJUPIdentity  = "69f412f8b21e69b24abfcaf0b834f9772f14fa30d79db374a86cd2fff04d5741"
)

const shadowAllocationUsage = `Usage: mithril-agent shadow allocation --portfolio PATH
       --instruction PATH --out-dir PATH

Builds one immutable paper-only allocation generation from an existing
portfolio and a dashboard instruction. It writes new policy files and a
matching portfolio inside a new directory, but never activates them, restarts
a service, signs, submits, or touches a wallet.`

func runShadowAllocation(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow allocation", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	portfolioPath := flags.String("portfolio", "", "current private paper portfolio")
	instructionPath := flags.String("instruction", "", "operator paper allocation instruction")
	outDir := flags.String("out-dir", "", "new immutable allocation directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowAllocationUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !absoluteClean(*portfolioPath) ||
		!absoluteClean(*instructionPath) || !absoluteClean(*outDir) {
		return errors.New("shadow allocation requires clean absolute --portfolio, --instruction, and --out-dir paths")
	}
	instruction, instructionSHA256, err := paperdashboard.LoadInstruction(*instructionPath)
	if err != nil || instruction.Version != paperdashboard.InstructionVersion ||
		instruction.Market != "all" {
		return errors.New("shadow allocation instruction is not a current all-market paper request")
	}
	var current shadowPortfolioManifest
	if err := readStrictJSON(*portfolioPath, &current); err != nil {
		return errors.New("shadow allocation portfolio is invalid")
	}
	currentTotal, policies, err := validateShadowPortfolio(current)
	if err != nil || currentTotal == 0 {
		return errors.New("shadow allocation portfolio does not pass its capital checks")
	}
	next := shadowPortfolioManifest{
		Version: shadowPortfolioVersion, Status: "paper_portfolio", PaperOnly: true,
		InstructionSHA256:       instructionSHA256,
		TotalCapitalLimitMicros: instruction.PaperCapitalMicros,
		MaxSOLUSDMicros:         current.MaxSOLUSDMicros,
		Books:                   make([]shadowPortfolioBook, 0, len(current.Books)),
	}
	resized := make(map[string]shadow.Policy, len(current.Books))
	remaining := instruction.PaperCapitalMicros
	for index, book := range current.Books {
		currentCapital, err := shadowPortfolioCapital(policies[book.ID], current.MaxSOLUSDMicros)
		if err != nil {
			return errors.New("paper book capital is invalid")
		}
		target := proportionalPaperAllocation(
			instruction.PaperCapitalMicros, currentCapital, currentTotal,
		)
		if index == len(current.Books)-1 {
			target = remaining
		} else if target > remaining {
			return errors.New("shadow allocation is not representable")
		}
		remaining -= target
		policy, err := refreshPaperPolicySources(policies[book.ID])
		if err != nil {
			return fmt.Errorf("refresh paper book %s sources: %w", book.ID, err)
		}
		policy, err = resizePaperPolicy(policy, target, *instruction, current.MaxSOLUSDMicros)
		if err != nil {
			return fmt.Errorf("resize paper book %s: %w", book.ID, err)
		}
		resized[book.ID] = policy
	}
	if err := prepareShadowAllocationDirectory(*outDir); err != nil {
		return err
	}
	for _, book := range current.Books {
		policy := resized[book.ID]
		policyPath := filepath.Join(*outDir, book.ID+"-policy.json")
		if err := writeAllocationPolicy(policyPath, policy); err != nil {
			return err
		}
		fingerprint, err := policy.Fingerprint()
		if err != nil {
			return err
		}
		next.Books = append(next.Books, shadowPortfolioBook{
			ID: book.ID, Market: policy.Market, PolicyPath: policyPath,
			PolicySHA256: fingerprint,
		})
	}
	total, _, err := validateShadowPortfolio(next)
	if err != nil {
		return fmt.Errorf("validate resized paper portfolio: %w", err)
	}
	encoded, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	portfolioOut := filepath.Join(*outDir, "portfolio.json")
	if err := securefile.ReplacePrivate(portfolioOut, append(encoded, '\n'), maxInputBytes); err != nil {
		return errors.New("write resized paper portfolio")
	}
	return json.NewEncoder(output).Encode(struct {
		Status                  string `json:"status"`
		PaperOnly               bool   `json:"paper_only"`
		Authorized              bool   `json:"authorized"`
		InstructionSHA256       string `json:"instruction_sha256"`
		PortfolioPath           string `json:"portfolio_path"`
		Books                   int    `json:"books"`
		CapitalAtCeilingMicros  uint64 `json:"capital_at_ceiling_micros"`
		TotalCapitalLimitMicros uint64 `json:"total_capital_limit_micros"`
	}{
		Status: "paper_allocation_ready_not_active", PaperOnly: true,
		InstructionSHA256: instructionSHA256, PortfolioPath: portfolioOut,
		Books: len(next.Books), CapitalAtCeilingMicros: total,
		TotalCapitalLimitMicros: next.TotalCapitalLimitMicros,
	})
}

func refreshPaperPolicySources(policy shadow.Policy) (shadow.Policy, error) {
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	legacyMarket, legacyQuote, legacyNative := "", "", ""
	marketPrimary, marketSecondary := "", ""
	switch policy.Market {
	case shadow.MarketSOLUSDC:
		marketPrimary = pricesource.PythPushIdentitySHA256()
		marketSecondary = pricesource.KrakenSOLIdentitySHA256()
		legacyMarket = legacyKrakenSOLIdentity
		legacyQuote = legacyKrakenUSDCIdentity
		legacyNative = legacyKrakenSOLIdentity
	case shadow.MarketJUPUSDC:
		marketPrimary = pricesource.PythPushJUPIdentitySHA256()
		marketSecondary = pricesource.KrakenJUPIdentitySHA256()
		legacyMarket = legacyKrakenJUPIdentity
		legacyQuote = legacyKrakenUSDCIdentity
		legacyNative = legacyKrakenSOLIdentity
	default:
		candidate, ok := marketadmission.Lookup(policy.Market)
		if !ok || policy.Version != shadow.AdmittedVersion {
			return shadow.Policy{}, errors.New("paper policy market sources are unsupported")
		}
		var err error
		marketPrimary, err = candidate.Pyth.IdentitySHA256()
		if err != nil {
			return shadow.Policy{}, err
		}
		marketSecondary, err = candidate.Kraken.IdentitySHA256()
		if err != nil {
			return shadow.Policy{}, err
		}
	}
	if policy.Trigger.PrimarySourceSHA256 != marketPrimary ||
		!currentOrLegacySource(policy.Trigger.SecondarySourceSHA256, marketSecondary, legacyMarket) {
		return shadow.Policy{}, errors.New("paper policy market sources are not the pinned pair")
	}
	policy.Trigger.SecondarySourceSHA256 = marketSecondary
	if policy.ReturnTrigger != nil {
		policy.ReturnTrigger.PrimarySourceSHA256 = marketPrimary
		policy.ReturnTrigger.SecondarySourceSHA256 = marketSecondary
	}
	if policy.QuotePeg == nil ||
		policy.QuotePeg.PrimarySourceSHA256 != pricesource.PythPushUSDCIdentitySHA256() ||
		!currentOrLegacySource(
			policy.QuotePeg.SecondarySourceSHA256,
			pricesource.KrakenIdentitySHA256(), legacyQuote,
		) {
		return shadow.Policy{}, errors.New("paper policy quote sources are not the pinned pair")
	}
	policy.QuotePeg.SecondarySourceSHA256 = pricesource.KrakenIdentitySHA256()
	if policy.NativeFeePrice != nil {
		if policy.NativeFeePrice.PrimarySourceSHA256 != pricesource.PythPushIdentitySHA256() ||
			!currentOrLegacySource(
				policy.NativeFeePrice.SecondarySourceSHA256,
				pricesource.KrakenSOLIdentitySHA256(), legacyNative,
			) {
			return shadow.Policy{}, errors.New("paper policy native fee sources are not the pinned pair")
		}
		policy.NativeFeePrice.SecondarySourceSHA256 = pricesource.KrakenSOLIdentitySHA256()
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

func currentOrLegacySource(actual, current, legacy string) bool {
	return actual == current || legacy != "" && actual == legacy
}

func prepareShadowAllocationDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create shadow allocation output directory: %w", err)
	}
	if err := validatePrivateDirectory(path); err != nil {
		return errors.New("prepared shadow allocation output directory is not private")
	}
	info, err := os.Lstat(path)
	if err != nil || !fileowner.CurrentOwned(info) {
		return errors.New("prepared shadow allocation output directory has the wrong owner")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return errors.New("inspect prepared shadow allocation output directory")
	}
	if len(entries) != 0 {
		return errors.New("prepared shadow allocation output directory is not empty")
	}
	return nil
}

func resizePaperPolicy(
	policy shadow.Policy, target uint64, instruction paperdashboard.Instruction, maxSOL uint64,
) (shadow.Policy, error) {
	if policy.Cluster != shadow.Mainnet || policy.Adaptive == nil ||
		policy.StartingFeeReserveLamports == 0 {
		return shadow.Policy{}, errors.New("paper policy cannot apply the requested safeguards")
	}
	adaptive := *policy.Adaptive
	policy.TickSeconds = instruction.CadenceSeconds
	adaptive.MaxDrawdownBPS = instruction.MaxDrawdownBPS
	adaptive.MaxObservationGapSeconds = instruction.CadenceSeconds * 2
	policy.Adaptive = &adaptive
	reserveValue, ok := paperAllocationValue(policy.StartingFeeReserveLamports, maxSOL, 9)
	if !ok || target <= reserveValue {
		return shadow.Policy{}, errors.New("paper allocation cannot fund its native reserve")
	}
	tradeCapital := target - reserveValue
	maximum := min(instruction.MaximumOrderMicros, tradeCapital)
	if maximum < instruction.MinimumOrderMicros {
		return shadow.Policy{}, errors.New("paper allocation is smaller than the minimum order")
	}
	policy.MinimumOrderValueMicros = instruction.MinimumOrderMicros
	policy.MaximumOrderValueMicros = maximum
	if policy.IsSell() {
		if policy.Market != shadow.MarketSOLUSDC {
			return shadow.Policy{}, errors.New("only the SOL paper book may start from base inventory")
		}
		maximumUnits, ok := paperAllocationUnits(maximum, maxSOL, policy.InputDecimals)
		if !ok {
			return shadow.Policy{}, errors.New("paper SOL order is not representable")
		}
		totalNativeUnits, ok := paperAllocationUnits(target, maxSOL, policy.InputDecimals)
		if !ok || totalNativeUnits <= policy.StartingFeeReserveLamports {
			return shadow.Policy{}, errors.New("paper SOL allocation cannot fund its native reserve")
		}
		units := min(maximumUnits, totalNativeUnits-policy.StartingFeeReserveLamports)
		if !ok || units == 0 {
			return shadow.Policy{}, errors.New("paper SOL order is not representable")
		}
		orderValue, ok := paperAllocationValue(units, maxSOL, policy.InputDecimals)
		if !ok || orderValue < instruction.MinimumOrderMicros {
			return shadow.Policy{}, errors.New("paper SOL order rounds below the minimum")
		}
		nativeUnits := units + policy.StartingFeeReserveLamports
		if nativeUnits < units {
			return shadow.Policy{}, errors.New("paper SOL allocation is not representable")
		}
		nativeValue, ok := paperAllocationValue(nativeUnits, maxSOL, policy.InputDecimals)
		if !ok || nativeValue > target {
			return shadow.Policy{}, errors.New("paper SOL allocation exceeds its target")
		}
		idle, ok := paperAllocationUnits(target-nativeValue, 1_000_000, policy.OutputDecimals)
		if !ok {
			return shadow.Policy{}, errors.New("paper SOL cash balance is not representable")
		}
		policy.InputAmount, policy.StartingInputUnits, policy.StartingOutputUnits = units, units, idle
	} else {
		if policy.InputDecimals != 6 {
			return shadow.Policy{}, errors.New("paper quote balance is not denominated in USDC micros")
		}
		if policy.Version == shadow.AdmittedVersion {
			if policy.InputAmount < instruction.MinimumOrderMicros || policy.InputAmount > maximum {
				return shadow.Policy{}, errors.New("admitted market order size is outside the requested limits")
			}
		} else {
			policy.InputAmount = maximum
		}
		policy.StartingInputUnits = tradeCapital
		policy.StartingOutputUnits = 0
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

func proportionalPaperAllocation(total, part, whole uint64) uint64 {
	value := new(big.Int).SetUint64(total)
	value.Mul(value, new(big.Int).SetUint64(part))
	value.Div(value, new(big.Int).SetUint64(whole))
	return value.Uint64()
}

func paperAllocationValue(units, price uint64, decimals uint8) (uint64, bool) {
	if price == 0 {
		return 0, false
	}
	value := new(big.Int).SetUint64(units)
	value.Mul(value, new(big.Int).SetUint64(price))
	value.Div(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	if !value.IsUint64() {
		return 0, false
	}
	return value.Uint64(), true
}

func paperAllocationUnits(value, price uint64, decimals uint8) (uint64, bool) {
	if price == 0 {
		return 0, false
	}
	units := new(big.Int).SetUint64(value)
	units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	units.Div(units, new(big.Int).SetUint64(price))
	if !units.IsUint64() {
		return 0, false
	}
	return units.Uint64(), true
}

func writeAllocationPolicy(path string, policy shadow.Policy) error {
	encoded, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(path, append(encoded, '\n'), maxInputBytes); err != nil {
		return errors.New("write resized paper policy")
	}
	return nil
}
