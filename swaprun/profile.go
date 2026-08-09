package swaprun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/bits"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	fingerprintDomain    = "mithril-agent/orca-devnet-swap-v1/profile"
	buyFingerprintDomain = "mithril-agent/orca-devnet-buy-v2/profile"
)

type Profile struct {
	Name                         string                `json:"name"`
	Version                      uint32                `json:"version"`
	Cluster                      string                `json:"cluster"`
	Route                        orcaswap.Policy       `json:"route,omitzero"`
	BuyRoute                     *orcaswap.BuyPolicyV2 `json:"buy_route,omitempty"`
	InputLamports                uint64                `json:"input_lamports,omitzero"`
	InputTokenAmount             uint64                `json:"input_token_amount,omitempty"`
	SlippageBPS                  uint16                `json:"slippage_bps"`
	ReserveLamports              uint64                `json:"reserve_lamports"`
	MaxFeeLamports               uint64                `json:"max_fee_lamports"`
	DailyDebitCapLamports        uint64                `json:"daily_debit_cap_lamports,omitzero"`
	DailyInputTokenCap           uint64                `json:"daily_input_token_cap,omitempty"`
	DailyNativeFeeCapLamports    uint64                `json:"daily_native_fee_cap_lamports,omitempty"`
	ScheduleWindowSeconds        uint64                `json:"schedule_window_seconds"`
	ScheduleAnchorUnix           int64                 `json:"schedule_anchor_unix"`
	MaxClockUncertaintyMillis    uint64                `json:"max_clock_uncertainty_millis"`
	MaxObservationAgeSeconds     uint64                `json:"max_observation_age_seconds"`
	MinHealthyObservationSeconds uint64                `json:"min_healthy_observation_seconds"`
	MinHealthySlotAdvance        uint64                `json:"min_healthy_slot_advance"`
	MaxBlockHeightWindow         uint64                `json:"max_block_height_window"`
	MaxReconciliationSeconds     uint64                `json:"max_reconciliation_seconds"`
	PriceTrigger                 *pricetrigger.Policy  `json:"price_trigger,omitempty"`
}

func (p Profile) Validate() error {
	if p.isBuy() {
		return p.validateBuy()
	}
	if p.Name != orcaswap.ProfileName || p.Version != orcaswap.ProfileVersion ||
		p.Cluster != "devnet" {
		return errors.New("swap profile must be Orca Devnet v1")
	}
	if p.BuyRoute != nil || p.InputTokenAmount != 0 || p.DailyInputTokenCap != 0 ||
		p.DailyNativeFeeCapLamports != 0 {
		return errors.New("sell profile contains buy-only fields")
	}
	if err := p.Route.Validate(); err != nil {
		return err
	}
	if p.InputLamports == 0 || p.InputLamports > p.Route.MaxInputLamports ||
		p.SlippageBPS == 0 || p.SlippageBPS > p.Route.MaxSlippageBPS {
		return errors.New("swap amount or slippage is outside the route policy")
	}
	maxRent := p.Route.MaxOutputAccountRentLamports
	if p.ReserveLamports == 0 || p.MaxFeeLamports == 0 ||
		p.InputLamports > ^uint64(0)-p.MaxFeeLamports ||
		p.InputLamports+p.MaxFeeLamports > ^uint64(0)-maxRent ||
		p.ReserveLamports > ^uint64(0)-(p.InputLamports+p.MaxFeeLamports+maxRent) ||
		p.DailyDebitCapLamports < p.InputLamports+p.MaxFeeLamports+maxRent {
		return errors.New("swap reserve or fee limit is invalid")
	}
	return p.validateCommon(pricetrigger.SellAtOrAbove)
}

func (p Profile) validateBuy() error {
	if !p.isBuy() || p.Cluster != "devnet" || p.BuyRoute == nil ||
		p.Route != (orcaswap.Policy{}) || p.InputLamports != 0 ||
		p.DailyDebitCapLamports != 0 {
		return errors.New("buy profile must be Orca Devnet v2")
	}
	if err := p.BuyRoute.Validate(); err != nil {
		return err
	}
	if p.InputTokenAmount == 0 || p.InputTokenAmount > p.BuyRoute.MaxInputTokenAmount ||
		p.SlippageBPS == 0 || p.SlippageBPS > p.BuyRoute.MaxSlippageBPS ||
		p.DailyInputTokenCap < p.InputTokenAmount ||
		p.DailyNativeFeeCapLamports < p.MaxFeeLamports {
		return errors.New("buy amount or daily limits are outside the route policy")
	}
	maxRent := p.BuyRoute.MaxTemporaryRentLamports
	if p.ReserveLamports == 0 || p.MaxFeeLamports == 0 ||
		p.MaxFeeLamports > ^uint64(0)-maxRent ||
		p.ReserveLamports > ^uint64(0)-(p.MaxFeeLamports+maxRent) {
		return errors.New("buy reserve or fee limit is invalid")
	}
	return p.validateCommon(pricetrigger.BuyAtOrBelow)
}

func (p Profile) validateCommon(direction pricetrigger.Direction) error {
	if p.ScheduleWindowSeconds < 60 || p.ScheduleWindowSeconds > 86_400 ||
		86_400%p.ScheduleWindowSeconds != 0 || p.ScheduleAnchorUnix <= 0 ||
		p.ScheduleAnchorUnix%86_400 != 0 {
		return errors.New("swap schedule must divide one UTC day")
	}
	if p.MaxObservationAgeSeconds == 0 || p.MaxObservationAgeSeconds > 300 {
		return errors.New("swap observation age must be between 1 and 300 seconds")
	}
	if p.MinHealthyObservationSeconds == 0 ||
		p.MinHealthyObservationSeconds > p.MaxObservationAgeSeconds ||
		p.MinHealthySlotAdvance == 0 || p.MinHealthySlotAdvance > 1_000 {
		return errors.New("swap sustained-health limits are invalid")
	}
	if p.MaxClockUncertaintyMillis == 0 || p.MaxClockUncertaintyMillis > 2_000 {
		return errors.New("swap clock uncertainty must be between 1 and 2000 milliseconds")
	}
	if p.MaxBlockHeightWindow == 0 || p.MaxBlockHeightWindow > 300 {
		return errors.New("swap block-height window must be between 1 and 300")
	}
	if p.MaxReconciliationSeconds < 30 || p.MaxReconciliationSeconds > 900 {
		return errors.New("swap reconciliation time must be between 30 and 900 seconds")
	}
	if p.PriceTrigger != nil {
		if err := p.PriceTrigger.Validate(); err != nil {
			return err
		}
		if p.PriceTrigger.Direction != direction {
			if direction == pricetrigger.SellAtOrAbove {
				return errors.New("the Orca Devnet v1 route supports only sell-at-or-above triggers")
			}
			return errors.New("price trigger direction does not match the swap profile")
		}
	}
	return nil
}

func (p Profile) ClockUncertaintyLimit() time.Duration {
	return time.Duration(p.MaxClockUncertaintyMillis) * time.Millisecond
}

// FundedTradesPerDay is how many trades this profile's daily caps can actually
// pay for. The control grant and these caps are independent bounds that nothing
// reconciled: `strategy enable --max-trades 5` would grant five actions against
// caps that fund one, and the four the signer refuses are invisible — the
// refusal is discarded at the process boundary and surfaces, a minute later, as
// an expired blockhash. Every caller that compares the two bounds routes here.
//
// It is a LOWER bound: a sell is charged output-account rent only on the trade
// that creates the account, so the real count is this or better. Erring low is
// the safe direction for a refusal.
func (p Profile) FundedTradesPerDay() uint64 {
	perTrade := func(cap, cost uint64) uint64 {
		if cost == 0 {
			return 0
		}
		return cap / cost
	}
	if p.isBuy() {
		trades := perTrade(p.DailyInputTokenCap, p.InputTokenAmount)
		if fees := perTrade(p.DailyNativeFeeCapLamports, p.MaxFeeLamports); fees < trades {
			trades = fees
		}
		return trades
	}
	// A checked add, not a comparison against the addends: a three-term sum can
	// wrap to a value ABOVE two of them (3 + 3 + MaxUint64-1 wraps to 4), so the
	// obvious `cost < a || cost < b` guard passes and the division then reports
	// far MORE funded trades than exist. Erring high here is the one direction
	// that matters — it re-opens the arming mismatch this whole function closes.
	cost, carry := bits.Add64(p.InputLamports, p.MaxFeeLamports, 0)
	if carry != 0 {
		return 0
	}
	if cost, carry = bits.Add64(cost, p.Route.MaxOutputAccountRentLamports, 0); carry != 0 {
		return 0
	}
	return perTrade(p.DailyDebitCapLamports, cost)
}

func (p Profile) Fingerprint() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", errors.New("encode swap profile")
	}
	hash := sha256.New()
	domain := fingerprintDomain
	if p.isBuy() {
		domain = buyFingerprintDomain
	}
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (p Profile) Window(now time.Time) (start, end int64, err error) {
	if err := p.Validate(); err != nil {
		return 0, 0, err
	}
	nowUnix := now.UTC().Unix()
	if nowUnix < p.ScheduleAnchorUnix {
		return 0, 0, errors.New("swap schedule anchor is in the future")
	}
	window := int64(p.ScheduleWindowSeconds)
	start = nowUnix - (nowUnix-p.ScheduleAnchorUnix)%window
	return start, start + window, nil
}

func (p Profile) ActionID(now time.Time) (string, error) {
	fingerprint, err := p.Fingerprint()
	if err != nil {
		return "", err
	}
	start, _, err := p.Window(now)
	if err != nil {
		return "", err
	}
	return routeActionID(p, fingerprint, start)
}
