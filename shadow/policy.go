// Package shadow runs the trading rule against a live market and records what
// it would have done, without ever being able to do it.
//
// The safety property is structural, not procedural. A shadow policy has no
// field that can name a key, a signer, a submitter, or a spending limit,
// because those concepts do not exist in this package. There is no code path
// from here to a signature, so "shadow mode cannot trade" is not a rule
// somebody has to keep enforcing — it is a thing that cannot be expressed.
//
// That is what makes it safe to point at Mainnet. Everything here reads.
package shadow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/base58"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// Cluster names a market to observe. Shadow mode may look at Mainnet precisely
// because it cannot act on it.
const (
	Mainnet = "mainnet-beta"
	Devnet  = "devnet"
)

// Version is written into every record so a report can refuse to mix results
// produced by different accounting rules.
const (
	LegacyVersion    = uint32(4)
	NativeFeeVersion = uint32(5)
	Version          = uint32(6)
)

const (
	QuoteJupiter = "jupiter"
	QuoteOrca    = "orca"

	MarketSOLUSDC = "SOL/USDC"
	MarketJUPUSDC = "JUP/USDC"

	wrappedSOLMint  = "So11111111111111111111111111111111111111112"
	mainnetUSDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	mainnetJUPMint  = "JUPyiwrYJFskUPiHa7hkeR8VUtAeFoSYbKedZNsDvCN"
)

// JournalVersion identifies the run header written before the first tick. The
// original journal format wrote a ledger as shadow.opened and could not safely
// resume, so it is deliberately incompatible with this restart-safe format.
const (
	LegacyJournalVersion    = uint32(2)
	NativeFeeJournalVersion = uint32(3)
	JournalVersion          = uint32(4)
)

func JournalVersionFor(policy Policy) uint32 {
	if policy.Version == LegacyVersion {
		return LegacyJournalVersion
	}
	if policy.Version == NativeFeeVersion {
		return NativeFeeJournalVersion
	}
	return JournalVersion
}

// Opening binds one daily journal to the exact policy that produced it.
// Without this header a restart could silently continue the same evidence file
// under different thresholds, sizing, sources, or accounting rules.
type Opening struct {
	Version      uint32 `json:"version"`
	PolicySHA256 string `json:"policy_sha256"`
}

// QuoteRoute binds the executable-price evidence to the venue and assets that
// produced it. These values used to be process flags, so one journal could be
// resumed with a different pool or token pair while retaining the same policy
// fingerprint. A report from mixed routes is not strategy evidence.
type QuoteRoute struct {
	Provider   string `json:"provider"`
	Pool       string `json:"pool,omitempty"`
	InputMint  string `json:"input_mint"`
	OutputMint string `json:"output_mint"`
}

// MainnetQuoteRoute returns the fixed Jupiter SOL/USDC route for one direction.
func MainnetQuoteRoute(sell bool) QuoteRoute {
	return MainnetMarketQuoteRoute(MarketSOLUSDC, sell)
}

// MainnetMarketQuoteRoute returns a pinned Jupiter route for a supported paper
// market. An empty route is never valid and makes an unknown market fail closed.
func MainnetMarketQuoteRoute(market string, sell bool) QuoteRoute {
	baseMint := ""
	switch market {
	case MarketSOLUSDC:
		baseMint = wrappedSOLMint
	case MarketJUPUSDC:
		baseMint = mainnetJUPMint
	default:
		return QuoteRoute{}
	}
	input, output := mainnetUSDCMint, wrappedSOLMint
	output = baseMint
	if sell {
		input, output = baseMint, mainnetUSDCMint
	}
	return QuoteRoute{Provider: QuoteJupiter, InputMint: input, OutputMint: output}
}

// Policy is a complete description of a shadow run: what to watch, what rule to
// apply, and how to score the result.
type Policy struct {
	Version uint32 `json:"version"`
	Cluster string `json:"cluster"`
	Market  string `json:"market,omitempty"`

	// Adaptive replaces absolute entry prices with a rolling, price-relative
	// decision model. Trigger and ReturnTrigger still bind the independently
	// validated feed and the two inventory directions; their thresholds are not
	// used when this field is present.
	Adaptive *AdaptivePolicy `json:"adaptive,omitempty"`

	// Trigger is the same rule type the real trader uses. A fixed policy applies
	// its comparison; an adaptive policy uses it only to bind and validate the
	// price feed, sources, and first inventory direction.
	Trigger pricetrigger.Policy `json:"trigger"`

	// QuotePeg is mandatory on Mainnet because the accounting below labels its
	// USDC proceeds as USD. Two independent USDC/USD confidence intervals must
	// stay inside this band on every observable tick. Devnet's test token is not
	// USDC, so Devnet remains an execution-mechanics proxy and must not claim
	// this evidence.
	QuotePeg *pricetrigger.BandPolicy `json:"quote_peg,omitempty"`

	// QuoteRoute is part of the policy fingerprint. Runtime flags may repeat
	// these values for compatibility, but can never replace them.
	QuoteRoute QuoteRoute `json:"quote_route"`

	// Observe is the address whose route is quoted. It is watch-only: quoting
	// requires an address but never a key, and nothing here can spend from it.
	Observe string `json:"observe"`

	// InputAmount is the initial hypothetical lot in the input asset's base
	// units. Later round-trip legs use simulated proceeds; InputDecimals scales
	// amounts for display and price maths.
	InputAmount   uint64 `json:"input_amount"`
	InputDecimals uint8  `json:"input_decimals"`
	// OutputDecimals scales the quoted output the same way.
	OutputDecimals uint8 `json:"output_decimals"`

	// SlippageBPS bounds the hypothetical fill exactly as it would bound a real
	// one, so a shadow result cannot look better than the real rule permits.
	SlippageBPS uint16 `json:"slippage_bps"`

	// FeeLamports is the transaction cost charged against every modeled
	// submitted attempt, including a runtime slippage refusal.
	FeeLamports uint64 `json:"fee_lamports"`

	// TickSeconds is how often the market is observed.
	TickSeconds uint64 `json:"tick_seconds"`

	// SettleSeconds is the delay between deciding and scoring. A decision is
	// never scored against the price that produced it; it is scored against a
	// price observed strictly later, which is the only honest way to account
	// for the fact that a real order takes time to land.
	SettleSeconds uint64 `json:"settle_seconds"`

	// StartingInputUnits and StartingOutputUnits are the notional opening
	// inventory, which also fixes the hold-the-asset benchmark the strategy is
	// measured against.
	StartingInputUnits  uint64 `json:"starting_input_units"`
	StartingOutputUnits uint64 `json:"starting_output_units"`
	// StartingFeeReserveLamports keeps liquid native transaction costs separate
	// from traded inventory, including setup rent until it becomes locked. Zero
	// retains the original SOL/USDC accounting for old policies and journals.
	StartingFeeReserveLamports uint64 `json:"starting_fee_reserve_lamports,omitempty"`
	// OneTimeSetupRentLamports is conservative native capital locked by the
	// first successful Mainnet Jupiter route setup. It remains part of equity,
	// not a fee.
	OneTimeSetupRentLamports uint64 `json:"one_time_setup_rent_lamports,omitempty"`

	// NativeFeePrice binds independent SOL/USD evidence when the traded base is
	// not SOL. The ceiling keeps the adaptive cost hurdle conservative before a
	// live native price exists.
	NativeFeePrice              *pricetrigger.Policy `json:"native_fee_price,omitempty"`
	NativeFeePriceCeilingMicros uint64               `json:"native_fee_price_ceiling_micros,omitempty"`

	// ReturnTrigger makes the run a ROUND TRIP. With it set, Trigger is the
	// rule for the leg that spends the starting inventory and ReturnTrigger is
	// the rule for buying back, both scored on one set of books — which is the
	// only way to answer "does buying low and selling high actually make money
	// here", because a one-directional run cannot see the round trip's cost.
	//
	// Direction starts with Trigger and changes only after that leg fills. A
	// refusal keeps the same leg armed, so the two rules can never both fire at
	// once and spare opening inventory cannot accidentally cause a second sell.
	ReturnTrigger *pricetrigger.Policy `json:"return_trigger,omitempty"`
}

// Fingerprint returns a deterministic identity for every decision-affecting
// policy field. Policy contains no maps, so encoding/json's struct order is a
// stable hash input.
func (p Policy) Fingerprint() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// RoundTrip reports whether this policy scores both legs on one book.
func (p Policy) RoundTrip() bool { return p.ReturnTrigger != nil }

const (
	minTickSeconds   = uint64(5)
	maxTickSeconds   = uint64(3600)
	maxSettleSeconds = uint64(600)
	maxDecimals      = uint8(18)
)

// Validate rejects a policy that could produce a misleading result, not just
// one that could crash. A shadow run that silently scores itself with no fee,
// no slippage bound, or no settlement delay is worse than no shadow run at all,
// because it produces a number somebody will believe.
func (p Policy) Validate() error {
	if p.Version != Version && p.Version != NativeFeeVersion && p.Version != LegacyVersion {
		return errors.New("shadow policy version is not supported")
	}
	if p.Version == LegacyVersion &&
		(p.Market != "" || p.StartingFeeReserveLamports != 0 || p.OneTimeSetupRentLamports != 0 ||
			p.NativeFeePrice != nil || p.NativeFeePriceCeilingMicros != 0) {
		return errors.New("legacy shadow policy cannot use v5 market accounting")
	}
	if p.Version == NativeFeeVersion && p.OneTimeSetupRentLamports != 0 {
		return errors.New("v5 shadow policy cannot use v6 setup-rent accounting")
	}
	if p.Cluster != Mainnet && p.Cluster != Devnet {
		return errors.New("shadow policy cluster must be mainnet-beta or devnet")
	}
	if err := p.Trigger.Validate(); err != nil {
		return err
	}
	market := p.Market
	if p.Version == LegacyVersion && market == "" {
		market = MarketSOLUSDC
	}
	if p.Cluster == Mainnet && market != MarketSOLUSDC && market != MarketJUPUSDC {
		return errors.New("mainnet shadow policy market is unsupported")
	}
	if market == MarketJUPUSDC {
		if p.Trigger.Version != pricetrigger.MultiFeedVersion ||
			p.Trigger.Feed != pricetrigger.FeedJUPUSD || p.NativeFeePrice == nil ||
			p.StartingFeeReserveLamports == 0 {
			return errors.New("JUP/USDC paper policy needs JUP/USD and native SOL/USD evidence")
		}
		if err := p.NativeFeePrice.Validate(); err != nil ||
			p.NativeFeePrice.Feed != pricetrigger.FeedSOLUSD ||
			p.NativeFeePrice.Direction != pricetrigger.BuyAtOrBelow ||
			p.NativeFeePrice.PrimarySourceSHA256 == p.Trigger.PrimarySourceSHA256 ||
			p.NativeFeePrice.PrimarySourceSHA256 == p.Trigger.SecondarySourceSHA256 ||
			p.NativeFeePrice.SecondarySourceSHA256 == p.Trigger.PrimarySourceSHA256 ||
			p.NativeFeePrice.SecondarySourceSHA256 == p.Trigger.SecondarySourceSHA256 {
			return errors.New("JUP/USDC native fee price policy is invalid")
		}
		if p.NativeFeePriceCeilingMicros < 100_000_000 ||
			p.NativeFeePriceCeilingMicros > pricetrigger.MaxPriceMicros {
			return errors.New("JUP/USDC native fee price ceiling is invalid")
		}
		if p.Version == Version && p.OneTimeSetupRentLamports == 0 {
			return errors.New("JUP/USDC paper policy needs a conservative setup-rent reserve")
		}
	} else if p.NativeFeePrice != nil || p.NativeFeePriceCeilingMicros != 0 {
		return errors.New("SOL/USDC paper policy does not need separate native price evidence")
	} else if p.OneTimeSetupRentLamports != 0 &&
		(p.Version != Version || p.Cluster != Mainnet || market != MarketSOLUSDC ||
			p.StartingFeeReserveLamports == 0) {
		return errors.New("token setup rent is supported only by current Mainnet paper policies")
	}
	if p.Adaptive != nil {
		if err := p.Adaptive.Validate(); err != nil {
			return err
		}
		var costFloor uint32
		var err error
		if market == MarketJUPUSDC {
			if p.IsSell() {
				return errors.New("JUP/USDC adaptive paper policy must start from its USDC buy leg")
			}
			costFloor, err = adaptiveQuoteSignalCostFloorBPS(
				p.Adaptive.Version, p.SlippageBPS, p.FeeLamports,
				p.NativeFeePriceCeilingMicros, p.InputAmount, p.InputDecimals,
			)
		} else {
			costFloor, err = adaptiveSignalCostFloorBPS(
				p.Adaptive.Version, p.SlippageBPS, p.FeeLamports, p.InputAmount,
			)
		}
		if err != nil {
			return err
		}
		if uint32(p.Adaptive.MinimumSignalBPS) < costFloor {
			return errors.New("adaptive minimum signal must cover round-trip fees and its versioned safety margin")
		}
		if p.Adaptive.MaxObservationGapSeconds < p.TickSeconds {
			return errors.New("adaptive observation gap must allow at least one policy tick")
		}
		if p.ReturnTrigger == nil {
			return errors.New("adaptive shadow policy needs both inventory directions")
		}
	}
	if p.Cluster == Mainnet {
		if p.QuotePeg == nil {
			return errors.New("mainnet shadow policy needs independent USDC/USD evidence")
		}
		if err := p.QuotePeg.Validate(); err != nil {
			return err
		}
	} else if p.QuotePeg != nil {
		return errors.New("devnet shadow policy cannot treat its test quote token as USDC")
	}
	if err := p.validateQuoteRoute(); err != nil {
		return err
	}
	if p.ReturnTrigger != nil {
		if err := p.ReturnTrigger.Validate(); err != nil {
			return err
		}
		// Opposite directions, or it is not a round trip — two sell rules on one
		// book would just be the same leg twice and would report a profit the
		// inventory could never have produced.
		if p.ReturnTrigger.Direction == p.Trigger.Direction {
			return errors.New("a round trip needs opposite directions on its two triggers")
		}
		// Same feed and same sources: scoring two legs against two different
		// price feeds would make the round trip's profit an artefact of the
		// disagreement between them.
		if p.ReturnTrigger.Feed != p.Trigger.Feed ||
			p.ReturnTrigger.PrimarySourceSHA256 != p.Trigger.PrimarySourceSHA256 ||
			p.ReturnTrigger.SecondarySourceSHA256 != p.Trigger.SecondarySourceSHA256 {
			return errors.New("a round trip's two triggers must read the same price feed")
		}
		// Buy strictly below sell, or one price reading satisfies both and the
		// books would churn on every tick for a guaranteed loss.
		sell, buy := p.Trigger, *p.ReturnTrigger
		if !p.IsSell() {
			sell, buy = buy, sell
		}
		if buy.ThresholdMicros >= sell.ThresholdMicros {
			return errors.New("a round trip must buy back below the price it sells at")
		}
	}
	if p.Observe == "" {
		return errors.New("shadow policy needs an address to quote against")
	}
	if p.InputAmount == 0 {
		return errors.New("shadow policy input amount must be positive")
	}
	if p.InputDecimals > maxDecimals || p.OutputDecimals > maxDecimals {
		return errors.New("shadow policy decimals are out of range")
	}
	if p.SlippageBPS == 0 || p.SlippageBPS > 500 {
		return errors.New("shadow policy slippage must be between 1 and 500 basis points")
	}
	if p.FeeLamports == 0 {
		return errors.New("shadow policy must charge a transaction fee")
	}
	if p.TickSeconds < minTickSeconds || p.TickSeconds > maxTickSeconds {
		return errors.New("shadow policy tick interval is out of range")
	}
	if p.SettleSeconds == 0 || p.SettleSeconds > maxSettleSeconds {
		return errors.New("shadow policy must settle a decision strictly later than it was made")
	}
	if p.Adaptive != nil &&
		(uint64(p.Adaptive.SlowWindow)-1)*p.TickSeconds+p.SettleSeconds >= 86_400 {
		return errors.New("adaptive warmup and settlement must fit inside one UTC evaluation day")
	}
	if p.StartingInputUnits == 0 && p.StartingOutputUnits == 0 {
		return errors.New("shadow policy needs an opening inventory to measure against")
	}
	if p.StartingInputUnits < p.InputAmount {
		return errors.New("shadow policy opening input inventory is smaller than one trade")
	}
	feeReserve := p.FeeLamports
	if p.RoundTrip() || p.Version == Version && market == MarketJUPUSDC {
		if p.FeeLamports > math.MaxUint64/2 {
			return errors.New("shadow policy required fees are too large")
		}
		feeReserve *= 2
	}
	if p.StartingFeeReserveLamports != 0 {
		if p.Cluster != Mainnet ||
			p.QuoteRoute != MainnetMarketQuoteRoute(market, p.IsSell()) {
			return errors.New("separate paper fee reserve does not match its Mainnet market")
		}
		if p.StartingFeeReserveLamports < feeReserve {
			return errors.New("shadow policy native fee reserve does not fund its transaction fees")
		}
		if p.OneTimeSetupRentLamports > math.MaxUint64-feeReserve ||
			p.StartingFeeReserveLamports < p.OneTimeSetupRentLamports+feeReserve {
			return errors.New("shadow policy native reserve does not fund setup rent and transaction fees")
		}
		if market == MarketSOLUSDC {
			baseUnits := p.StartingOutputUnits
			if p.IsSell() {
				baseUnits = p.StartingInputUnits
			}
			if baseUnits > math.MaxUint64-p.StartingFeeReserveLamports {
				return errors.New("shadow policy native inventory and fee reserve are too large")
			}
		}
		return nil
	}
	if p.IsSell() && p.StartingInputUnits-p.InputAmount < feeReserve {
		return errors.New("shadow policy opening SOL inventory does not leave its transaction fee")
	}
	if !p.IsSell() && p.StartingOutputUnits < p.FeeLamports {
		return errors.New("shadow policy opening SOL inventory does not fund its transaction fee")
	}
	return nil
}

func (p Policy) validateQuoteRoute() error {
	for _, address := range []string{p.QuoteRoute.InputMint, p.QuoteRoute.OutputMint} {
		if _, err := base58.Decode32(address); err != nil {
			return errors.New("shadow quote route contains an invalid mint")
		}
	}
	if p.QuoteRoute.InputMint == p.QuoteRoute.OutputMint {
		return errors.New("shadow quote route mints must differ")
	}
	switch p.QuoteRoute.Provider {
	case QuoteJupiter:
		if p.Cluster != Mainnet || p.QuoteRoute.Pool != "" {
			return errors.New("Jupiter shadow quotes require Mainnet and no pool")
		}
	case QuoteOrca:
		if p.Cluster != Devnet {
			return errors.New("Orca shadow quotes are supported only for Devnet evidence")
		}
		if _, err := base58.Decode32(p.QuoteRoute.Pool); err != nil {
			return errors.New("Orca shadow quote route contains an invalid pool")
		}
	default:
		return errors.New("shadow quote provider must be jupiter or orca")
	}
	if p.Cluster != Mainnet {
		return nil
	}
	market := p.Market
	if p.Version == LegacyVersion && market == "" {
		market = MarketSOLUSDC
	}
	want := MainnetMarketQuoteRoute(market, p.IsSell())
	wantInputDecimals, wantOutputDecimals := uint8(6), uint8(6)
	if market == MarketSOLUSDC {
		wantOutputDecimals = 9
		if p.IsSell() {
			wantInputDecimals, wantOutputDecimals = 9, 6
		}
	}
	if p.QuoteRoute != want ||
		p.InputDecimals != wantInputDecimals || p.OutputDecimals != wantOutputDecimals {
		return errors.New("Mainnet shadow quote route must match its market and rule direction")
	}
	return nil
}

func (p Policy) Tick() time.Duration {
	return time.Duration(p.TickSeconds) * time.Second
}

func (p Policy) Settle() time.Duration {
	return time.Duration(p.SettleSeconds) * time.Second
}

// IsSell reports the direction in the terms the accounting uses: a sell spends
// the input asset and receives the output asset.
func (p Policy) IsSell() bool {
	return p.Trigger.Direction == pricetrigger.SellAtOrAbove
}
