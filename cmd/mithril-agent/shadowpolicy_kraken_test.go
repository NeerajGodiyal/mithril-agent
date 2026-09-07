package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

func TestShadowPolicyKrakenSourceSelection(t *testing.T) {
	for name, args := range map[string][]string{
		"fixed-sol":    {"--sell-at-usd", "80", "--buy-at-usd", "70"},
		"adaptive-sol": {"--adaptive"},
		"mandate-sol":  {"--adaptive", "--market", "SOL/USDC", "--budget-sol", "1", "--drawdown-stop-bps", "750"},
		"mandate-jup":  {"--adaptive", "--market", "JUP/USDC", "--budget-usdc", "25", "--drawdown-stop-bps", "750"},
	} {
		t.Run(name, func(t *testing.T) {
			original := generatedPolicyPath(t, args...)
			explicit := generatedPolicyPath(t, append(append([]string{}, args...), "--kraken-source", "pre-trade")...)
			a, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(explicit)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) {
				t.Fatal("explicit default changed policy bytes")
			}
			base, err := loadShadowPolicy(original)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := loadShadowPolicy(generatedPolicyPath(t, append(append([]string{}, args...), "--kraken-source", "ticker-batch")...))
			if err != nil {
				t.Fatal(err)
			}
			for _, trigger := range []*pricetrigger.Policy{&base.Trigger, base.ReturnTrigger, base.NativeFeePrice} {
				if trigger == nil {
					continue
				}
				trigger.SecondarySourceSHA256, err = pricesource.KrakenTickerIdentitySHA256(trigger.Feed)
				if err != nil {
					t.Fatal(err)
				}
			}
			if base.QuotePeg != nil {
				base.QuotePeg.SecondarySourceSHA256, err = pricesource.KrakenTickerIdentitySHA256(base.QuotePeg.Feed)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(base, selected) {
				t.Fatal("source selection changed more than all secondary identities")
			}
		})
	}
}

func TestShadowPolicyKrakenSourceRejectsUnsupported(t *testing.T) {
	for _, args := range [][]string{
		{"--kraken-source", "unknown"}, {"--kraken-source", ""}, {"--kraken-source"},
		{"--kraken-source", "ticker-batch", "--cluster", "devnet"},
		{"--kraken-source", "ticker-batch", "--market", "WIF/USDC"},
		{"--kraken-source", "ticker-batch", "--market", "PYTH/USDC"},
		{"--kraken-source", "ticker-batch", "--market", "JTO/USDC"},
		{"--kraken-source", "ticker-batch", "--admission-artifact", "/unread"},
		{"--kraken-source", "ticker-batch", "--provisional-artifact", "/unread"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "policy.json")
			var output bytes.Buffer
			err := runShadowPolicy(append([]string{"--out", out, "--observe", "So11111111111111111111111111111111111111112", "--adaptive"}, args...), &output)
			if err == nil || output.Len() != 0 {
				t.Fatal("unsupported source accepted or emitted output")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("unsupported source wrote policy")
			}
		})
	}
}
