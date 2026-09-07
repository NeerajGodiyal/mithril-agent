package policyauthority

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestStrategyRunnerCompiledCLIRepeatedBlockedClaim(t *testing.T) {
	binary := os.Getenv("MITHRIL_AGENT_QA_CLI")
	if binary == "" {
		t.Skip("set MITHRIL_AGENT_QA_CLI to an independently built CLI")
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("QA CLI must be a clean absolute path")
	}
	temporaryRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryRoot)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := filepath.Abs("../deploy/unsigned-strategy/run-once.py")
	if err != nil {
		t.Fatal(err)
	}
	path, seed, authority, recovery, next, acquisition, at := strategyDecisionFixture(t)
	wallet, err := ReadWalletInventory(seed.WalletPath, next)
	if err != nil {
		t.Fatal(err)
	}
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
	claimPath := seed.WalletPath + ".claim-" + wallet.HeadSHA256 + ".jsonl"
	if _, err := ReadClaimedPaperRequest(claimPath, next); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(path)
	authorityPath, recoveryPath := filepath.Join(root, "runner-authority.json"), filepath.Join(root, "runner-recovery.json")
	for file, value := range map[string]any{authorityPath: authority, recoveryPath: recovery} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := filepath.Join(root, "runner")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	before := strategyReconcileSnapshot(t, path, seed.WalletPath, seed.ClaimPath, claimPath, acquisition, authorityPath, recoveryPath)
	args := []string{"--strategy", path, "--inventory", seed.WalletPath, "--authority-policy", authorityPath,
		"--submitter-policy", recoveryPath, "--next-submitter-policy", recoveryPath,
		"--buy-authority-policy", filepath.Join(root, "unused-buy.json"), "--sell-authority-policy", filepath.Join(root, "unused-sell.json")}
	const invoke = `import importlib.util, sys
spec = importlib.util.spec_from_file_location("unsigned_runner", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
raise SystemExit(module.run_once(sys.argv[2], sys.argv[4:], timeout_seconds=15, binary=sys.argv[3]))
`
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		command := exec.CommandContext(ctx, python, append([]string{"-B", "-c", invoke, wrapper, stateDir, binary}, args...)...)
		command.Env = append(os.Environ(), "MITHRIL_AGENT_MITHRIL_RPC_URL=", "MITHRIL_AGENT_PRIMARY_RPC_URL=", "MITHRIL_AGENT_SECONDARY_RPC_URL=", "MITHRIL_AGENT_JUPITER_API_KEY=invalid\nkey")
		output, err := command.CombinedOutput()
		cancel()
		if err != nil || len(output) != 0 {
			t.Fatalf("runner invocation %d: %v (%s)", i, err, output)
		}
		statusPath := filepath.Join(stateDir, "status.json")
		raw, err := os.ReadFile(statusPath)
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]any
		if err := json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
		if status["state"] != "blocked" || status["blocked_reason"] != "claim_not_reserved" || status["can_sign"] != false || status["can_submit"] != false {
			t.Fatalf("runner invocation %d did not preserve unsigned blocked state: %s", i, raw)
		}
		info, err := os.Stat(statusPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("runner status is not private: %v", err)
		}
		assertStrategyReconcileUnchanged(t, before)
	}
}
