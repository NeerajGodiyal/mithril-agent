package policyauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func TestWalletAcquisitionCompiledCLIRetainedReceiptIgnoresBuilderKey(t *testing.T) {
	binary := os.Getenv("MITHRIL_AGENT_QA_CLI")
	if binary == "" {
		t.Skip("set MITHRIL_AGENT_QA_CLI to an independently built CLI")
	}
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("QA CLI must be a clean absolute path")
	}
	f := newPaperRequestFixture(t)
	f.now = time.Now().UTC()
	window := int64(f.policy.TransactionPolicy.ScheduleWindowSeconds)
	f.start = f.policy.TransactionPolicy.ScheduleAnchorUnix + (f.now.Unix()-f.policy.TransactionPolicy.ScheduleAnchorUnix)/window*window
	f.ticks[0].At, f.ticks[0].DecisionQuote.ReceivedAt = f.now, f.now
	var err error
	f.bounds.EvidenceSHA256, err = proposalcheck.PaperEvidenceSHA256(f.ticks)
	if err != nil {
		t.Fatal(err)
	}
	const primaryURL, secondaryURL = "https://primary.invalid", "https://secondary.invalid"
	primary, err := solanarpc.NewPaced(primaryURL, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := solanarpc.NewPaced(secondaryURL, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.policy.JupiterProviders.PrimaryOriginSHA256, f.policy.JupiterProviders.SecondaryOriginSHA256 = primary.Identity(), secondary.Identity()
	f.primary.identity, f.secondary.identity = primary.Identity(), secondary.Identity()
	evidence := f.evidence.(*paperReserveEvidence).Evidence.(*jupiterEvidence)
	evidence.primary, evidence.secondary = primary.Identity(), secondary.Identity()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	walletPath := filepath.Join(root, "inventory.jsonl")
	account, err := orcaswap.AssociatedTokenAddress(f.paper.Observe, f.candidate.Policy.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	opening, err := initializeWalletInventory(walletPath, f.policy, f.now, func(string, string) (txflow.WalletObservation, error) {
		return txflow.WalletObservation{Owner: f.paper.Observe, TokenMint: f.candidate.Policy.OutputMint, TokenAccount: account,
			GenesisHash: solana.MainnetBetaGenesisHash, PrimaryIdentity: primary.Identity(), SecondaryIdentity: secondary.Identity(),
			NativeLamports: 20_000_000, TokenUnits: 100, MinimumContextSlot: 100, MaximumContextSlot: 100 + proposalcheck.MaxEvidenceSlotSkew,
			NativePrimarySlot: 100, NativeSecondarySlot: 100, TokenPrimarySlot: 100, TokenSecondarySlot: 100}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claimPath := walletPath + ".claim-" + opening.HeadSHA256 + ".jsonl"
	acquisition := claimPath + ".acquisition.jsonl"
	build := WalletAdmissionBuildForTest(t, f.candidate)
	// Offline builder stands in for the host-observed upstream receipt.
	build.Quote.ReceivedAt = time.Now().UTC()
	build.Quote.ResponseSHA256 = strings.Repeat("a", 64)
	_, err = proposalcheck.CheckAndRecordAcquisition(t.Context(), acquisition, f.maxAcquisitionAge,
		strategyAcquisitionBuilder(func(context.Context, jupiterquote.Request) (jupiterquote.BuildResult, error) { return build, nil }),
		f.evidence, f.primary, f.secondary, f.policy.JupiterProviders.PrimaryTrustDomain, f.policy.JupiterProviders.SecondaryTrustDomain,
		f.policy.JupiterProviders.ArchiveProbeSignature, f.candidate.Policy, f.candidate.Request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposalcheck.ReadAcquiredCandidate(acquisition, time.Now().UTC(), f.maxAcquisitionAge); err != nil {
		t.Fatal(err)
	}
	policyPath, paperPath, boundsPath := filepath.Join(root, "authority.json"), filepath.Join(root, "paper.json"), filepath.Join(root, "bounds.json")
	for path, value := range map[string]any{policyPath: f.policy, paperPath: f.paper, boundsPath: f.bounds} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := filepath.Join(root, "shadow-"+f.now.Format("2006-01-02")+".jsonl")
	store, err := journal.OpenStrict(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(f.now, shadow.EventOpened, "", shadow.Opening{Version: shadow.JournalVersionFor(f.paper), PolicySHA256: f.bounds.PolicySHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(f.now, f.ticks[0].Event, "", f.ticks[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := strategyReconcileSnapshot(t, walletPath, acquisition, journalPath)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "getGenesisHash" {
			t.Error("unexpected readiness RPC")
			return
		}
		// Stop at local genesis verification, before external RPCs or signing.
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": solana.DevnetGenesisHash})
	}))
	defer server.Close()
	for _, key := range []string{"invalid\nunused-key", ""} {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		command := exec.CommandContext(ctx, binary, "proposal", "inventory", "--operation", "acquire", "--inventory", walletPath,
			"--authority-policy", policyPath, "--paper-policy", paperPath, "--paper-journal", journalPath, "--paper-bounds", boundsPath,
			"--schedule-start-unix", strconv.FormatInt(f.start, 10), "--max-decision-age-seconds", "7200", "--max-acquisition-age-seconds", "7200")
		command.Env = append(os.Environ(), "MITHRIL_AGENT_MITHRIL_RPC_URL="+server.URL, "MITHRIL_AGENT_PRIMARY_RPC_URL="+primaryURL,
			"MITHRIL_AGENT_SECONDARY_RPC_URL="+secondaryURL, "MITHRIL_AGENT_JUPITER_API_KEY="+key)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err = command.Run()
		cancel()
		if err == nil || !strings.Contains(stderr.String(), "verify Mainnet RPC identities") || stdout.Len() != 0 {
			t.Fatalf("retained receipt did not reach readiness: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("readiness calls=%d, want 2", calls.Load())
	}
	if !reflect.DeepEqual(before, strategyReconcileSnapshot(t, walletPath, acquisition, journalPath)) {
		t.Fatal("retry changed original evidence")
	}
	if _, found, err := RecoverWalletPaperClaim(walletPath, f.policy, time.Now().UTC()); err != nil || found {
		t.Fatalf("unexpected retained claim: found=%v err=%v", found, err)
	}
}

func TestWalletAcquisitionSchedulePreflightPreservesHead(t *testing.T) {
	path, _, policy, request, opening, _, now := walletAccountingFixture(t)
	before := walletAdmissionBytes(t, path)
	claim := path + ".claim-" + opening.HeadSHA256 + ".jsonl"
	window := int64(policy.TransactionPolicy.ScheduleWindowSeconds)
	overflowStart := int64(math.MaxInt64) - (int64(math.MaxInt64)-policy.TransactionPolicy.ScheduleAnchorUnix)%window
	for _, test := range []struct {
		name  string
		start int64
		at    time.Time
		want  string
	}{
		{"misaligned", request.ScheduleWindowStartUnix + 1, now, "Jupiter signing schedule window is outside policy"},
		{"before anchor", policy.TransactionPolicy.ScheduleAnchorUnix - 1, now, "Jupiter signing schedule window is outside policy"},
		{"overflow", overflowStart, now, "Jupiter signing schedule window is outside policy"},
		{"expired", request.ScheduleWindowStartUnix, time.Unix(request.ScheduleWindowEndUnix, 0).UTC(), "signing request schedule window does not include current UTC time"},
		{"future window", request.ScheduleWindowEndUnix, now, "signing request schedule window does not include current UTC time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// All provider/builder inputs are absent: a scheduling refusal must
			// precede those dependencies and any durable acquisition or claim.
			_, err := AcquireWalletPaperClaim(t.Context(), path, policy, shadow.Policy{}, nil,
				proposalcheck.PaperIntentBounds{}, jupiterquote.Request{}, test.start, test.at,
				time.Minute, time.Minute, nil, nil, nil, nil, nil)
			if err == nil || err.Error() != test.want {
				t.Fatalf("schedule refusal = %v; want %q", err, test.want)
			}
			for _, artifact := range []string{claim, claim + ".acquisition.jsonl"} {
				if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid schedule created %s: %v", artifact, err)
				}
			}
			if !bytes.Equal(before, walletAdmissionBytes(t, path)) {
				t.Fatal("schedule refusal changed inventory")
			}
		})
	}
}

func TestWalletAcquisitionRecoveryIgnoresNewInvalidSchedule(t *testing.T) {
	path, _, policy, request, opening, _, now := walletAccountingFixture(t)
	claim := path + ".claim-" + opening.HeadSHA256 + ".jsonl"
	seedRetainedWalletClaim(t, claim, policy, request, now)
	before, claimBefore := walletAdmissionBytes(t, path), walletAdmissionBytes(t, claim)
	got, err := AcquireWalletPaperClaim(t.Context(), path, policy, shadow.Policy{}, nil,
		proposalcheck.PaperIntentBounds{}, jupiterquote.Request{}, 0,
		time.Unix(request.ScheduleWindowEndUnix, 0).UTC().Add(time.Hour), 0, 0, nil, nil, nil, nil, nil)
	if err != nil || !got.Recovered || !reflect.DeepEqual(got.Request, request) {
		t.Fatalf("retained request did not recover ahead of fresh validation: %+v, %v", got, err)
	}
	if !bytes.Equal(before, walletAdmissionBytes(t, path)) || !bytes.Equal(claimBefore, walletAdmissionBytes(t, claim)) {
		t.Fatal("recovery changed original evidence")
	}
}
