package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestStrategyPriceReadersValidatePinnedSourcesWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("unexpected strategy observation network access")
	}}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	for _, name := range []string{"valid", "legacy SOL", "current missing market", "nil clock", "unsupported market", "market primary", "market secondary", "quote primary", "quote secondary", "missing quote", "endpoint"} {
		t.Run(name, func(t *testing.T) {
			policy := validShadowPolicy()
			policy.Trigger.PrimarySourceSHA256 = pricesource.PythPushIdentitySHA256()
			policy.Trigger.SecondarySourceSHA256 = pricesource.KrakenSOLIdentitySHA256()
			clock := time.Now
			endpoint := "https://rpc.invalid"
			switch name {
			case "legacy SOL":
				policy.Version, policy.Market = shadow.LegacyVersion, ""
				if err := policy.ValidateForRun(); err != nil {
					t.Fatalf("legacy fixture is invalid: %v", err)
				}
			case "current missing market":
				policy.Market = ""
			case "nil clock":
				clock = nil
			case "unsupported market":
				policy.Market = shadow.MarketJUPUSDC
			case "market primary":
				policy.Trigger.PrimarySourceSHA256 = strings.Repeat("a", 64)
			case "market secondary":
				policy.Trigger.SecondarySourceSHA256 = strings.Repeat("b", 64)
			case "quote primary":
				policy.QuotePeg.PrimarySourceSHA256 = strings.Repeat("c", 64)
			case "quote secondary":
				policy.QuotePeg.SecondarySourceSHA256 = strings.Repeat("d", 64)
			case "missing quote":
				policy.QuotePeg = nil
			case "endpoint":
				endpoint = "http://untrusted.invalid/private-endpoint-token"
			}
			t.Setenv(shadowEndpointEnvironment, endpoint)
			before, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			readers, err := strategyPriceReaders(policy, clock)
			if name != "valid" && name != "legacy SOL" {
				if err == nil {
					t.Fatalf("accepted invalid %s", name)
				}
				if strings.Contains(err.Error(), endpoint) {
					t.Fatal("endpoint leaked in configuration error")
				}
				for _, reader := range readers {
					if reader != nil {
						t.Fatal("failed configuration returned usable reader")
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := []string{pricesource.PythPushIdentitySHA256(), pricesource.KrakenSOLIdentitySHA256(), pricesource.PythPushUSDCIdentitySHA256(), pricesource.KrakenIdentitySHA256()}
				for i, reader := range readers {
					identified, ok := reader.(interface{ IdentitySHA256() string })
					if !ok || identified.IdentitySHA256() != want[i] {
						t.Fatalf("reader %d is not the pinned source", i)
					}
				}
			}
			if calls.Load() != 0 {
				t.Fatal("source construction performed network reads")
			}
			after, err := json.Marshal(policy)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("source selection changed the original policy")
			}
		})
	}
}

func TestProposalStrategyStepRejectsManualInputs(t *testing.T) {
	root := t.TempDir()
	base := []string{"--operation", "step", "--strategy", filepath.Join(root, "strategy"), "--inventory", filepath.Join(root, "wallet"), "--authority-policy", filepath.Join(root, "authority"), "--submitter-policy", filepath.Join(root, "submitter")}
	for _, extra := range [][]string{{"--schedule-start-unix", "123"}, {"--observation", filepath.Join(root, "manual-observation")}, {"--claim", filepath.Join(root, "claim")}, {"--accounting-sha256", strings.Repeat("a", 64)}, {"--decision-sha256", strings.Repeat("b", 64)}} {
		var output bytes.Buffer
		if err := runProposalStrategy(t.Context(), append(append([]string{}, base...), extra...), &output, time.Now); err == nil || !strings.Contains(err.Error(), "not applicable") || output.Len() != 0 {
			t.Fatalf("step accepted caller-controlled input %v: %v", extra, err)
		}
	}
}

func TestProposalStrategyStepRejectsAmbiguousPolicyPathsBeforeReads(t *testing.T) {
	root := t.TempDir()
	wallet, buy, sell := filepath.Join(root, "wallet"), filepath.Join(root, "buy"), filepath.Join(root, "sell")
	base := []string{"--operation", "step", "--strategy", filepath.Join(root, "strategy"), "--inventory", wallet,
		"--authority-policy", filepath.Join(root, "authority"), "--submitter-policy", filepath.Join(root, "submitter")}
	for _, extra := range [][]string{
		{"--buy-authority-policy", buy}, {"--sell-authority-policy", sell},
		{"--buy-authority-policy", buy, "--sell-authority-policy", buy},
		{"--buy-authority-policy", "relative", "--sell-authority-policy", sell},
		{"--buy-authority-policy", buy, "--sell-authority-policy", sell, "--next-authority-policy", buy},
		{"--buy-authority-policy", wallet, "--sell-authority-policy", sell},
		{"--buy-authority-policy", buy, "--sell-authority-policy", wallet},
		{"--operation", "show", "--buy-authority-policy", buy, "--sell-authority-policy", sell},
		{"--operation", "acquire", "--buy-authority-policy", buy, "--sell-authority-policy", sell},
	} {
		var output bytes.Buffer
		err := runProposalStrategy(t.Context(), append(append([]string{}, base...), extra...), &output, time.Now)
		if err == nil || output.Len() != 0 || strings.HasPrefix(err.Error(), "read ") {
			t.Fatalf("invalid policy paths reached file reads: %v: %v", extra, err)
		}
	}
}

func TestWriteStrategyStepProjectsOnlyNonAuthorizingStatus(t *testing.T) {
	result := policyauthority.StrategyReconciliation{ClaimPath: "/private/not-for-projection/claim.jsonl", AcquisitionPath: "/private/not-for-projection/acquisition.jsonl", BlockedReason: "wallet_has_pending_claim", AccountingSHA256: strings.Repeat("a", 64),
		Strategy: policyauthority.StrategyJournalStatus{State: &shadow.AccountedStrategy{}, HeadSHA256: strings.Repeat("b", 64)}}
	var output bytes.Buffer
	if err := writeStrategyStep(&output, result, nil, nil); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(output.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 7 || data["status"] != "strategy_step_not_authorized" || data["current_head_sha256"] != result.Strategy.HeadSHA256 || data["blocked_reason"] != "wallet_has_pending_claim" || data["strategy_pending"] != false || data["can_sign"] != false || data["can_submit"] != false || strings.Contains(output.String(), result.ClaimPath) {
		t.Fatal("step projection leaked private state or claimed authority")
	}
	want := errors.New("step output failed")
	if err := writeStrategyStep(writerFunc(func([]byte) (int, error) { return 0, want }), result, nil, nil); !errors.Is(err, want) {
		t.Fatalf("step output error: %v", err)
	}
	result.BlockedReason, result.AcquisitionPath = "acquisition_pending", "/private/operator/acquisition.jsonl"
	output.Reset()
	if err := writeStrategyStep(&output, result, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &data); err != nil || data["acquisition_path"] != result.AcquisitionPath {
		t.Fatalf("step omitted the acquisition needed for explicit retirement: %v", err)
	}
}
