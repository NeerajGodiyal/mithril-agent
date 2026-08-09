package mcpobserve

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/solana"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFailureStageDoesNotExposeTheError(t *testing.T) {
	err := failAt("state", errors.New("private detail"))
	if got := FailureStage(err); got != "state" {
		t.Fatalf("FailureStage() = %q, want state", got)
	}
	if got := FailureStage(errors.New("unstaged")); got != "" {
		t.Fatalf("unstaged error reported %q", got)
	}
}

const helperEnv = "MITHRIL_AGENT_MCP_HELPER"
const helperHealthTimeEnv = "MITHRIL_AGENT_MCP_HEALTH_TIME"

func TestMCPHelperProcess(_ *testing.T) {
	if os.Getenv(helperEnv) != "1" &&
		os.Getenv("MITHRIL_MCP_PROFILE") != "integration-test-helper" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "mithril", Version: "test"}, nil)
	type accountInput struct {
		Pubkey     string  `json:"pubkey"`
		Encoding   string  `json:"encoding"`
		DataLength *uint64 `json:"data_length"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        accountTool,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input accountInput) (*mcpsdk.CallToolResult, any, error) {
		if input.Pubkey == "" || input.Encoding != "base64" || input.DataLength == nil || *input.DataLength != 0 {
			return nil, nil, fmt.Errorf("unexpected input")
		}
		return nil, validAccountResult(), nil
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        stateTool,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return nil, validStateResult(), nil
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        genesisTool,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return nil, validGenesisResult(), nil
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        diagnosisTool,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input struct {
		IncludeLogs        *bool `json:"include_logs"`
		IncludeReplayTrend *bool `json:"include_replay_trend"`
	}) (*mcpsdk.CallToolResult, any, error) {
		if input.IncludeLogs == nil || *input.IncludeLogs {
			return nil, nil, fmt.Errorf("log scanning must be explicitly disabled")
		}
		if input.IncludeReplayTrend == nil || *input.IncludeReplayTrend {
			return nil, nil, fmt.Errorf("replay scanning must be explicitly disabled")
		}
		observedAt := time.Now().UTC()
		if configured := os.Getenv(helperHealthTimeEnv); configured != "" {
			parsed, err := time.Parse(time.RFC3339Nano, configured)
			if err != nil {
				return nil, nil, err
			}
			observedAt = parsed
		}
		return nil, validDiagnosisResult(observedAt), nil
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        infoTool,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		return nil, validInfoResult(), nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestVerifyNodeIdentityRejectsDrift(t *testing.T) {
	tests := map[string]func(map[string]any){
		"missing": func(value map[string]any) {
			value["found"] = false
			delete(value, "state")
		},
		"unsupported schema": func(value map[string]any) {
			value["state"].(map[string]any)["schema_supported"] = false
		},
		"wrong cluster": func(value map[string]any) {
			value["state"].(map[string]any)["cluster"] = "mainnet-beta"
		},
		"wrong genesis": func(value map[string]any) {
			value["state"].(map[string]any)["genesis_hash"] = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"
		},
		"not ready": func(value map[string]any) {
			value["state"].(map[string]any)["stage"] = "building"
		},
		"error shutdown": func(value map[string]any) {
			value["is_error_shutdown"] = true
		},
		"clock anomaly": func(value map[string]any) {
			value["state"].(map[string]any)["source_clock_anomaly"] = true
		},
		"corruption": func(value map[string]any) {
			value["state"].(map[string]any)["corruption_reason"] = "bad"
		},
		"unknown top-level field": func(value map[string]any) {
			value["unexpected"] = true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validStateResult()
			mutate(value)
			if err := verifyNodeIdentity(value); err == nil {
				t.Fatal("mutated Mithril identity was accepted")
			}
		})
	}
}

func TestVerifyGenesisHashRejectsDrift(t *testing.T) {
	tests := []any{
		nil,
		map[string]any{"genesis_hash": "wrong"},
		map[string]any{"genesis_hash": solana.DevnetGenesisHash, "unexpected": true},
		map[string]any{"genesis_hash": 1},
	}
	for _, value := range tests {
		if err := verifyGenesisHash(value); err == nil {
			t.Fatalf("genesis result was accepted: %#v", value)
		}
	}
	if err := verifyGenesisHash(validGenesisResult()); err != nil {
		t.Fatalf("valid genesis result rejected: %v", err)
	}
}

func TestVerifyRPCOriginRejectsDifferentNode(t *testing.T) {
	if err := verifyRPCOrigin(validInfoResult(), "http://127.0.0.1:8899"); err != nil {
		t.Fatalf("matching RPC origin rejected: %v", err)
	}
	for _, value := range []any{
		nil,
		map[string]any{"rpc_configured": false},
		map[string]any{
			"rpc_configured": true,
			"rpc_origin":     "http://127.0.0.1:9900",
		},
	} {
		if err := verifyRPCOrigin(value, "http://127.0.0.1:8899"); err == nil {
			t.Fatalf("different MCP RPC origin was accepted: %#v", value)
		}
	}
}

func TestObserveAccountOverStdio(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	source := systemProgram
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	client, err := New(Config{
		Command: executable,
		Args:    []string{"-test.run=^TestMCPHelperProcess$"},
		Env: []string{
			helperEnv + "=1",
			helperHealthTimeEnv + "=" + now.Format(time.RFC3339Nano),
		},
		Cluster:   "devnet",
		RPCOrigin: "http://127.0.0.1:8899",
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	observation, err := client.ObserveAccount(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Source != source || observation.BalanceLamports != 4_000_000 || observation.Slot != 456 ||
		observation.ObservedAt != now || observation.EvidenceSource != "mithril_mcp" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestParseDiagnosisResultRejectsUnsafeOrInconsistentEvidence(t *testing.T) {
	now := time.Now().UTC()
	tests := map[string]func(map[string]any){
		"automation claim changed": func(value map[string]any) {
			value["safe_for_automation"] = true
		},
		"stale": func(value map[string]any) {
			value["observed_at"] = now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
		},
		"missing evidence flag": func(value map[string]any) {
			delete(value, "evidence_complete")
		},
		"inconsistent status": func(value map[string]any) {
			value["checks"].([]map[string]any)[0]["status"] = "critical"
		},
		"missing required check": func(value map[string]any) {
			value["checks"] = value["checks"].([]map[string]any)[1:]
		},
		"inconsistent slot comparison": func(value map[string]any) {
			value["slots_behind"].(map[string]any)["status"] = "behind"
		},
		"divergence": func(value map[string]any) {
			value["divergence_artifacts"] = []any{map[string]any{"checked_slot": 1}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validDiagnosisResult(now)
			mutate(value)
			health, err := parseDiagnosisResult(value, now)
			if name == "divergence" {
				if err != nil || health.DivergenceArtifacts != 1 {
					t.Fatalf("divergence evidence = %+v, %v", health, err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe diagnosis was accepted")
			}
		})
	}
}

func TestParseDiagnosisResultReturnsBoundedIssues(t *testing.T) {
	now := time.Now().UTC()
	value := validDiagnosisResult(now)
	value["status"] = "degraded"
	checks := value["checks"].([]map[string]any)
	checks[8]["status"] = "degraded"
	health, err := parseDiagnosisResult(value, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(health.Issues) != 1 || health.Issues[0].Name != "runtime_state_agreement" ||
		health.Issues[0].Status != "degraded" {
		t.Fatalf("health issues = %+v", health.Issues)
	}
}

func TestParseAccountResultRejectsSemanticDrift(t *testing.T) {
	source := systemProgram
	now := time.Now()
	tests := map[string]func(map[string]any){
		"absent": func(value map[string]any) {
			value["found"] = false
			value["value"] = nil
		},
		"wrong finality": func(value map[string]any) {
			value["finality"] = "confirmed"
		},
		"wrong consistency": func(value map[string]any) {
			value["consistency"] = "atomic"
		},
		"invalid balance": func(value map[string]any) {
			value["value"].(map[string]any)["lamports"] = "-1"
		},
		"executable": func(value map[string]any) {
			value["value"].(map[string]any)["executable"] = true
		},
		"program owned": func(value map[string]any) {
			value["value"].(map[string]any)["owner"] = "SysvarC1ock11111111111111111111111111111111"
		},
		"unknown field": func(value map[string]any) {
			value["unexpected"] = true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validAccountResult()
			mutate(value)
			if _, err := parseAccountResult(value, "devnet", source, now); err == nil {
				t.Fatal("mutated MCP result was accepted")
			}
		})
	}
}

func TestNewRejectsUnsafeCommand(t *testing.T) {
	if _, err := New(Config{Command: "mithril", Cluster: "devnet"}, nil); err == nil {
		t.Fatal("relative command was accepted")
	}
	path := t.TempDir() + "/server"
	if err := os.WriteFile(path, []byte("test"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Command: path, Cluster: "devnet"}, nil); err == nil {
		t.Fatal("world-writable command was accepted")
	}
}

func validAccountResult() map[string]any {
	return map[string]any{
		"found": true,
		"context": map[string]any{
			"slot": uint64(456),
		},
		"value": map[string]any{
			"data":           [2]string{"", "base64"},
			"executable":     false,
			"lamports":       "4000000",
			"owner":          systemProgram,
			"rent_epoch":     "0",
			"space":          uint64(0),
			"data_offset":    uint64(0),
			"data_length":    uint64(0),
			"data_truncated": false,
		},
		"finality":    "local_unfinalized",
		"consistency": "node_reported_non_atomic",
	}
}

func validStateResult() map[string]any {
	return map[string]any{
		"found": true,
		"state": map[string]any{
			"schema_supported":     true,
			"cluster":              "devnet",
			"genesis_hash":         solana.DevnetGenesisHash,
			"stage":                "ready",
			"source_clock_anomaly": false,
		},
		"is_error_shutdown": false,
	}
}

func validGenesisResult() map[string]any {
	return map[string]any{"genesis_hash": solana.DevnetGenesisHash}
}

func validInfoResult() map[string]any {
	origin := "http://127.0.0.1:8899"
	if configured := os.Getenv("MITHRIL_RPC_URL"); configured != "" {
		parsed, err := url.Parse(configured)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			origin = parsed.Scheme + "://" + parsed.Host
		}
	}
	return map[string]any{
		"rpc_configured": true,
		"rpc_origin":     origin,
	}
}

func validDiagnosisResult(observedAt time.Time) map[string]any {
	names := []string{
		"metrics",
		"runtime_provenance",
		"verification",
		"block_source",
		"rpc",
		"state",
		"state_evidence",
		"shutdown",
		"runtime_state_agreement",
		"divergence_artifacts",
		"host",
	}
	checks := make([]map[string]any, 0, len(names))
	for _, name := range names {
		checks = append(checks, map[string]any{"name": name, "status": "ok"})
	}
	return map[string]any{
		"status":              "healthy",
		"assessment_scope":    "point_in_time_snapshot",
		"observed_at":         observedAt.UTC().Format(time.RFC3339Nano),
		"safe_for_automation": false,
		"evidence_complete":   true,
		"checks":              checks,
		"slots_behind": map[string]any{
			"mithril_slot": 100, "reference_slot": 100, "slots_behind": 0,
			"reference_commitment": "confirmed", "mithril_view": "local_unfinalized_head",
			"threshold": 150, "status": "in_sync",
		},
		"divergence_artifacts": []any{},
	}
}

// A server that emits one enormous frame must be refused, not buffered. The
// SDK's own decoder has no size limit, so this bound is ours to enforce.
func TestObserveRejectsOversizedServerFrame(t *testing.T) {
	directory := t.TempDir()
	server := filepath.Join(directory, "flood-mcp")
	// Answer initialize with a single valid-looking but enormous frame.
	script := "#!/bin/sh\n" +
		"printf '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"pad\":\"'\n" +
		"i=0; while [ $i -lt 6000 ]; do printf '%s' 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; i=$((i+1)); done\n" +
		"printf '\"}}\\n'\n" +
		"sleep 30\n"
	if err := os.WriteFile(server, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	client, err := New(Config{
		Command:   server,
		Cluster:   "devnet",
		RPCOrigin: "http://127.0.0.1:8899",
	}, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	if _, err := client.ObserveAccount(ctx, systemProgram); err == nil {
		t.Fatal("an oversized server frame was accepted")
	}
}
