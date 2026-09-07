package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

func TestProposalStrategyHelpAndWriterError(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalStrategy(t.Context(), []string{"--help"}, &output, time.Now); err != nil {
		t.Fatal(err)
	}
	var dispatched bytes.Buffer
	if err := run([]string{"proposal", "strategy", "--help"}, &dispatched); err != nil || !bytes.Equal(output.Bytes(), dispatched.Bytes()) {
		t.Fatalf("strategy help dispatch: %v", err)
	}
	for _, word := range []string{"show", "initialize", "observe", "account", "prepare", "acquire", "retire-acquisition", "cancel-decision", "--acquisition", "--strategy", "--historical-submitter-policy", "--buy-authority-policy", "--sell-authority-policy"} {
		if !strings.Contains(output.String(), word) {
			t.Fatalf("strategy help omitted %q", word)
		}
	}
	want := errors.New("strategy output failed")
	if err := runProposalStrategy(t.Context(), []string{"--help"}, writerFunc(func([]byte) (int, error) { return 0, want }), time.Now); !errors.Is(err, want) {
		t.Fatalf("strategy help writer error: %v", err)
	}
}

func TestProposalStrategyRejectsInvalidArgumentsWithoutState(t *testing.T) {
	root := t.TempDir()
	strategy := filepath.Join(root, "strategy.jsonl")
	authority := filepath.Join(root, "authority.json")
	recovery := filepath.Join(root, "submitter.json")
	base := []string{"--strategy", strategy, "--authority-policy", authority, "--submitter-policy", recovery}
	for _, extra := range [][]string{
		{"--operation", "send"}, {"extra"}, {"--strategy", "relative"},
		{"--strategy", authority}, {"--authority-policy", recovery},
		{"--candidate", filepath.Join(root, "candidate.json")},
		{"--operation", "initialize"}, {"--operation", "observe"}, {"--operation", "account"},
		{"--operation", "prepare"}, {"--operation", "acquire"},
		{"--operation", "retire-acquisition"},
		{"--operation", "retire-acquisition", "--acquisition", strategy},
		{"--operation", "retire-acquisition", "--schedule-start-unix", "1"},
		{"--operation", "retire-acquisition", "--max-acquisition-age-seconds", "60"},
		{"--operation", "retire-acquisition", "--next-submitter-policy", recovery},
		{"--operation", "cancel-decision"},
		{"--operation", "cancel-decision", "--decision-sha256", strings.Repeat("a", 64)},
		{"--operation", "cancel-decision", "--inventory", filepath.Join(root, "wallet.jsonl"), "--decision-sha256", "invalid"},
		{"--operation", "cancel-decision", "--inventory", strategy, "--decision-sha256", strings.Repeat("a", 64)},
		{"--operation", "cancel-decision", "--max-decision-age-seconds", "60"},
		{"--operation", "cancel-decision", "--acquisition", filepath.Join(root, "acquisition.jsonl")},
		{"--operation", "cancel-decision", "--next-authority-policy", authority},
		{"--operation", "cancel-decision", "--next-submitter-policy", recovery},
		{"--max-decision-age-seconds", "0"}, {"--max-decision-age-seconds", "-1"},
		{"--max-decision-age-seconds", "18446744073709551615"},
		{"--max-acquisition-age-seconds", "0"}, {"--max-acquisition-age-seconds", "18446744073709551615"},
		{"--historical-submitter-policy", "relative"},
		{"--historical-submitter-policy", recovery},
		{"--historical-submitter-policy", filepath.Join(root, "history.json"), "--historical-submitter-policy", filepath.Join(root, "history.json")},
	} {
		var output bytes.Buffer
		if err := runProposalStrategy(t.Context(), append(append([]string{}, base...), extra...), &output, time.Now); err == nil || output.Len() != 0 {
			t.Fatalf("invalid strategy arguments accepted %v: %v", extra, err)
		} else if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") || strings.HasPrefix(err.Error(), "read ") {
			t.Fatalf("invalid arguments reached file reads %v: %v", extra, err)
		}
	}
	for _, path := range []string{strategy, authority, recovery} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("argument failure created %s: %v", path, err)
		}
	}
}

func TestProposalStrategyMissingHistoryIsReadOnly(t *testing.T) {
	authority, _, recovery := testJupiterPolicySet(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	strategy := filepath.Join(root, "missing-strategy.jsonl")
	authorityPath, recoveryPath := filepath.Join(root, "authority.json"), filepath.Join(root, "submitter.json")
	writeJSON(t, authorityPath, authority)
	writeJSON(t, recoveryPath, recovery)
	before := make(map[string][]byte)
	for _, path := range []string{authorityPath, recoveryPath} {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"MITHRIL_AGENT_MITHRIL_RPC_URL", "MITHRIL_AGENT_PRIMARY_RPC_URL", "MITHRIL_AGENT_SECONDARY_RPC_URL", jupiterAPIKeyEnvironment} {
		t.Setenv(key, "")
	}
	args := []string{"--strategy", strategy, "--authority-policy", authorityPath, "--submitter-policy", recoveryPath}
	inventory, acquisition := filepath.Join(root, "missing-inventory.jsonl"), filepath.Join(root, "missing-acquisition.jsonl")
	for _, extra := range [][]string{
		{},
		{"--operation", "retire-acquisition", "--inventory", inventory, "--acquisition", acquisition, "--next-authority-policy", authorityPath},
		{"--operation", "cancel-decision", "--inventory", inventory, "--decision-sha256", strings.Repeat("a", 64)},
	} {
		var output bytes.Buffer
		if err := runProposalStrategy(t.Context(), append(append([]string{}, args...), extra...), &output, time.Now); err == nil || output.Len() != 0 {
			t.Fatalf("operation accepted missing history: %v", extra)
		}
	}
	for _, path := range []string{strategy, inventory, acquisition} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("operation created missing history %s: %v", path, err)
		}
	}
	for path, want := range before {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(want, after) {
			t.Fatalf("operation changed protected input: %v", err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("operation created side effects: entries=%d, %v", len(entries), err)
	}
}

func TestProposalStrategyHistoryCountBound(t *testing.T) {
	root := t.TempDir()
	args := []string{"--strategy", filepath.Join(root, "strategy"), "--authority-policy", filepath.Join(root, "authority"), "--submitter-policy", filepath.Join(root, "submitter")}
	for i := 0; i < 65; i++ {
		args = append(args, "--historical-submitter-policy", filepath.Join(root, strings.Repeat("x", i+1)+".json"))
	}
	var output bytes.Buffer
	if err := runProposalStrategy(t.Context(), args, &output, time.Now); err == nil || output.Len() != 0 {
		t.Fatal("unbounded historical policy list accepted")
	} else if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") || strings.HasPrefix(err.Error(), "read ") {
		t.Fatalf("oversized history reached policy reads: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid history created state: %v", err)
	}
}

func TestProposalStrategyClaimProjectionDoesNotExposeRequest(t *testing.T) {
	claim := policyauthority.WalletPaperClaim{ClaimPath: "/private/claim.jsonl", Recovered: true,
		Request: signer.Request{ActionID: strings.Repeat("a", 64), MessageBase64: "private unsigned transaction"}}
	var output bytes.Buffer
	if err := writeStrategyClaim(&output, claim); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 8 || result["status"] != "unsigned_claim_not_authorized" || result["action_id"] != claim.Request.ActionID || result["recovered"] != true || result["pending"] != false || result["can_sign"] != false || result["can_submit"] != false || strings.Contains(output.String(), claim.Request.MessageBase64) {
		t.Fatal("claim projection exposed request or fabricated reservation/authority")
	}
	want := errors.New("projection writer failure")
	if err := writeStrategyClaim(writerFunc(func([]byte) (int, error) { return 0, want }), claim); !errors.Is(err, want) {
		t.Fatalf("claim projection writer error: %v", err)
	}
}

func TestProposalStrategyRequiresClock(t *testing.T) {
	for _, operation := range []string{"show", "initialize", "observe", "account", "prepare", "acquire", "cancel-decision"} {
		var output bytes.Buffer
		if err := runProposalStrategy(t.Context(), []string{"--operation", operation}, &output, nil); err == nil || !strings.Contains(err.Error(), "clock") || output.Len() != 0 {
			t.Fatalf("%s missing clock: %v", operation, err)
		}
	}
}

func TestProposalStrategyBuilderDefersConfiguration(t *testing.T) {
	t.Setenv(jupiterAPIKeyEnvironment, "invalid\nkey")
	var calls atomic.Int32
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("unexpected strategy builder network access")
	}}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	builder := proposalStrategyBuilder{}
	if calls.Load() != 0 {
		t.Fatal("builder construction contacted a provider")
	}
	if _, err := builder.Build(t.Context(), jupiterquote.Request{}); err == nil || !strings.Contains(err.Error(), "Jupiter API key") {
		t.Fatalf("fresh build did not validate configured key: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid builder configuration reached network transport")
	}
}
