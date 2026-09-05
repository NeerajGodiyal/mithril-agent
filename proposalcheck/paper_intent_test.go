package proposalcheck

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func paperIntentFixture(t *testing.T) (shadow.Policy, []shadow.Tick, Candidate, PaperIntentBounds) {
	t.Helper()
	candidate := candidateFixture(t)
	want := shadow.MainnetQuoteRoute(true)
	oldMint := candidate.Policy.OutputMint
	oldAccount := candidate.Request.DestinationTokenAccount
	newAccount, err := orcaswap.AssociatedTokenAddress(candidate.Policy.Owner, want.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	message, err := base64.StdEncoding.DecodeString(candidate.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	// Replace fixture account keys without changing the reviewed instruction
	// shape or amounts. Production code never rewrites a candidate.
	for old, replacement := range map[string]string{oldMint: want.OutputMint, oldAccount: newAccount} {
		before, _ := solana.Decode32(old)
		after, _ := solana.Decode32(replacement)
		for i := 0; i+32 <= len(message); i++ {
			if string(message[i:i+32]) == string(before[:]) {
				copy(message[i:i+32], after[:])
				i += 31
			}
		}
	}
	candidate.Policy.OutputMint = want.OutputMint
	candidate.Request.OutputMint = want.OutputMint
	candidate.Request.DestinationTokenAccount = newAccount
	candidate.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	p := shadow.Policy{Version: shadow.Version, Cluster: shadow.Mainnet, Market: shadow.MarketSOLUSDC,
		QuoteRoute: want, Observe: candidate.Policy.Owner, InputAmount: candidate.Request.InputAmount,
		InputDecimals: 9, OutputDecimals: 6, SlippageBPS: 50, FeeLamports: candidate.Policy.MaxFeeLamports,
		TickSeconds: 60, SettleSeconds: 30, StartingInputUnits: 1_000_000_000,
		Trigger: pricetrigger.Policy{Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 20_000_000,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90, MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			PrimarySourceSHA256: strings.Repeat("a", 64), SecondarySourceSHA256: strings.Repeat("b", 64)},
		QuotePeg: &pricetrigger.BandPolicy{Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
			MinimumMicros: pricetrigger.USDCBandMinimumMicros, MaximumMicros: pricetrigger.USDCBandMaximumMicros,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90, MaxDeviationBPS: 100, MaxConfidenceBPS: 100,
			PrimarySourceSHA256: strings.Repeat("c", 64), SecondarySourceSHA256: strings.Repeat("d", 64)}}
	ticks := []shadow.Tick{{At: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), Event: shadow.EventSignal,
		PriceMicros: 20_000_000, Triggered: true, QuoteLowerMicros: 999_000, QuoteUpperMicros: 1_001_000,
		DecisionQuote: &shadow.Quote{InputAmount: candidate.Request.InputAmount, EstimatedOutput: 20, MinimumOutput: 20}}}
	ticks[0].DecisionQuote.ReceivedAt = ticks[0].At
	ticks[0].EquityMicros = 20_000_000
	fingerprint, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := PaperEvidenceSHA256(ticks)
	if err != nil {
		t.Fatal(err)
	}
	bounds := PaperIntentBounds{PolicySHA256: fingerprint, EvidenceSHA256: digest, MaxInputAmount: p.InputAmount,
		NativeBudgetLamports: 10_000_000, ReserveLamports: 1_000_000}
	return p, ticks, candidate, bounds
}

func TestPaperIntentIsOfflineDeterministicAndBounded(t *testing.T) {
	p, ticks, c, b := paperIntentFixture(t)
	first, err := CheckPaperIntent(p, ticks, c.Policy, c, b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := CheckPaperIntent(p, ticks, c.Policy, c, b)
	if err != nil || first != again || first.SHA256 == "" || first.InputAmount != c.Request.InputAmount || !first.Sell {
		t.Fatalf("unstable unsigned receipt: %+v, %+v, %v", first, again, err)
	}
	for _, name := range []string{"policy", "provenance", "amount", "native budget", "overflow", "candidate", "deferred", "pending"} {
		t.Run(name, func(t *testing.T) {
			p, ticks, c, b := paperIntentFixture(t)
			switch name {
			case "policy":
				p.FeeLamports++
			case "provenance":
				b.EvidenceSHA256 = strings.Repeat("0", 64)
			case "amount":
				b.MaxInputAmount--
			case "native budget":
				b.NativeBudgetLamports = b.ReserveLamports
			case "overflow":
				b.ReserveLamports = ^uint64(0)
			case "candidate":
				c.LastValidBlockHeight = 0
			case "deferred":
				ticks[0].Deferred = true
				b.EvidenceSHA256, _ = PaperEvidenceSHA256(ticks)
			case "pending":
				ticks = append(ticks, ticks[0])
				b.EvidenceSHA256, _ = PaperEvidenceSHA256(ticks)
			}
			if _, err := CheckPaperIntent(p, ticks, c.Policy, c, b); err == nil {
				t.Fatal("unsafe handoff accepted")
			}
		})
	}
}

func TestPaperIntentRejectsCandidateWorseThanDecisionQuote(t *testing.T) {
	for _, minimum := range []bool{false, true} {
		p, ticks, candidate, bounds := paperIntentFixture(t)
		ticks[0].DecisionQuote.EstimatedOutput++
		if minimum {
			ticks[0].DecisionQuote.MinimumOutput++
		}
		var err error
		bounds.EvidenceSHA256, err = PaperEvidenceSHA256(ticks)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := shadow.Replay(p, ticks); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ValidateCandidateMaterial(candidate.Policy, candidate); err != nil {
			t.Fatal(err)
		}
		if _, err := CheckPaperIntent(p, ticks, candidate.Policy, candidate, bounds); err == nil {
			t.Fatalf("candidate worse than decision quote accepted (minimum changed: %v)", minimum)
		}
	}
}
