package policyauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// This opt-in test runs an independently built CLI against offline protected
// fixtures. It verifies restart routing, not a filled end-to-end trading run.
func TestStrategyStepCompiledCLIRestartRouting(t *testing.T) {
	binary := os.Getenv("MITHRIL_AGENT_QA_CLI")
	if binary == "" {
		t.Skip("set MITHRIL_AGENT_QA_CLI to an independently built CLI")
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("QA CLI must be a clean absolute path")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("QA CLI is not executable: %v", err)
	}
	for _, name := range []string{"unreserved claim", "retained acquisition", "paired unreserved claim", "paired retained acquisition", "paired missing selected"} {
		t.Run(name, func(t *testing.T) {
			paired := strings.HasPrefix(name, "paired ")
			mode := strings.TrimPrefix(name, "paired ")
			if mode == "missing selected" {
				mode = "retained acquisition"
			}
			path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
			wallet, err := ReadWalletInventory(seed.WalletPath, next)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{path, seed.WalletPath, seed.ClaimPath}
			if mode == "unreserved claim" {
				if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
					t.Fatal(err)
				}
				evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(account *txflow.AccountEvidence) {
					account.PrimaryLamports, account.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
				}}
				observed := wallet
				observed.NativeLamports--
				window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
				start := anchor + (at.Unix()-anchor)/window*window
				if _, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, start, at, time.Minute, evidence,
					jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, observed)); err == nil || !strings.Contains(err.Error(), "wallet balances changed") {
					t.Fatalf("fixture did not retain an unreserved claim: %v", err)
				}
				claim := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
				if _, err := ReadClaimedPaperRequest(claim, next); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, acquisition, claim)
			} else {
				acquired, err := proposalcheck.ReadAcquisition(acquisition, at, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				fixed := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl.acquisition.jsonl"
				if err := os.Rename(acquisition, fixed); err != nil {
					t.Fatal(err)
				}
				records, err := journal.ReadRecords(path)
				if err != nil {
					t.Fatal(err)
				}
				last := records[len(records)-1]
				policySHA, _, err := paperRequestHashes(next, signer.Request{})
				if err != nil {
					t.Fatal(err)
				}
				intent := strategyAcquisitionIntent{StrategyPath: path, StrategyHeadSHA256: last.Hash, WalletHeadSHA256: wallet.HeadSHA256,
					PolicySHA256: policySHA, Request: acquired.Candidate.Request, MaxDecisionAgeNS: int64(time.Minute), MaxAcquisitionAgeNS: int64(time.Minute)}
				store, err := journal.OpenStrict(fixed + ".intent.jsonl")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Append(acquired.ReceivedAt, strategyAcquisitionEvent, last.Hash, intent); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, fixed, fixed+".intent.jsonl")
			}
			root := filepath.Dir(path)
			authorityPath, recoveryPath, nextPath := filepath.Join(root, "cli-authority.json"), filepath.Join(root, "cli-recovery.json"), filepath.Join(root, "cli-next.json")
			for file, value := range map[string]any{authorityPath: authority, recoveryPath: recovery, nextPath: next} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, file)
			}
			before := strategyReconcileSnapshot(t, paths...)
			args := []string{"proposal", "strategy", "--operation", "step", "--strategy", path, "--inventory", seed.WalletPath,
				"--authority-policy", authorityPath, "--submitter-policy", recoveryPath, "--next-submitter-policy", recoveryPath}
			if mode == "retained acquisition" {
				args = append(args, "--max-decision-age-seconds", "60", "--max-acquisition-age-seconds", "60")
				if paired {
					selected := nextPath
					if name == "paired missing selected" {
						selected += ".missing"
					}
					args = append(args, "--sell-authority-policy", selected, "--buy-authority-policy", nextPath+".unused-missing")
				} else {
					args = append(args, "--next-authority-policy", nextPath)
				}
			} else if paired {
				// An unresolved unsigned claim must block before either policy is
				// read, even when neither paired input exists on disk.
				args = append(args, "--sell-authority-policy", nextPath+".missing", "--buy-authority-policy", nextPath+".unused-missing")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "MITHRIL_AGENT_SHADOW_RPC_URL=http://untrusted.invalid/private-test-endpoint", "MITHRIL_AGENT_MITHRIL_RPC_URL=", "MITHRIL_AGENT_PRIMARY_RPC_URL=", "MITHRIL_AGENT_SECONDARY_RPC_URL=", "MITHRIL_AGENT_JUPITER_API_KEY=invalid\nkey")
			var output, stderr bytes.Buffer
			command.Stdout, command.Stderr = &output, &stderr
			err = command.Run()
			if mode == "unreserved claim" {
				if err != nil {
					t.Fatalf("blocked CLI exit: %v (%s)", err, stderr.String())
				}
				var result map[string]any
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result["blocked_reason"] != "claim_not_reserved" || result["status"] != "strategy_step_not_authorized" || result["can_sign"] != false || result["can_submit"] != false {
					t.Fatal("CLI did not report unreserved recovery-only state")
				}
			} else if name == "paired missing selected" {
				if err == nil || output.Len() != 0 || !strings.Contains(stderr.String(), "read next authority policy") {
					t.Fatalf("missing selected policy failed at wrong boundary: %v (%s)", err, stderr.String())
				}
			} else if err == nil || output.Len() != 0 || !strings.Contains(stderr.String(), "fresh strategy RPC configuration is invalid") {
				t.Fatalf("retained acquisition did not skip fresh observation: %v (%s)", err, stderr.String())
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}

func TestStrategyStepCompiledCLIFinalizedReverseDirection(t *testing.T) {
	binary := os.Getenv("MITHRIL_AGENT_QA_CLI")
	if binary == "" {
		t.Skip("set MITHRIL_AGENT_QA_CLI to an independently built CLI")
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("QA CLI must be a clean absolute path")
	}
	path, seed, authority, recovery, next, original, at := strategyDecisionFixture(t)
	acquired, err := proposalcheck.ReadAcquisition(original, at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := path + ".second-acquisition.jsonl"
	seedStrategyAcquisition(t, acquisition, continuationCandidate(t, acquired.Candidate, "CLI finalized direction second transaction"), at.Add(-time.Second), time.Minute)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, next, acquisition, time.Minute, at); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStrategyJournalStatus(path, authority, recovery, at)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &paperReserveEvidence{Evidence: strategyClaimEvidence{jupiterEvidence: &jupiterEvidence{primary: wallet.Opening.PrimaryIdentity, secondary: wallet.Opening.SecondaryIdentity}, slot: wallet.LastFinalizedSlot, guard: next.TransactionPolicy.Jupiter.RouteGuard}, mutate: func(a *txflow.AccountEvidence) {
		a.PrimaryLamports, a.SecondaryLamports = wallet.NativeLamports, wallet.NativeLamports
	}}
	window, anchor := int64(next.TransactionPolicy.ScheduleWindowSeconds), next.TransactionPolicy.ScheduleAnchorUnix
	claim, err := PrepareStrategyWalletClaim(t.Context(), seed.WalletPath, path, authority, recovery, next, anchor+(at.Unix()-anchor)/window*window, at, time.Minute, evidence, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.PrimaryIdentity}, jupiterSlot{slot: wallet.LastFinalizedSlot, identity: wallet.Opening.SecondaryIdentity}, strategyClaimLifecycle(t, wallet))
	if err != nil {
		t.Fatal(err)
	}
	secondRecovery := continuationRecovery(t, next, claim, wallet, false)
	finalized, err := submitter.ReadJupiterFinalizedWalletEvidence(secondRecovery, claim.Request)
	if err != nil {
		t.Fatal(err)
	}
	accounted, err := FinalizeWalletClaim(seed.WalletPath, claim.ClaimPath, next, claim.Request, secondRecovery, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	state, err := ApplyStrategyContinuationOutcome(path, authority, recovery, secondRecovery, status.PendingDecisionSHA256, accounted.HeadSHA256, at.Add(2*time.Second))
	if err != nil || state.NextSell() {
		t.Fatalf("actual successful accounting did not switch to buy: %v", err)
	}
	// The reverse trade uses this fill's concrete proceeds, not either the
	// quote prediction or the wallet's preexisting token inventory.
	received := finalized.Finalized.OutputReceived
	if accounted.TokenUnits != wallet.TokenUnits+received || received == 0 {
		t.Fatal("finalized proceeds do not explain the exact token balance change")
	}
	reverse, candidate := strategyReverseCandidate(t, next, received)
	readAt := time.Unix(claim.Request.ScheduleWindowEndUnix, 0).UTC()
	ready := false
	for i, price := range []uint64{2_000_000_000, 2_100_000_000, 2_200_000_000, 2_300_000_000} {
		observed := readAt.Add(time.Duration(i) * time.Minute)
		samples := strategyOutcomeSamples(seed, observed)
		samples[0].PriceMicros, samples[1].PriceMicros = price, price
		decision, err := ObserveStrategyJournal(path, authority, recovery, observed, samples[0], samples[1], samples[2], samples[3], secondRecovery)
		if err != nil {
			t.Fatal(err)
		}
		if decision.ReadyForQuote {
			if decision.Sell || decision.InputAmount != received {
				t.Fatal("reverse decision ignored actual received inventory")
			}
			readAt, ready = observed, true
			break
		}
	}
	if !ready {
		t.Fatal("no reverse opportunity after actual finalized outcome")
	}
	reverseAcquisition := path + ".reverse-acquisition.jsonl"
	seedStrategyAcquisition(t, reverseAcquisition, candidate, readAt, time.Minute)
	if _, err := CommitStrategyJournalDecision(path, authority, recovery, reverse, reverseAcquisition, time.Minute, readAt.Add(time.Second), secondRecovery); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(path)
	authorityPath, recoveryPath, historyPath, buyPath, sellPath := filepath.Join(root, "paired-original.json"), filepath.Join(root, "paired-recovery.json"), filepath.Join(root, "paired-history.json"), filepath.Join(root, "paired-buy.json"), filepath.Join(root, "paired-sell.json")
	files := []string{path, seed.WalletPath, seed.ClaimPath, claim.ClaimPath, original, acquisition, reverseAcquisition}
	for file, value := range map[string]any{authorityPath: authority, recoveryPath: recovery, historyPath: secondRecovery, buyPath: reverse, sellPath: next} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	before := strategyReconcileSnapshot(t, files...)
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "buy selected unused sell absent", true: "wrong selected direction"}[wrong], func(t *testing.T) {
			selected := buyPath
			if wrong {
				selected = sellPath
			}
			args := []string{"proposal", "strategy", "--operation", "step", "--strategy", path, "--inventory", seed.WalletPath, "--authority-policy", authorityPath, "--submitter-policy", recoveryPath, "--historical-submitter-policy", historyPath, "--buy-authority-policy", selected, "--sell-authority-policy", sellPath + ".unused-missing", "--max-decision-age-seconds", "60", "--max-acquisition-age-seconds", "60"}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(os.Environ(), "MITHRIL_AGENT_SHADOW_RPC_URL=http://untrusted.invalid/private-test-endpoint", "MITHRIL_AGENT_MITHRIL_RPC_URL=", "MITHRIL_AGENT_PRIMARY_RPC_URL=", "MITHRIL_AGENT_SECONDARY_RPC_URL=", "MITHRIL_AGENT_JUPITER_API_KEY=invalid\nkey")
			var output, stderr bytes.Buffer
			command.Stdout, command.Stderr = &output, &stderr
			err := command.Run()
			want := "fresh strategy RPC configuration is invalid"
			if wrong {
				want = "selected strategy authority policy differs from the verified buy/sell direction"
			}
			if err == nil || output.Len() != 0 || !strings.Contains(stderr.String(), want) {
				t.Fatalf("reverse routing failed at wrong boundary: %v (%s), want %q", err, stderr.String(), want)
			}
			assertStrategyReconcileUnchanged(t, before)
		})
	}
}
