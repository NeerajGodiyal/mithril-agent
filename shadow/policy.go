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
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// Cluster names a market to observe. Shadow mode is the one part of this system
// that is allowed to look at Mainnet, precisely because it cannot act on it.
const (
	Mainnet = "mainnet-beta"
	Devnet  = "devnet"
)

// Version is written into every record so a report can refuse to mix results
// produced by different accounting rules.
const Version = uint32(1)

// Policy is a complete description of a shadow run: what to watch, what rule to
// apply, and how to score the result.
type Policy struct {
	Version uint32 `json:"version"`
	Cluster string `json:"cluster"`

	// Trigger is the same rule type the real trader uses, evaluated by the same
	// pure function. If the two ever disagree it is a bug, not a difference of
	// configuration.
	Trigger pricetrigger.Policy `json:"trigger"`

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

	// FeeLamports is the transaction cost charged against every hypothetical
	// fill. Ignoring it is the most common way a paper result flatters itself.
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
}

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
