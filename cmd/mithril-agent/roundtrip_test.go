package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/swaprun"
)

// nextLeg must never guess. Holding both assets is ordinary mid-cycle, and
// picking one would be the command inventing an intention the operator has not
// expressed — the same class of mistake as arming without being asked.
func TestNextLegNeverGuesses(t *testing.T) {
	for name, test := range map[string]struct {
		sell, buy bool
		want      string
	}{
		"only sell fundable": {sell: true, want: "sell"},
		"only buy fundable":  {buy: true, want: "buy"},
		"both fundable":      {sell: true, buy: true, want: "either"},
		"neither fundable":   {want: "neither"},
	} {
		t.Run(name, func(t *testing.T) {
			state := roundTripState{
				Sell: &legState{Funded: test.sell},
				Buy:  &legState{Funded: test.buy},
			}
			if got := nextLeg(state); !strings.HasPrefix(got, test.want) {
				t.Fatalf("next leg = %q, want prefix %q", got, test.want)
			}
		})
	}
}

// Both legs armed against one wallet is the specific hazard this view exists
// to surface: they race for the same lamports and the loser reports a failure
// that looks like a bug.
func TestRenderNamesTheDoubleArmedHazard(t *testing.T) {
	var output strings.Builder
	state := roundTripState{
		SOL: "1 SOL", DevUSDC: "2 devUSDC",
		Sell: &legState{Direction: "sell", Input: "0.001 SOL", MinimumOut: "21 devUSDC",
			Funded: true, Armed: true, ControlMode: "devnet_enabled"},
		Buy: &legState{Direction: "buy", Input: "0.05 devUSDC", MinimumOut: "0.002 SOL",
			Funded: true, Armed: true, ControlMode: "devnet_enabled"},
		Next:     "either: the wallet can fund both legs",
		Warnings: []string{"both legs are armed against the same wallet; stop one before it races the other"},
	}
	if err := renderRoundTrip(&output, state); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"sell", "buy", "ARMED", "WARNING", "races the other", "Next:"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("render omitted %q:\n%s", expected, output.String())
		}
	}
}

// A shortfall has to say how much is missing and in which asset, or the
// operator has to work out the funding gap themselves from two balances.
func TestRenderReportsShortfallWithItsAsset(t *testing.T) {
	var output strings.Builder
	state := roundTripState{
		SOL: "0.001 SOL", DevUSDC: "0 devUSDC",
		Sell: &legState{Direction: "sell", Input: "0.001 SOL", MinimumOut: "21 devUSDC",
			Funded: false, Shortfall: "0.052 SOL", ControlMode: "no_new_actions"},
		Buy: &legState{Direction: "buy", Input: "0.05 devUSDC", MinimumOut: "0.002 SOL",
			Funded: false, Shortfall: "0.05 devUSDC", ControlMode: "no_new_actions"},
		Next: "neither: the wallet cannot fund either leg",
	}
	if err := renderRoundTrip(&output, state); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"short by 0.052 SOL", "short by 0.05 devUSDC", "neither"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("render omitted %q:\n%s", expected, output.String())
		}
	}
}

// A grant that has expired, or spent its last action, still reports mode
// devnet_enabled — control.state decrements RemainingActions without rewriting
// the mode — so liveness has to be read structurally. It was briefly decided by
// matching the human-readable grant text, which made the answer hostage to that
// wording and treated a document with NO expiry as live even though
// control.validateState rejects one.
func TestGrantLivenessIsStructuralNotTextual(t *testing.T) {
	for name, test := range map[string]struct {
		body string
		want bool
	}{
		"live":             {`{"version":3,"mode":"devnet_enabled","expires_at":"2100-01-01T00:00:00Z","remaining_actions":1}`, true},
		"expired":          {`{"version":3,"mode":"devnet_enabled","expires_at":"2000-01-01T00:00:00Z","remaining_actions":1}`, false},
		"exhausted":        {`{"version":3,"mode":"devnet_enabled","expires_at":"2100-01-01T00:00:00Z"}`, false},
		"no expiry at all": {`{"version":3,"mode":"devnet_enabled","remaining_actions":1}`, false},
		"stopped":          {`{"version":3,"mode":"no_new_actions"}`, false},
		"unreadable":       {`not json`, false},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.json")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, live := controlGrantAt(path); live != test.want {
				t.Fatalf("live = %v, want %v for %s", live, test.want, test.body)
			}
		})
	}
}

// deriveRoundTrip is the whole judgement of this screen, and it sat at zero
// coverage because the reads around it post to a hardcoded endpoint. These are
// the branches an operator acts on.
func TestDeriveRoundTripJudgesFundingFromBalances(t *testing.T) {
	sell := config{Swap: profilePointer(testSwapProfile(reserveOwner))}
	buy := config{Swap: profilePointer(testBuySwapProfile(t))}
	sellNeed := sell.Swap.WalletRequirementLamports()
	buyNative := buy.Swap.WalletRequirementLamports()
	buyTokens := buy.Swap.InputTokenAmount

	for name, test := range map[string]struct {
		lamports, tokens uint64
		wantSellFunded   bool
		wantBuyFunded    bool
		wantNext         string
	}{
		"neither": {0, 0, false, false, "neither: the wallet cannot fund either leg"},
		"sell only": {
			sellNeed, 0, true, false, "sell",
		},
		// The buy's INPUT is a token, which makes it easy to believe a wallet full
		// of devUSDC can buy. It cannot: the fee and the temporary account's rent
		// are native SOL.
		"tokens but no native SOL": {
			0, buyTokens, false, false, "neither: the wallet cannot fund either leg",
		},
		"buy only": {
			buyNative, buyTokens, buyNative >= sellNeed, true,
			map[bool]string{true: "either: the wallet can fund both legs", false: "buy"}[buyNative >= sellNeed],
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := deriveRoundTrip(sell, buy, roundTripPaths{}, test.lamports, test.tokens)
			if state.Sell.Funded != test.wantSellFunded {
				t.Errorf("sell funded = %v, want %v (%+v)", state.Sell.Funded, test.wantSellFunded, state.Sell)
			}
			if state.Buy.Funded != test.wantBuyFunded {
				t.Errorf("buy funded = %v, want %v (%+v)", state.Buy.Funded, test.wantBuyFunded, state.Buy)
			}
			if state.Next != test.wantNext {
				t.Errorf("next = %q, want %q", state.Next, test.wantNext)
			}
		})
	}
}

// A sweep pointed at the wrong file must not read as "no sweep configured": that
// is a comforting fact, and the operator would act on it.
func TestDeriveRoundTripExplainsAnUnusableSweep(t *testing.T) {
	sell := config{Swap: profilePointer(testSwapProfile(reserveOwner))}
	buy := config{Swap: profilePointer(testBuySwapProfile(t))}
	for name, path := range map[string]string{
		"absent":      "/nonexistent/sweep.json",
		"not a sweep": writeStrategyConfig(t, sell),
	} {
		t.Run(name, func(t *testing.T) {
			state := deriveRoundTrip(sell, buy, roundTripPaths{sweep: path}, 0, 0)
			if state.SweepArmed != nil {
				t.Fatal("an unusable sweep reported an arming state")
			}
			if len(state.Warnings) != 1 || !strings.Contains(state.Warnings[0], path) {
				t.Fatalf("warnings = %+v, want one naming %s", state.Warnings, path)
			}
			if !strings.Contains(state.Warnings[0], "not used") {
				t.Errorf("the warning did not say the sweep was unused: %s", state.Warnings[0])
			}
		})
	}
}

func profilePointer(profile swaprun.Profile) *swaprun.Profile { return &profile }

func writeStrategyConfig(t *testing.T, cfg config) string {
	t.Helper()
	cfg.Evidence.PrimaryTrustDomain = "primary.test"
	cfg.Evidence.SecondaryTrustDomain = "secondary.test"
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
