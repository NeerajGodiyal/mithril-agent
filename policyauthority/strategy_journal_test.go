package policyauthority

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type strategyTestSource struct{ sample pricetrigger.Sample }

func (s *strategyTestSource) IdentitySHA256() string { return s.sample.SourceSHA256 }
func (s *strategyTestSource) Latest(context.Context, string) (pricetrigger.Sample, error) {
	return s.sample, nil
}

type strategyTestRecorder struct{ ticks []shadow.Tick }

func (r *strategyTestRecorder) Record(_ time.Time, _ string, payload any) error {
	if tick, ok := payload.(shadow.Tick); ok {
		r.ticks = append(r.ticks, tick)
	}
	return nil
}

type strategyTestQuoter struct{}

func (strategyTestQuoter) Quote(_ context.Context, _ string, _ bool, amount uint64, _ uint16) (shadow.Quote, error) {
	return shadow.Quote{InputAmount: amount, EstimatedOutput: 14_000_000, MinimumOutput: 13_930_000}, nil
}

func strategyJournalFixture(t *testing.T) (string, strategySeed, Policy, signer.Request, time.Time) {
	t.Helper()
	return strategyJournalFixtureWithIdentity(t, "", "")
}

func strategyJournalFixtureWithIdentity(t *testing.T, owner, attestor string) (string, strategySeed, Policy, signer.Request, time.Time) {
	t.Helper()
	authority, request := paperTerminalRequestForOwner(t, owner)
	if attestor != "" {
		authority.TransactionPolicy.AttestationPublicKey = attestor
	}
	c := request.JupiterCandidate
	mint := shadow.MainnetMarketQuoteRoute(shadow.MarketSOLUSDC, true).OutputMint
	output, err := orcaswap.AssociatedTokenAddress(c.Policy.Owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	message, err := base64.StdEncoding.DecodeString(c.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	for before, after := range map[string]string{c.Policy.OutputMint: mint, c.Request.DestinationTokenAccount: output} {
		a, e := solana.Decode32(before)
		if e != nil {
			t.Fatal(e)
		}
		b, e := solana.Decode32(after)
		if e != nil {
			t.Fatal(e)
		}
		message = bytes.ReplaceAll(message, a[:], b[:])
	}
	message = bytes.ReplaceAll(message, binary.LittleEndian.AppendUint64(nil, 10), binary.LittleEndian.AppendUint64(nil, 7_000_000))
	message = bytes.ReplaceAll(message, binary.LittleEndian.AppendUint64(nil, 20), binary.LittleEndian.AppendUint64(nil, 14_000_000))
	c.Policy.OutputMint, c.Policy.MaxInputAmount, c.Policy.MinOutputAmount = mint, 7_000_000, 13_930_000
	c.Request.OutputMint, c.Request.DestinationTokenAccount, c.Request.InputAmount = mint, output, 7_000_000
	c.Quote.InputAmount, c.Quote.EstimatedOutput, c.Quote.MinimumOutput = 7_000_000, 14_000_000, 13_930_000
	c.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	request.MessageBase64 = c.MessageBase64
	authority.TransactionPolicy.Jupiter = &c.Policy
	authority.TransactionPolicy.MaxLamports = 7_000_000
	authority.TransactionPolicy.DailyDebitCapLamports = 20_000_000
	fingerprint, err := c.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	authority.TransactionPolicy.ProfileFingerprint, request.ProfileFingerprint = fingerprint, fingerprint
	request.ActionID, err = jupiterswap.ComputeActionID(fingerprint, request.ScheduleWindowStartUnix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.ValidateJupiterRequest(authority.TransactionPolicy, request); err != nil {
		t.Fatalf("request fixture: %v", err)
	}
	p := shadow.Policy{Version: shadow.Version, Cluster: shadow.Mainnet, Market: shadow.MarketSOLUSDC, QuoteRoute: shadow.MainnetMarketQuoteRoute(shadow.MarketSOLUSDC, true), Observe: c.Policy.Owner,
		InputAmount: 7_000_000, InputDecimals: 9, OutputDecimals: 6, SlippageBPS: 50, FeeLamports: 10_000, StartingInputUnits: 20_000_000, StartingOutputUnits: 100, TickSeconds: 60, SettleSeconds: 30,
		Trigger:  pricetrigger.Policy{Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD, Direction: pricetrigger.SellAtOrAbove, ThresholdMicros: 2_000_000_000, MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90, MaxDeviationBPS: 200, MaxConfidenceBPS: 200, PrimarySourceSHA256: strings.Repeat("a", 64), SecondarySourceSHA256: strings.Repeat("b", 64)},
		QuotePeg: &pricetrigger.BandPolicy{Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD, MinimumMicros: pricetrigger.USDCBandMinimumMicros, MaximumMicros: pricetrigger.USDCBandMaximumMicros, MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90, MaxDeviationBPS: 100, MaxConfidenceBPS: 100, PrimarySourceSHA256: strings.Repeat("c", 64), SecondarySourceSHA256: strings.Repeat("d", 64)},
		Adaptive: &shadow.AdaptivePolicy{Version: shadow.AdaptiveVersion, FastWindow: 2, SlowWindow: 3, MinimumSignalBPS: 100, MaxVolatilityBPS: 5000, MaxQuoteImpactBPS: 5000, MaxDrawdownBPS: 5000, CooldownSeconds: 60, MaxObservationGapSeconds: 600}}
	back := p.Trigger
	back.Direction, back.ThresholdMicros = pricetrigger.BuyAtOrBelow, 1_900_000_000
	p.ReturnTrigger = &back
	start := time.Unix(request.ScheduleWindowStartUnix, 0).UTC()
	sources := [4]*strategyTestSource{{sample: pricetrigger.Sample{SourceSHA256: p.Trigger.PrimarySourceSHA256, Feed: p.Trigger.Feed}}, {sample: pricetrigger.Sample{SourceSHA256: p.Trigger.SecondarySourceSHA256, Feed: p.Trigger.Feed}}, {sample: pricetrigger.Sample{SourceSHA256: p.QuotePeg.PrimarySourceSHA256, Feed: p.QuotePeg.Feed, PriceMicros: 1_000_000}}, {sample: pricetrigger.Sample{SourceSHA256: p.QuotePeg.SecondarySourceSHA256, Feed: p.QuotePeg.Feed, PriceMicros: 1_000_000}}}
	recorder := &strategyTestRecorder{}
	runner, err := shadow.NewRunner(p, sources[0], sources[1], strategyTestQuoter{}, recorder, sources[2], sources[3])
	if err != nil {
		t.Fatal(err)
	}
	for index, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
		at := start.Add(time.Duration(index) * time.Minute)
		for _, s := range sources {
			s.sample.PublishedAt = at
		}
		sources[0].sample.PriceMicros, sources[1].sample.PriceMicros = price, price
		if _, err := runner.Step(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := shadow.NewAccountedStrategy(p, recorder.ticks); err != nil {
		t.Fatalf("adaptive fixture: %v", err)
	}
	fp, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	history, err := proposalcheck.PaperEvidenceSHA256(recorder.ticks)
	if err != nil {
		t.Fatal(err)
	}
	bounds := proposalcheck.PaperIntentBounds{PolicySHA256: fp, EvidenceSHA256: history, MaxInputAmount: 7_000_000, NativeBudgetLamports: 20_000_000, ReserveLamports: 1_000_000}
	intent, err := proposalcheck.CheckPaperIntent(p, recorder.ticks, c.Policy, *c, bounds)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	wallet, claim := filepath.Join(dir, "wallet.jsonl"), filepath.Join(dir, "claim.jsonl")
	// Reuse the established test claim encoder, retaining its field order.
	scratch := filepath.Join(t.TempDir(), "claim.jsonl")
	claimAt := start.Add(3 * time.Minute)
	seedWalletAccountingClaim(t, scratch, authority, request, claimAt)
	claims, err := journal.ReadRecords(scratch)
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Replace(claims[0].Payload, []byte(`"paper_intent_sha256":"`+strings.Repeat("a", 64)+`"`), []byte(`"paper_intent_sha256":"`+intent.SHA256+`"`), 1)
	requestRaw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(append(raw[:len(raw)-1], []byte(`,"request":`)...), requestRaw...)
	raw = append(raw, '}')
	store, err := journal.OpenStrict(claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(claimAt, claims[0].Type, request.ActionID, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	o := txflow.WalletObservation{Owner: c.Policy.Owner, TokenMint: mint, TokenAccount: output, GenesisHash: solana.MainnetBetaGenesisHash, PrimaryIdentity: authority.JupiterProviders.PrimaryOriginSHA256, SecondaryIdentity: authority.JupiterProviders.SecondaryOriginSHA256, NativeLamports: 20_000_000, TokenUnits: 100, MinimumContextSlot: 100, MaximumContextSlot: 100 + proposalcheck.MaxEvidenceSlotSkew, NativePrimarySlot: 100, NativeSecondarySlot: 100, TokenPrimarySlot: 100, TokenSecondarySlot: 100}
	if _, err := initializeWalletInventory(wallet, authority, start.Add(-time.Minute), func(string, string) (txflow.WalletObservation, error) { return o, nil }); err != nil {
		t.Fatal(err)
	}
	now := claimAt.Add(time.Minute)
	if _, err := reserveWalletClaim(wallet, claim, authority, request, claimAt.Add(time.Second), func(WalletInventory) (txflow.WalletObservation, error) { return o, nil }); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "strategy.jsonl"), strategySeed{WalletPath: wallet, ClaimPath: claim, Policy: p, Ticks: recorder.ticks, Bounds: bounds}, authority, request, now
}

func TestStrategyJournalReplaysOriginalSeedAndObservations(t *testing.T) {
	path, seed, authority, request, now := strategyJournalFixture(t)
	state, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, seed.Bounds, now)
	if err != nil {
		t.Fatal(err)
	}
	opening := state.Ledger()
	seedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, seed.Bounds, now.Add(time.Second)); err != nil || !reflect.DeepEqual(again.Ledger(), opening) {
		t.Fatalf("seed repeat: %v", err)
	}
	changed := seed.Bounds
	changed.ReserveLamports++
	if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, changed, now.Add(time.Second)); err == nil {
		t.Fatal("different valid intent accepted")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(seedBytes, unchanged) {
		t.Fatal("seed repeat/conflict mutated journal")
	}
	at := now.Add(time.Minute)
	samples := [4]pricetrigger.Sample{{SourceSHA256: seed.Policy.Trigger.PrimarySourceSHA256, Feed: seed.Policy.Trigger.Feed, PriceMicros: 1_900_000_000, PublishedAt: at}, {SourceSHA256: seed.Policy.Trigger.SecondarySourceSHA256, Feed: seed.Policy.Trigger.Feed, PriceMicros: 1_900_000_000, PublishedAt: at}, {SourceSHA256: seed.Policy.QuotePeg.PrimarySourceSHA256, Feed: seed.Policy.QuotePeg.Feed, PriceMicros: 1_000_000, PublishedAt: at}, {SourceSHA256: seed.Policy.QuotePeg.SecondarySourceSHA256, Feed: seed.Policy.QuotePeg.Feed, PriceMicros: 1_000_000, PublishedAt: at}}
	decision, err := ObserveStrategyJournal(path, authority, submitter.Policy{}, at, samples[0], samples[1], samples[2], samples[3])
	if err != nil || decision.ReadyForQuote {
		t.Fatalf("pending observation: %+v,%v", decision, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveStrategyJournal(path, authority, submitter.Policy{}, at, samples[0], samples[1], samples[2], samples[3]); err != nil {
		t.Fatal(err)
	}
	// A later actual terminal does not rewrite the original seed or settle it.
	if _, err := recordPaperTerminal(seed.ClaimPath, authority, request, at.Add(time.Second), func() (submitter.JupiterFinalizedEvidence, error) {
		return submitter.JupiterFinalizedEvidence{ActionID: request.ActionID, Verdict: txflow.VerdictFailed, FeeLamports: 5000}, nil
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := ReadStrategyJournal(path, authority, submitter.Policy{}, at.Add(time.Minute))
	if err != nil || !restored.Pending() {
		t.Fatalf("restart: %v", err)
	}
	if _, err := state.Observe(at, samples[0], samples[1], samples[2], samples[3]); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Ledger(), state.Ledger()) || restored.Ledger().OpeningEquityMicros != opening.OpeningEquityMicros {
		t.Fatal("restart changed original books")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read/retry changed journal")
	}
	samples[0].SourceSHA256 = strings.Repeat("f", 64)
	if _, err := ObserveStrategyJournal(path, authority, submitter.Policy{}, at.Add(time.Minute), samples[0], samples[1], samples[2], samples[3]); err == nil {
		t.Fatal("invalid observation accepted")
	}
	locked, err := journal.OpenStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveStrategyJournal(path, authority, submitter.Policy{}, at.Add(time.Minute), samples[0], samples[1], samples[2], samples[3]); !errors.Is(err, journal.ErrLocked) {
		t.Fatalf("concurrent writer: %v", err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(before, []byte("torn")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStrategyJournal(path, authority, submitter.Policy{}, at.Add(time.Minute)); err == nil {
		t.Fatal("torn journal read")
	}
	if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, seed.Bounds, at.Add(time.Minute)); err == nil {
		t.Fatal("torn journal repaired")
	}
	after, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(after, append(before, []byte("torn")...)) {
		t.Fatal("torn bytes changed")
	}
}

func TestStrategyJournalRejectsChangedDependenciesWithoutWrites(t *testing.T) {
	for _, mode := range []string{"missing claim", "valid changed claim", "wallet balances", "wallet head", "relative wallet", "relative claim", "relative strategy"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, request, now := strategyJournalFixture(t)
			if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, seed.Bounds, now); err != nil {
				t.Fatal(err)
			}
			// Replacement journals are themselves valid hash chains. The reader
			// must reject changed provenance, not just a corrupt journal suffix.
			switch mode {
			case "missing claim":
				if err := os.Rename(seed.ClaimPath, seed.ClaimPath+".saved"); err != nil {
					t.Fatal(err)
				}
			case "valid changed claim":
				records, err := journal.ReadRecords(seed.ClaimPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(seed.ClaimPath, seed.ClaimPath+".saved"); err != nil {
					t.Fatal(err)
				}
				store, err := journal.OpenStrict(seed.ClaimPath)
				if err != nil {
					t.Fatal(err)
				}
				changed, err := store.Append(records[0].At.Add(time.Nanosecond), records[0].Type, records[0].ActionID, records[0].Payload)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if changed.Hash == records[0].Hash {
					t.Fatal("claim identity did not change")
				}
				if _, err := ValidatePaperRequestClaim(changed, authority, request); err != nil {
					t.Fatalf("replacement claim is invalid: %v", err)
				}
			case "wallet balances", "wallet head":
				records, err := journal.ReadRecords(seed.WalletPath)
				if err != nil {
					t.Fatal(err)
				}
				var opening txflow.WalletObservation
				var reservation WalletReservation
				if err := json.Unmarshal(records[0].Payload, &opening); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(records[1].Payload, &reservation); err != nil {
					t.Fatal(err)
				}
				if mode == "wallet balances" {
					opening.NativeLamports++
					reservation.Observation.NativeLamports++
				}
				if err := os.Rename(seed.WalletPath, seed.WalletPath+".saved"); err != nil {
					t.Fatal(err)
				}
				store, err := journal.OpenStrict(seed.WalletPath)
				if err != nil {
					t.Fatal(err)
				}
				first, err := store.Append(records[0].At.Add(-time.Nanosecond), records[0].Type, records[0].ActionID, opening)
				if err != nil {
					t.Fatal(err)
				}
				reservation.PreviousHeadSHA256 = first.Hash
				if _, err := store.Append(records[1].At, records[1].Type, records[1].ActionID, reservation); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := ReadWalletInventory(seed.WalletPath, authority); err != nil {
					t.Fatalf("replacement wallet is invalid: %v", err)
				}
			}
			before := make(map[string][]byte)
			for _, file := range []string{path, seed.WalletPath, seed.ClaimPath} {
				raw, err := os.ReadFile(file)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				before[file] = raw
			}
			if strings.HasPrefix(mode, "relative ") {
				journalPath, walletPath, claimPath := path, seed.WalletPath, seed.ClaimPath
				switch mode {
				case "relative wallet":
					walletPath = filepath.Base(walletPath)
				case "relative claim":
					claimPath = filepath.Base(claimPath)
				case "relative strategy":
					journalPath = filepath.Base(journalPath)
				}
				if _, err := InitializeStrategyJournal(journalPath, walletPath, claimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, seed.Bounds, now); err == nil {
					t.Fatal("relative path accepted")
				}
			} else {
				if _, err := ReadStrategyJournal(path, authority, submitter.Policy{}, now.Add(time.Minute)); err == nil {
					t.Fatal("changed dependency accepted")
				}
				if _, err := InitializeStrategyJournal(path, seed.WalletPath, seed.ClaimPath, authority, submitter.Policy{}, seed.Policy, seed.Ticks, seed.Bounds, now.Add(time.Minute)); err == nil {
					t.Fatal("changed dependency reseeded history")
				}
			}
			for file, want := range before {
				got, err := os.ReadFile(file)
				if want == nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing dependency was created: %v", err)
				}
				if want != nil && err != nil || !bytes.Equal(got, want) {
					t.Fatalf("invalid attempt changed %s: %v", file, err)
				}
			}
		})
	}
}
