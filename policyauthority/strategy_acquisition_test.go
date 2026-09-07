package policyauthority

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type strategyAcquisitionBuilder func(context.Context, jupiterquote.Request) (jupiterquote.BuildResult, error)

func (b strategyAcquisitionBuilder) Build(ctx context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
	return b(ctx, request)
}

func TestAcquireStrategyWalletClaimConnected(t *testing.T) {
	for _, mode := range []string{"fresh builder", "changed observation", "retained acquisition", "unbound acquisition", "committed decision", "expired acquisition", "missing builder"} {
		t.Run(mode, func(t *testing.T) {
			path, seed, authority, recovery, next, originalAcquisition, at := strategyDecisionFixture(t)
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			acquired, err := proposalcheck.ReadAcquisition(originalAcquisition, at, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			acquisition := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl.acquisition.jsonl"
			if mode != "fresh builder" && mode != "changed observation" && mode != "missing builder" {
				if err := os.Rename(originalAcquisition, acquisition); err != nil {
					t.Fatal(err)
				}
			}
			var builder proposalcheck.Builder
			var concurrentObservation []byte
			calls := 0
			if mode == "fresh builder" || mode == "changed observation" {
				// Deliver current observations without waiting through the historical
				// fixture's schedule. The builder still uses its real receipt clock.
				base := time.Now().UTC().Add(-121 * time.Second)
				for i, price := range []uint64{3_000_000_000, 2_500_000_000, 2_000_000_000} {
					observed := base.Add(time.Duration(i) * time.Minute)
					samples := strategyOutcomeSamples(seed, observed)
					samples[0].PriceMicros, samples[1].PriceMicros = price, price
					if _, err := ObserveStrategyJournal(path, authority, recovery, observed, samples[0], samples[1], samples[2], samples[3]); err != nil {
						t.Fatal(err)
					}
				}
				at = time.Now().UTC()
				proposal := WalletAdmissionBuildForTest(t, continuationCandidate(t, acquired.Candidate, "fresh acquired continuation"))
				builder = strategyAcquisitionBuilder(func(_ context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
					calls++
					if request != acquired.Candidate.Request {
						t.Fatal("builder request differs from the actual ready direction or size")
					}
					if mode == "changed observation" {
						observed := time.Now().UTC()
						samples := strategyOutcomeSamples(seed, observed)
						samples[0].PriceMicros, samples[1].PriceMicros = 2_000_000_000, 2_000_000_000
						ready, err := ObserveStrategyJournal(path, authority, recovery, observed, samples[0], samples[1], samples[2], samples[3])
						if err != nil || !ready.ReadyForQuote || ready.InputAmount != request.InputAmount {
							t.Fatalf("concurrent observation fixture is not ready: %v", err)
						}
						concurrentObservation, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					proposal.Quote.ReceivedAt = time.Now().UTC()
					proposal.Quote.ResponseSHA256 = strings.Repeat("a", 64)
					return proposal, nil
				})
			}
			if mode == "committed decision" {
				if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expired acquisition" {
				at = at.Add(2 * time.Minute)
			}
			decisionAge := time.Minute
			if mode == "expired acquisition" {
				decisionAge = 5 * time.Minute
			}
			if mode == "retained acquisition" || mode == "expired acquisition" {
				records, err := journal.ReadRecords(path)
				if err != nil {
					t.Fatal(err)
				}
				policySHA, _, err := paperRequestHashes(next, signer.Request{})
				if err != nil {
					t.Fatal(err)
				}
				last := records[len(records)-1]
				intent := strategyAcquisitionIntent{StrategyPath: path, StrategyHeadSHA256: last.Hash,
					WalletHeadSHA256: wallet.HeadSHA256, PolicySHA256: policySHA, Request: acquired.Candidate.Request,
					MaxDecisionAgeNS: int64(decisionAge), MaxAcquisitionAgeNS: int64(time.Minute)}
				store, err := journal.OpenStrict(acquisition + ".intent.jsonl")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Append(acquired.ReceivedAt, strategyAcquisitionEvent, last.Hash, intent); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{
				jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity},
				slot:            wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard,
			}, mutate: func(account *txflow.AccountEvidence) {
				account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
			}}
			window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
			start := anchor + (at.Unix()-anchor)/window*window
			claim, err := AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next,
				start, at, decisionAge, time.Minute, builder, evidence,
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity},
				jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
			if mode == "expired acquisition" || mode == "missing builder" || mode == "changed observation" || mode == "unbound acquisition" {
				if err == nil || claim.Request.ActionID != "" || (mode != "changed observation" && calls != 0) {
					t.Fatalf("invalid acquisition proceeded: %v", err)
				}
				want := map[string]string{"expired acquisition": "acquisition provenance", "missing builder": "requires a builder", "changed observation": "opportunity changed", "unbound acquisition": "lacks its original opportunity"}[mode]
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("rejected at the wrong boundary: %v; want %q", err, want)
				}
				after, readErr := os.ReadFile(path)
				if mode == "changed observation" {
					before = concurrentObservation
				}
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("failed acquisition changed strategy history")
				}
				if _, statErr := os.Stat(seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"); !os.IsNotExist(statErr) {
					t.Fatalf("failed acquisition created a claim: %v", statErr)
				}
				if mode == "changed observation" {
					_, retryErr := AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next,
						start, time.Now().UTC(), time.Minute, time.Minute, nil, evidence,
						jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity},
						jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
					if retryErr == nil {
						t.Fatal("retry rebound retained acquisition to a different observation")
					}
				}
				return
			}
			if err != nil || claim.Recovered || claim.Inventory.PendingSHA256 == "" {
				t.Fatalf("connected acquisition: %v", err)
			}
			if (mode == "fresh builder" && calls != 1) || (mode != "fresh builder" && calls != 0) {
				t.Fatalf("unexpected builder calls: %d", calls)
			}
			if mode == "committed decision" {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("resumed decision was appended again")
				}
			}
			claimBytes, err := os.ReadFile(claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".unavailable"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(acquisition, acquisition+".unavailable"); err != nil {
				t.Fatal(err)
			}
			retried, err := AcquireStrategyWalletClaim(t.Context(), seed.WalletPath, path, Policy{}, recovery, next,
				0, at.Add(24*time.Hour), 0, 0, nil, nil, nil, nil, nil)
			if err != nil || !retried.Recovered || !reflect.DeepEqual(claim.Request, retried.Request) || !reflect.DeepEqual(claim.Inventory, retried.Inventory) {
				t.Fatalf("recovery depended on fresh inputs: %v", err)
			}
			after, err := os.ReadFile(claim.ClaimPath)
			if err != nil || !bytes.Equal(claimBytes, after) {
				t.Fatal("acquisition recovery changed retained claim")
			}
		})
	}
}
