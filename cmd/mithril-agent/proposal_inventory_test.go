package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestProposalInventoryInitializeThenShowWithoutRPC(t *testing.T) {
	authority, _, _ := testJupiterPolicySet(t)
	owner, mint := authority.TransactionPolicy.Jupiter.Owner, authority.TransactionPolicy.Jupiter.OutputMint
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	mintKey, err := solana.Decode32(mint)
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := solana.Decode32(owner)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch request.Method {
		case "getGenesisHash":
			result = solana.MainnetBetaGenesisHash
		case "getSlot":
			result = uint64(100)
		case "getAccountInfo":
			if len(request.Params) != 2 || !bytes.Contains(request.Params[1], []byte(`"commitment":"finalized"`)) {
				t.Error("non-finalized wallet read")
				return
			}
			var queried string
			if err := json.Unmarshal(request.Params[0], &queried); err != nil {
				t.Error(err)
				return
			}
			program, lamports, data := orcaswap.SystemProgram, uint64(9007199254740993), []byte{}
			if queried == address {
				program, lamports, data = orcaswap.TokenProgram, 2039280, make([]byte, 165)
				copy(data[:32], mintKey[:])
				copy(data[32:64], ownerKey[:])
				binary.LittleEndian.PutUint64(data[64:72], 200)
				data[108] = 1
			} else if queried != owner {
				t.Error("unexpected wallet address")
				return
			}
			result = map[string]any{"context": map[string]any{"slot": 101}, "value": map[string]any{"owner": program, "lamports": lamports, "space": len(data), "executable": false, "data": []any{base64.StdEncoding.EncodeToString(data), "base64"}}}
		default:
			t.Errorf("unexpected RPC %s", request.Method)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Error(err)
		}
	})
	a, b := httptest.NewServer(handler), httptest.NewServer(handler)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	old := newPacedExternalRPC
	newPacedExternalRPC = func(endpoint string, _ time.Duration) (*solanarpc.Client, error) {
		return solanarpc.New(endpoint, nil, true)
	}
	t.Cleanup(func() { newPacedExternalRPC = old })
	primary, secondary, err := openEvidenceProviders(a.URL, b.URL)
	if err != nil {
		t.Fatal(err)
	}
	authority.JupiterProviders.PrimaryOriginSHA256 = primary.Identity()
	authority.JupiterProviders.SecondaryOriginSHA256 = secondary.Identity()
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", a.URL)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", b.URL)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	policy, inventory := filepath.Join(root, "authority.json"), filepath.Join(root, "inventory.jsonl")
	writeJSON(t, policy, authority)
	args := []string{"--inventory", inventory, "--authority-policy", policy}
	var initialized bytes.Buffer
	if err := runProposalInventory(t.Context(), append(append([]string{}, args...), "--operation", "initialize"), &initialized, time.Now); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(initialized.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["native_lamports"] != "9007199254740993" || result["token_units"] != "200" || result["can_sign"] != false || result["can_submit"] != false || result["pending"] != false {
		t.Fatalf("inventory output = %v", result)
	}
	before, err := os.ReadFile(inventory)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "")
	var shown bytes.Buffer
	if err := runProposalInventory(t.Context(), args, &shown, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(inventory)
	if err != nil || !bytes.Equal(before, after) || !bytes.Equal(initialized.Bytes(), shown.Bytes()) {
		t.Fatal("show changed inventory or output")
	}
	var missingInputs bytes.Buffer
	if err := runProposalInventory(t.Context(), append(append([]string{}, args...), "--operation", "admit"), &missingInputs, time.Now); err == nil || !strings.Contains(err.Error(), "fresh admission requires") || missingInputs.Len() != 0 {
		t.Fatalf("fresh admission without inputs = %v", err)
	}
	// Retained requests must be recoverable without fresh paper files or RPC,
	// including after their original schedule expires.
	candidate := mainnetCandidateForPolicy(t, *authority.TransactionPolicy.Jupiter)
	start := authority.TransactionPolicy.ScheduleAnchorUnix
	action, err := jupiterswap.ComputeActionID(authority.TransactionPolicy.ProfileFingerprint, start)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain: jupiterswap.RequestDomain, Cluster: authority.TransactionPolicy.Cluster,
		Profile: authority.TransactionPolicy.Profile, ProfileVersion: authority.TransactionPolicy.ProfileVersion,
		ProfileFingerprint: authority.TransactionPolicy.ProfileFingerprint, ActionID: action,
		ScheduleWindowStartUnix: start, ScheduleWindowEndUnix: start + int64(authority.TransactionPolicy.ScheduleWindowSeconds),
		MessageBase64: candidate.MessageBase64, RecentBlockhash: solana.Encode(bytes.Repeat([]byte{9}, 32)),
		BlockhashContextSlot: 100, FeeLamports: 5000, FeeMinContextSlot: 100,
		PrimaryFeeContextSlot: 100, SecondaryFeeContextSlot: 100,
		ObservedBlockHeight: candidate.LastValidBlockHeight - 1, LastValidBlockHeight: candidate.LastValidBlockHeight,
		JupiterCandidate: &candidate, JupiterProviders: authority.JupiterProviders,
	}
	if _, err := signer.ValidateJupiterRequest(authority.TransactionPolicy, request); err != nil {
		t.Fatal(err)
	}
	const event = "paper.unsigned-request-claim-v1"
	hash := func(domain string, value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(append([]byte(event+"/"+domain+"\x00"), raw...))
		return hex.EncodeToString(digest[:])
	}
	claim := struct {
		PaperIntentSHA256 string          `json:"paper_intent_sha256"`
		PolicySHA256      string          `json:"policy_sha256"`
		RequestSHA256     string          `json:"request_sha256"`
		MaxDecisionAgeNS  int64           `json:"max_decision_age_ns,string"`
		AcquisitionSHA256 string          `json:"acquisition_sha256"`
		Request           *signer.Request `json:"request,omitempty"`
	}{strings.Repeat("a", 64), hash("policy", authority), hash("request", request), int64(time.Minute), strings.Repeat("b", 64), &request}
	claimPath := inventory + ".claim-" + result["head_sha256"].(string) + ".jsonl"
	store, err := journal.OpenStrict(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(time.Now().UTC(), event, action, claim); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	claimBefore, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "")
	t.Setenv(jupiterAPIKeyEnvironment, "")
	for _, operation := range []string{"admit", "acquire"} {
		var recovered bytes.Buffer
		if err := runProposalInventory(t.Context(), append(append([]string{}, args...), "--operation", operation), &recovered, time.Now); err != nil {
			t.Fatal(err)
		}
		var recovery map[string]any
		if err := json.Unmarshal(recovered.Bytes(), &recovery); err != nil {
			t.Fatal(err)
		}
		if recovery["recovered"] != true || recovery["can_sign"] != false || recovery["can_submit"] != false ||
			recovery["pending"] != false || recovery["action_id"] != action || len(recovery) != 9 || bytes.Contains(recovered.Bytes(), []byte("message_base64")) {
			t.Fatalf("%s recovery = %s", operation, recovered.Bytes())
		}
		for path, want := range map[string][]byte{inventory: before, claimPath: claimBefore} {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s recovery changed %s: %v", operation, filepath.Base(path), err)
			}
		}
	}
	// Both strategy entry points must recover this same existing claim before
	// loading absent original history or unused fresh provider configuration.
	t.Setenv(jupiterAPIKeyEnvironment, "invalid\nunused")
	for _, operation := range []string{"prepare", "acquire"} {
		var recovered bytes.Buffer
		strategyArgs := []string{"--operation", operation, "--inventory", inventory,
			"--next-authority-policy", policy, "--historical-submitter-policy", filepath.Join(root, "unavailable-history.json")}
		if err := runProposalStrategy(t.Context(), strategyArgs, &recovered, func() time.Time { return time.Now().Add(24 * time.Hour) }); err != nil {
			t.Fatalf("strategy %s recovery required fresh dependencies: %v", operation, err)
		}
		var recovery map[string]any
		if err := json.Unmarshal(recovered.Bytes(), &recovery); err != nil {
			t.Fatal(err)
		}
		if recovery["recovered"] != true || recovery["pending"] != false || recovery["can_sign"] != false || recovery["can_submit"] != false || recovery["action_id"] != action || recovery["claim_path"] != claimPath || bytes.Contains(recovered.Bytes(), []byte("message_base64")) {
			t.Fatalf("strategy %s recovery = %s", operation, recovered.Bytes())
		}
		for path, want := range map[string][]byte{inventory: before, claimPath: claimBefore} {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("strategy %s recovery changed retained state: %v", operation, err)
			}
		}
	}
}

func TestProposalInventoryAcquireRejectsCallerReceiptPaths(t *testing.T) {
	root := t.TempDir()
	inventory := filepath.Join(root, "inventory.jsonl")
	for _, flagName := range []string{"--candidate", "--acquisition"} {
		var output bytes.Buffer
		args := []string{"--operation", "acquire", "--inventory", inventory,
			"--authority-policy", filepath.Join(root, "authority.json"), flagName, filepath.Join(root, "receipt.json")}
		if err := runProposalInventory(t.Context(), args, &output, time.Now); err == nil ||
			!strings.Contains(err.Error(), "does not accept candidate or acquisition paths") || output.Len() != 0 {
			t.Fatalf("%s rejection = %v", flagName, err)
		}
	}
	if _, err := os.Stat(inventory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid acquire created inventory")
	}
}

func TestProposalInventoryRejectsInvalidArgumentsWithoutCreatingState(t *testing.T) {
	root := t.TempDir()
	path, policy := filepath.Join(root, "inventory.jsonl"), filepath.Join(root, "authority.json")
	base := []string{"--inventory", path, "--authority-policy", policy}
	for _, extra := range [][]string{
		{"--operation", "send"}, {"--operation", "reserve"}, {"--operation", "account"},
		{"--claim", filepath.Join(root, "claim.jsonl")}, {"--submitter-policy", policy},
		{"--operation", "reserve", "--claim", path}, {"extra"}, {"--inventory", "relative"},
		{"--operation", "admit", "--claim", filepath.Join(root, "claim.jsonl")},
		{"--paper-policy", filepath.Join(root, "paper.json")}, {"--max-decision-age-seconds", "0"},
	} {
		var output bytes.Buffer
		if err := runProposalInventory(t.Context(), append(append([]string{}, base...), extra...), &output, time.Now); err == nil || output.Len() != 0 {
			t.Fatalf("accepted invalid arguments %v", extra)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created inventory on argument error")
	}
}

func TestProposalInventoryShowMissingIsReadOnly(t *testing.T) {
	authority, _, submission := testJupiterPolicySet(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy, inventory := filepath.Join(root, "authority.json"), filepath.Join(root, "inventory.jsonl")
	writeJSON(t, policy, authority)
	before, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runProposalInventory(t.Context(), []string{"--inventory", inventory, "--authority-policy", policy}, &output, time.Now); err == nil || output.Len() != 0 {
		t.Fatal("missing inventory accepted")
	}
	if _, err := os.Stat(inventory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("show created inventory")
	}
	after, err := os.ReadFile(policy)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("show changed policy")
	}
	claim, submitterPath := filepath.Join(root, "missing-claim.jsonl"), filepath.Join(root, "submitter.json")
	writeJSON(t, submitterPath, submission)
	output.Reset()
	if err := runProposalInventory(t.Context(), []string{"--operation", "account", "--inventory", inventory,
		"--authority-policy", policy, "--claim", claim, "--submitter-policy", submitterPath}, &output, time.Now); err == nil || output.Len() != 0 {
		t.Fatal("account accepted missing original claim")
	}
	for _, path := range []string{inventory, claim} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("account created state without original claim")
		}
	}
}

func TestProposalInventoryHelpAndWriterError(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalInventory(t.Context(), []string{"--help"}, &output, time.Now); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"show|initialize|reserve|account|admit|acquire", "forbids --candidate", "derived private receipt", "No operation releases", "signs, submits"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("help omitted %q", text)
		}
	}
	want := errors.New("output failed")
	if err := runProposalInventory(t.Context(), []string{"--help"}, writerFunc(func([]byte) (int, error) { return 0, want }), time.Now); !errors.Is(err, want) {
		t.Fatalf("writer error = %v", err)
	}
}

func TestProposalInventoryMutationRequiresClock(t *testing.T) {
	root := t.TempDir()
	for _, operation := range []string{"initialize", "reserve", "account", "admit", "acquire"} {
		args := []string{"--inventory", filepath.Join(root, "inventory"), "--authority-policy", filepath.Join(root, "authority"), "--operation", operation}
		if operation == "reserve" || operation == "account" {
			args = append(args, "--claim", filepath.Join(root, "claim"))
		}
		if operation == "account" {
			args = append(args, "--submitter-policy", filepath.Join(root, "submitter"))
		}
		if err := runProposalInventory(t.Context(), args, &bytes.Buffer{}, nil); err == nil || !strings.Contains(err.Error(), "clock") {
			t.Fatalf("%s clock error = %v", operation, err)
		}
	}
}
