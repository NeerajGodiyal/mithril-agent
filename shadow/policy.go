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
const Version = uint32(4)

const (
	QuoteJupiter = "jupiter"
	QuoteOrca    = "orca"

	wrappedSOLMint  = "So11111111111111111111111111111111111111112"
	mainnetUSDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

// JournalVersion identifies the run header written before the first tick. The
// original journal format wrote a ledger as shadow.opened and could not safely
// resume, so it is deliberately incompatible with this restart-safe format.
const JournalVersion = uint32(2)

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
	input, output := mainnetUSDCMint, wrappedSOLMint
	if sell {
		input, output = wrappedSOLMint, mainnetUSDCMint
	}
	return QuoteRoute{Provider: QuoteJupiter, InputMint: input, OutputMint: output}
}

// Policy is a complete description of a shadow run: what to watch, what rule to
// apply, and how to score the result.
type Policy struct {
	Version uint32 `json:"version"`
	Cluster string `json:"cluster"`

	// Trigger is the same rule type the real trader uses, evaluated by the same
	// pure function. If the two ever disagree it is a bug, not a difference of
	// configuration.
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

	// InputAmount is the size of the hypothetical trade in the input asset's
	// base units, and InputDecimals scales it for display and for price maths.
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
	if p.Version != Version {
		return errors.New("shadow policy version is not supported")
	}
	if p.Cluster != Mainnet && p.Cluster != Devnet {
		return errors.New("shadow policy cluster must be mainnet-beta or devnet")
	}
	if err := p.Trigger.Validate(); err != nil {
		return err
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
	if p.StartingInputUnits == 0 && p.StartingOutputUnits == 0 {
		return errors.New("shadow policy needs an opening inventory to measure against")
	}
	if p.StartingInputUnits < p.InputAmount {
		return errors.New("shadow policy opening input inventory is smaller than one trade")
	}
	feeReserve := p.FeeLamports
	if p.RoundTrip() {
		if p.FeeLamports > math.MaxUint64/2 {
			return errors.New("shadow policy round-trip fees are too large")
		}
		feeReserve *= 2
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
	want := MainnetQuoteRoute(p.IsSell())
	wantInputDecimals, wantOutputDecimals := uint8(6), uint8(9)
	if p.IsSell() {
		wantInputDecimals, wantOutputDecimals = 9, 6
	}
	if p.QuoteRoute != want ||
		p.InputDecimals != wantInputDecimals || p.OutputDecimals != wantOutputDecimals {
		return errors.New("Mainnet shadow quote route must match the SOL/USDC rule direction")
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
