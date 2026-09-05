package policyauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type paperRequestFixture struct {
	policy             Policy
	paper              shadow.Policy
	ticks              []shadow.Tick
	bounds             proposalcheck.PaperIntentBounds
	candidate          proposalcheck.Candidate
	evidence           proposalcheck.NativeReserveEvidence
	primary, secondary jupiterSlot
	start              int64
	now                time.Time
	maxDecisionAge     time.Duration
	path               string
	acquisitionPath    string
	maxAcquisitionAge  time.Duration
}

type paperReserveEvidence struct {
	proposalcheck.Evidence
	calls                      int
	genesisChecks              int
	owner                      string
	upfront, reserve, min, max uint64
	extra                      uint64
	mutate                     func(*txflow.AccountEvidence)
	err                        error
	delay                      time.Duration
}

func (e *paperReserveEvidence) VerifyGenesis(ctx context.Context, expected string) error {
	e.genesisChecks++
	return e.Evidence.VerifyGenesis(ctx, expected)
}

func (e *paperReserveEvidence) VerifyNativeReserve(_ context.Context, owner string, upfront, reserve, minimum, maximum uint64) (txflow.AccountEvidence, error) {
	time.Sleep(e.delay)
	e.calls++
	e.owner, e.upfront, e.reserve, e.min, e.max = owner, upfront, reserve, minimum, maximum
	account := txflow.AccountEvidence{
		Address: owner, PrimaryOwner: "11111111111111111111111111111111", SecondaryOwner: "11111111111111111111111111111111",
		PrimaryLamports: upfront + reserve + e.extra, SecondaryLamports: upfront + reserve + e.extra,
		PrimaryContextSlot: minimum, SecondaryContextSlot: minimum,
	}
	if e.mutate != nil {
		e.mutate(&account)
	}
	return account, e.err
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
	f := paperRequestFixture{policy: policy, paper: p, ticks: ticks, candidate: candidate,
		bounds: proposalcheck.PaperIntentBounds{PolicySHA256: fingerprint, EvidenceSHA256: digest,
			MaxInputAmount: 10, NativeBudgetLamports: 10_000_000, ReserveLamports: 1_000_000},
		evidence: &paperReserveEvidence{Evidence: evidence}, primary: primary, secondary: secondary, start: start, now: now, maxDecisionAge: 2 * time.Hour,
		path: filepath.Join(t.TempDir(), "paper-claim.jsonl")}
	f.acquisitionPath = filepath.Join(t.TempDir(), "acquisition.jsonl")
	f.maxAcquisitionAge = 2 * time.Hour
	seedPaperAcquisition(t, f)
	return f
}

func seedPaperAcquisition(t *testing.T, f paperRequestFixture) {
	t.Helper()
	encoded, err := proposalcheck.EncodeCandidate(f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	payload := struct {
		CandidateSHA256 string        `json:"candidate_sha256"`
		ResponseSHA256  string        `json:"response_sha256"`
		ReceivedAt      time.Time     `json:"received_at"`
		MaxAge          time.Duration `json:"max_age_ns,string"`
	}{hex.EncodeToString(digest[:]), strings.Repeat("a", 64), f.now, f.maxAcquisitionAge}
	store, err := journal.Open(f.acquisitionPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(f.now, "proposal.acquired-v1", payload.CandidateSHA256, payload); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f paperRequestFixture) claim(t *testing.T) (signer.Request, error) {
	t.Helper()
	return ClaimPaperRequest(t.Context(), f.path, f.policy, f.paper, f.ticks, f.bounds,
		f.candidate, f.start, f.now, f.maxDecisionAge, f.acquisitionPath, f.maxAcquisitionAge, f.evidence, f.primary, f.secondary)
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

type expiredPaperEvidence struct {
	proposalcheck.NativeReserveEvidence
}

func (expiredPaperEvidence) NodeBlockHeight(context.Context, uint64) (uint64, error) {
	return 200, nil
}

func TestPaperRequestClaimRejectsChangedOrStaleInputs(t *testing.T) {
	for _, name := range []string{"window", "request", "decision", "policy", "input bound", "native budget", "reserve", "max decision age", "expired blockhash", "expired schedule", "provider", "paper provenance"} {
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
				f.acquisitionPath = filepath.Join(t.TempDir(), "changed-candidate.jsonl")
				seedPaperAcquisition(t, f)
			case "decision":
				f.now = f.now.Add(time.Second)
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
			case "max decision age":
				f.maxDecisionAge++
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
			durableConflict := name == "window" || name == "request" || name == "decision" || name == "policy" ||
				name == "input bound" || name == "native budget" || name == "reserve" || name == "max decision age"
			if durableConflict {
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
			if durableConflict && !strings.Contains(err.Error(), "different or pending claim") {
				t.Fatalf("changed input did not reach durable binding: %v", err)
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
	if err := claimPaperRequest(store, f.policy, intent, request, f.now, f.maxDecisionAge, strings.Repeat("a", 64)); err == nil {
		t.Fatal("failed append was accepted")
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("failed append created records: %+v", records)
	}
}

type unexpectedPaperPreparation struct {
	proposalcheck.NativeReserveEvidence
}

func (unexpectedPaperPreparation) EvidenceProviderIdentities() (string, string) {
	panic("rejected recency reached preparation")
}

func TestPaperRequestRecencyRejectsBeforeJournalOrPreparation(t *testing.T) {
	for _, name := range []string{"zero limit", "negative limit", "zero now", "empty history", "missing quote", "zero decision", "future decision", "expired decision", "zero receipt", "future receipt", "expired receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newPaperRequestFixture(t)
			f.maxDecisionAge = time.Minute
			f.now = f.now.Add(time.Minute)
			switch name {
			case "zero limit":
				f.maxDecisionAge = 0
			case "negative limit":
				f.maxDecisionAge = -1
			case "zero now":
				f.now = time.Time{}
			case "empty history":
				f.ticks = nil
			case "missing quote":
				f.ticks[0].DecisionQuote = nil
			case "zero decision":
				f.ticks[0].At = time.Time{}
			case "future decision":
				f.ticks[0].At = f.now.Add(time.Nanosecond)
			case "expired decision":
				f.ticks[0].At = f.ticks[0].At.Add(-time.Nanosecond)
			case "zero receipt":
				f.ticks[0].DecisionQuote.ReceivedAt = time.Time{}
			case "future receipt":
				f.ticks[0].DecisionQuote.ReceivedAt = f.now.Add(time.Nanosecond)
			case "expired receipt":
				f.ticks[0].DecisionQuote.ReceivedAt = f.ticks[0].At.Add(-time.Nanosecond)
			}
			var err error
			f.bounds.EvidenceSHA256, err = proposalcheck.PaperEvidenceSHA256(f.ticks)
			if err != nil {
				t.Fatal(err)
			}
			f.evidence = unexpectedPaperPreparation{f.evidence}
			request, err := f.claim(t)
			if err == nil || !strings.Contains(err.Error(), "recency") || !reflect.DeepEqual(request, signer.Request{}) {
				t.Fatalf("invalid recency returned request or wrong error: %v", err)
			}
			if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid recency created a journal: %v", err)
			}
		})
	}
}

func TestPaperRequestRecencyBoundaryAndExpiredRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newPaperRequestFixture(t)
		f.maxDecisionAge = time.Minute
		f.now = f.now.Add(f.maxDecisionAge)
		if _, err := f.claim(t); err != nil {
			t.Fatalf("exact recency boundary rejected: %v", err)
		}
		before, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Nanosecond)
		// The historical receipt and exact unsigned request remain valid. Only
		// current recency must reject this repeat after the journal was closed.
		if _, err := proposalcheck.CheckPaperIntent(f.paper, f.ticks, f.candidate.Policy, f.candidate, f.bounds); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareJupiterRequest(t.Context(), f.policy, f.candidate, f.start, f.now, f.evidence, f.primary, f.secondary); err != nil {
			t.Fatal(err)
		}
		f.evidence = unexpectedPaperPreparation{f.evidence}
		request, err := f.claim(t)
		if err == nil || !strings.Contains(err.Error(), "recency") || !reflect.DeepEqual(request, signer.Request{}) {
			t.Fatalf("expired restart returned request or wrong error: %v", err)
		}
		after, err := os.ReadFile(f.path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("expired restart changed journal: %v", err)
		}
	})
}

func TestPaperRequestRejectsLegacyClaimWithoutUpgrade(t *testing.T) {
	f := newPaperRequestFixture(t)
	if _, err := f.claim(t); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	record := store.Records()[0]
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Seed the exact old three-field payload through the durable journal API,
	// retaining every identity from an otherwise valid current claim.
	var legacyPayload struct {
		PaperIntentSHA256 string `json:"paper_intent_sha256"`
		PolicySHA256      string `json:"policy_sha256"`
		RequestSHA256     string `json:"request_sha256"`
	}
	if err := json.Unmarshal(record.Payload, &legacyPayload); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(t.TempDir(), "legacy-claim.jsonl")
	store, err = journal.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(f.now, record.Type, record.ActionID, legacyPayload); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	request, err := f.claim(t)
	if err == nil || !strings.Contains(err.Error(), "different or pending claim") || !reflect.DeepEqual(request, signer.Request{}) {
		t.Fatalf("legacy claim was accepted or failed outside durable binding: %v", err)
	}
	after, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy claim was upgraded or rewritten: %v", err)
	}
}

func TestPaperRequestNativeReserveRechecksWithoutChangingClaim(t *testing.T) {
	f := newPaperRequestFixture(t)
	evidence := f.evidence.(*paperReserveEvidence)
	checked, err := proposalcheck.Recheck(t.Context(), evidence, f.primary, f.secondary,
		f.candidate.Policy, *f.policy.JupiterProviders, f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	rechecks := evidence.genesisChecks
	first, err := f.claim(t)
	if err != nil {
		t.Fatalf("exact reserve equality rejected: %v", err)
	}
	if got := evidence.genesisChecks; got != rechecks+1 {
		t.Fatalf("claim performed %d proposal rechecks, want one", got-rechecks)
	}
	if evidence.calls != 1 || evidence.owner != f.candidate.Policy.Owner ||
		evidence.upfront != checked.MaximumUpfrontLamports || evidence.reserve != f.bounds.ReserveLamports ||
		evidence.min != max(checked.MinimumContextSlot, checked.PrimaryFeeContextSlot, checked.SecondaryFeeContextSlot, checked.SimulationContextSlot) ||
		evidence.max != checked.MinimumContextSlot+proposalcheck.MaxEvidenceSlotSkew {
		t.Fatalf("reserve check did not bind exact checked costs and contexts: %+v", evidence)
	}
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	evidence.extra = 100
	again, err := f.claim(t)
	if err != nil || evidence.calls != 2 || !reflect.DeepEqual(first, again) {
		t.Fatalf("repeat did not reread balance and preserve request: calls=%d err=%v", evidence.calls, err)
	}
	after, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("fresh balance changed durable claim: %v", err)
	}
	evidence.mutate = func(a *txflow.AccountEvidence) {
		a.PrimaryLamports = evidence.upfront + evidence.reserve - 1
		a.SecondaryLamports = a.PrimaryLamports
	}
	request, err := f.claim(t)
	if err == nil || evidence.calls != 3 || !reflect.DeepEqual(request, signer.Request{}) {
		t.Fatalf("repeat reused stale sufficient balance: calls=%d err=%v", evidence.calls, err)
	}
	after, err = os.ReadFile(f.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("insufficient repeat changed durable claim: %v", err)
	}
}

func TestPaperRequestNativeReserveRejectsBeforeClaim(t *testing.T) {
	for _, name := range []string{"insufficient", "wrong account", "wrong owner", "disagreeing balance", "stale context", "future context", "reader error", "provider"} {
		t.Run(name, func(t *testing.T) {
			f := newPaperRequestFixture(t)
			evidence := f.evidence.(*paperReserveEvidence)
			evidence.mutate = func(a *txflow.AccountEvidence) {
				switch name {
				case "insufficient":
					a.PrimaryLamports--
					a.SecondaryLamports--
				case "wrong account":
					a.Address = "wrong"
				case "wrong owner":
					a.PrimaryOwner = "wrong"
					a.SecondaryOwner = "wrong"
				case "disagreeing balance":
					a.SecondaryLamports++
				case "stale context":
					a.PrimaryContextSlot--
				case "future context":
					a.SecondaryContextSlot = evidence.max + 1
				}
			}
			if name == "reader error" {
				evidence.err = errors.New("native balance unavailable")
			}
			if name == "provider" {
				f.primary.identity = strings.Repeat("3", 64)
			}
			request, err := f.claim(t)
			if err == nil || !reflect.DeepEqual(request, signer.Request{}) {
				t.Fatalf("invalid balance qualification returned request: %v", err)
			}
			wantCalls := 1
			if name == "provider" {
				wantCalls = 0
			}
			if evidence.calls != wantCalls {
				t.Fatalf("reserve calls=%d, want %d", evidence.calls, wantCalls)
			}
			store, err := journal.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			count := len(store.Records())
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("failed reserve check appended %d records", count)
			}
		})
	}
}

func TestPaperRequestAcquisitionRejectsWithoutRenewal(t *testing.T) {
	for _, name := range []string{"missing", "expired", "changed age", "renewed receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newPaperRequestFixture(t)
			f.maxAcquisitionAge = time.Minute
			f.acquisitionPath = filepath.Join(t.TempDir(), "original.jsonl")
			seedPaperAcquisition(t, f)
			if _, err := f.claim(t); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				f.acquisitionPath = filepath.Join(t.TempDir(), "missing.jsonl")
			case "expired":
				f.now = f.now.Add(time.Minute + time.Nanosecond)
			case "changed age":
				f.maxAcquisitionAge++
			case "renewed receipt":
				f.now = f.now.Add(time.Second)
				f.acquisitionPath = filepath.Join(t.TempDir(), "renewed.jsonl")
				seedPaperAcquisition(t, f)
			}
			if name != "renewed receipt" {
				f.evidence = unexpectedPaperPreparation{f.evidence}
			}
			request, err := f.claim(t)
			if err == nil || !reflect.DeepEqual(request, signer.Request{}) {
				t.Fatalf("changed acquisition returned request: %v", err)
			}
			after, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("acquisition rejection rewrote claim: %v", err)
			}
		})
	}
}

func TestPaperRequestAcquisitionExpiresDuringPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newPaperRequestFixture(t)
		f.maxAcquisitionAge = time.Second
		f.acquisitionPath = filepath.Join(t.TempDir(), "short-lived.jsonl")
		seedPaperAcquisition(t, f)
		f.evidence.(*paperReserveEvidence).delay = time.Second + time.Nanosecond
		request, err := f.claim(t)
		if err == nil || !strings.Contains(err.Error(), "expired") || !reflect.DeepEqual(request, signer.Request{}) {
			t.Fatalf("preparation renewed acquisition: %v", err)
		}
		records, err := journal.ReadRecords(f.path)
		if err != nil || len(records) != 0 {
			t.Fatalf("expired preparation appended claim: %v", err)
		}
	})
}

func TestPaperRequestBoundsAtPreparationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		delay   time.Duration
		wantErr string
	}{
		{"decision boundary", time.Second, ""},
		{"decision expired", time.Second + time.Nanosecond, "recency"},
		{"schedule inside", time.Second - time.Nanosecond, ""},
		{"schedule ended", time.Second, "schedule window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newPaperRequestFixture(t)
				f.maxDecisionAge = time.Second
				if strings.HasPrefix(tc.name, "schedule") {
					f.maxDecisionAge = 2 * time.Hour
					f.now = time.Unix(f.start+3599, 0).UTC()
				}
				var err error
				f.bounds.EvidenceSHA256, err = proposalcheck.PaperEvidenceSHA256(f.ticks)
				if err != nil {
					t.Fatal(err)
				}
				f.evidence.(*paperReserveEvidence).delay = tc.delay
				request, err := f.claim(t)
				if tc.wantErr == "" {
					if err != nil || request.ActionID == "" {
						t.Fatalf("valid completion boundary rejected: %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !reflect.DeepEqual(request, signer.Request{}) {
					t.Fatalf("expired completion returned request or wrong error: %v", err)
				}
				if records, err := journal.ReadRecords(f.path); err != nil || len(records) != 0 {
					t.Fatalf("expired completion appended claim: %v", err)
				}
			})
		})
	}
}
