package policyauthority

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

type paperRequestFixture struct {
	policy             Policy
	paper              shadow.Policy
	ticks              []shadow.Tick
	bounds             proposalcheck.PaperIntentBounds
	candidate          proposalcheck.Candidate
	evidence           proposalcheck.Evidence
	primary, secondary jupiterSlot
	start              int64
	now                time.Time
	path               string
}

func newPaperRequestFixture(t *testing.T) paperRequestFixture {
	t.Helper()
	policy, candidate, evidence, primary, secondary, start := jupiterAuthorityFixture(t)
	route := shadow.MainnetQuoteRoute(true)
	output, err := orcaswap.AssociatedTokenAddress(candidate.Policy.Owner, route.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	message, err := base64.StdEncoding.DecodeString(candidate.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse the canonical unsigned authority fixture, substituting only its
	// synthetic output mint/account with the paper market's USDC identity.
	for before, after := range map[string]string{candidate.Policy.OutputMint: route.OutputMint,
		candidate.Request.DestinationTokenAccount: output} {
		oldKey, err := solana.Decode32(before)
		if err != nil {
			t.Fatal(err)
		}
		newKey, err := solana.Decode32(after)
		if err != nil {
			t.Fatal(err)
		}
		message = bytes.ReplaceAll(message, oldKey[:], newKey[:])
	}
	candidate.Policy.OutputMint = route.OutputMint
	candidate.Request.OutputMint = route.OutputMint
	candidate.Request.DestinationTokenAccount = output
	candidate.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	policy.TransactionPolicy.Jupiter = &candidate.Policy
	policy.TransactionPolicy.ProfileFingerprint, err = candidate.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	p := shadow.Policy{Version: shadow.Version, Cluster: shadow.Mainnet, Market: shadow.MarketSOLUSDC,
		QuoteRoute: route, Observe: candidate.Policy.Owner, InputAmount: candidate.Request.InputAmount,
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
	now := time.Unix(start+1, 0).UTC()
	ticks := []shadow.Tick{{At: now, Event: shadow.EventSignal, PriceMicros: 20_000_000,
		Triggered: true, QuoteLowerMicros: 999_000, QuoteUpperMicros: 1_001_000, EquityMicros: 20_000_000,
		DecisionQuote: &shadow.Quote{InputAmount: 10, EstimatedOutput: 20, MinimumOutput: 20, ReceivedAt: now}}}
	fingerprint, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := proposalcheck.PaperEvidenceSHA256(ticks)
	if err != nil {
		t.Fatal(err)
	}
	return paperRequestFixture{policy: policy, paper: p, ticks: ticks, candidate: candidate,
		bounds: proposalcheck.PaperIntentBounds{PolicySHA256: fingerprint, EvidenceSHA256: digest,
			MaxInputAmount: 10, NativeBudgetLamports: 10_000_000, ReserveLamports: 1_000_000},
		evidence: evidence, primary: primary, secondary: secondary, start: start, now: now,
		path: filepath.Join(t.TempDir(), "paper-claim.jsonl")}
}

func (f paperRequestFixture) claim(t *testing.T) (signer.Request, error) {
	t.Helper()
	return ClaimPaperRequest(t.Context(), f.path, f.policy, f.paper, f.ticks, f.bounds,
		f.candidate, f.start, f.now, f.evidence, f.primary, f.secondary)
}

func TestPaperRequestClaimIsDurableUnsignedAndIdempotent(t *testing.T) {
	f := newPaperRequestFixture(t)
	first, err := f.claim(t)
	if err != nil {
		t.Fatal(err)
	}
	if first.RiskGrant.SignatureBase64 != "" || first.RiskGrant.Claims.Version != 0 {
		t.Fatal("claim granted authority")
	}
	if err := f.policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := signer.ValidateJupiterRequest(f.policy.TransactionPolicy, first); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.path)
	if err != nil || len(before) == 0 {
		t.Fatalf("request returned without a durable claim: %v", err)
	}
	// Each call closes/reopens the writer, exercising restart projection.
	f.now = f.now.Add(time.Second)
	again, err := f.claim(t)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("exact repeat changed request: %v", err)
	}
	after, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("repeat rewrote claim journal: %v", err)
	}
}

type expiredPaperEvidence struct{ proposalcheck.Evidence }

func (expiredPaperEvidence) NodeBlockHeight(context.Context, uint64) (uint64, error) {
	return 200, nil
}

func TestPaperRequestClaimRejectsChangedOrStaleInputs(t *testing.T) {
	for _, name := range []string{"window", "request", "decision", "policy", "input bound", "native budget", "reserve", "expired blockhash", "expired schedule", "provider", "paper provenance"} {
		t.Run(name, func(t *testing.T) {
			f := newPaperRequestFixture(t)
			if _, err := f.claim(t); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "window":
				f.start += 3600
				f.now = f.now.Add(time.Hour)
			case "request":
				f.candidate.LastValidBlockHeight++
			case "decision":
				f.ticks[0].At = f.ticks[0].At.Add(time.Second)
				f.ticks[0].DecisionQuote.ReceivedAt = f.ticks[0].At
				f.bounds.EvidenceSHA256, err = proposalcheck.PaperEvidenceSHA256(f.ticks)
			case "policy":
				f.policy.TransactionPolicy.DailyDebitCapLamports++
			case "input bound":
				f.bounds.MaxInputAmount++
			case "native budget":
				f.bounds.NativeBudgetLamports++
			case "reserve":
				f.bounds.ReserveLamports++
			case "expired blockhash":
				f.evidence = expiredPaperEvidence{f.evidence}
			case "expired schedule":
				f.now = f.now.Add(time.Hour)
			case "provider":
				f.primary.identity = strings.Repeat("3", 64)
			case "paper provenance":
				f.bounds.EvidenceSHA256 = strings.Repeat("0", 64)
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "window" || name == "request" || name == "decision" || name == "policy" ||
				name == "input bound" || name == "native budget" || name == "reserve" {
				// These must fail at the durable binding, not an unrelated
				// fixture/policy error in either existing validator.
				if _, err := proposalcheck.CheckPaperIntent(f.paper, f.ticks,
					*f.policy.TransactionPolicy.Jupiter, f.candidate, f.bounds); err != nil {
					t.Fatalf("changed input is not independently valid paper evidence: %v", err)
				}
				if _, err := PrepareJupiterRequest(t.Context(), f.policy, f.candidate,
					f.start, f.now, f.evidence, f.primary, f.secondary); err != nil {
					t.Fatalf("changed input is not an independently valid unsigned request: %v", err)
				}
			}
			request, err := f.claim(t)
			if err == nil || !reflect.DeepEqual(request, signer.Request{}) {
				t.Fatalf("rejected claim returned request: %v", err)
			}
			after, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejection changed journal: %v", err)
			}
		})
	}
}

func TestPaperRequestClaimFailsClosedOnLockAndForeignJournal(t *testing.T) {
	f := newPaperRequestFixture(t)
	store, err := journal.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	request, err := f.claim(t)
	if !errors.Is(err, journal.ErrLocked) || !reflect.DeepEqual(request, signer.Request{}) {
		t.Fatalf("concurrent writer was not refused: %v", err)
	}
	if _, err := store.Append(f.now, "unrelated.event", "another-action", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if request, err := f.claim(t); err == nil || !reflect.DeepEqual(request, signer.Request{}) {
		t.Fatalf("foreign journal was accepted: %v", err)
	}
}

func TestPaperRequestClaimRequiresSuccessfulAppend(t *testing.T) {
	f := newPaperRequestFixture(t)
	intent, err := proposalcheck.CheckPaperIntent(f.paper, f.ticks, f.candidate.Policy, f.candidate, f.bounds)
	if err != nil {
		t.Fatal(err)
	}
	request, err := PrepareJupiterRequest(t.Context(), f.policy, f.candidate, f.start, f.now, f.evidence, f.primary, f.secondary)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := claimPaperRequest(store, f.policy, intent, request, f.now); err == nil {
		t.Fatal("failed append was accepted")
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("failed append created records: %+v", records)
	}
}
