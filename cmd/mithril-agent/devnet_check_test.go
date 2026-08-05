package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type devnetCheckObserverStub struct {
	observation agent.NodeObservation
	err         error
}

func (stub devnetCheckObserverStub) Observe(
	context.Context,
	string,
) (agent.NodeObservation, error) {
	return stub.observation, stub.err
}

type devnetCheckLifecycleStub struct {
	genesisErr error
	accountErr error
}

func (stub devnetCheckLifecycleStub) VerifyGenesis(context.Context, string) error {
	return stub.genesisErr
}

func (stub devnetCheckLifecycleStub) AccountsForTransfer(
	context.Context,
	string,
	string,
	uint64,
) (txflow.TransferAccountEvidence, error) {
	return txflow.TransferAccountEvidence{}, stub.accountErr
}

func TestDevnetCheckReportsBoundedReadOnlyReadiness(t *testing.T) {
	configPath := writeDevnetCheckConfig(t)
	installDevnetCheckStubs(t, devnetCheckDependencies{
		observer:  devnetCheckObserverStub{observation: healthyDevnetCheckObservation()},
		lifecycle: devnetCheckLifecycleStub{},
	})

	var output bytes.Buffer
	if err := run([]string{"devnet-check", "--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	var summary devnetCheckSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != preflightOK || !allDevnetChecksOK(summary.Checks) {
		t.Fatalf("devnet check = %+v", summary)
	}
	if output.Len() > 512 || bytes.Count(output.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("output is not one bounded line: %q", output.String())
	}
	for _, forbidden := range []string{
		"source-secret",
		"destination-secret",
		filepath.Dir(configPath),
		"http",
	} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("output disclosed %q: %s", forbidden, output.String())
		}
	}
}

func TestDevnetCheckFailsClosedByStage(t *testing.T) {
	tests := []struct {
		name       string
		deps       devnetCheckDependencies
		wantFailed string
	}{
		{
			name: "MCP",
			deps: devnetCheckDependencies{
				observer:  devnetCheckObserverStub{err: errors.New("unavailable")},
				lifecycle: devnetCheckLifecycleStub{},
			},
			wantFailed: "mithril_mcp",
		},
		{
			name: "health",
			deps: devnetCheckDependencies{
				observer:  devnetCheckObserverStub{observation: unhealthyDevnetCheckObservation()},
				lifecycle: devnetCheckLifecycleStub{},
			},
			wantFailed: "mithril_health",
		},
		{
			name: "genesis",
			deps: devnetCheckDependencies{
				observer:  devnetCheckObserverStub{observation: healthyDevnetCheckObservation()},
				lifecycle: devnetCheckLifecycleStub{genesisErr: errors.New("mismatch")},
			},
			wantFailed: "provider_genesis",
		},
		{
			name: "accounts",
			deps: devnetCheckDependencies{
				observer:  devnetCheckObserverStub{observation: healthyDevnetCheckObservation()},
				lifecycle: devnetCheckLifecycleStub{accountErr: errors.New("disagreement")},
			},
			wantFailed: "provider_accounts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeDevnetCheckConfig(t)
			installDevnetCheckStubs(t, test.deps)
			var output bytes.Buffer
			err := run([]string{"devnet-check", "--config", configPath}, &output)
			if !errors.Is(err, errDevnetCheckFailed) {
				t.Fatalf("error = %v", err)
			}
			var decoded map[string]any
			if json.Unmarshal(output.Bytes(), &decoded) != nil ||
				!strings.Contains(output.String(), `"`+test.wantFailed+`":"failed"`) {
				t.Fatalf("summary = %s", output.String())
			}
		})
	}
}

func TestDevnetCheckDoesNotOpenLiveDependenciesAfterPreflightFailure(t *testing.T) {
	configPath := writeDevnetCheckConfig(t)
	oldPreflight := devnetPreflight
	oldOpen := openDevnetCheckDependencies
	t.Cleanup(func() {
		devnetPreflight = oldPreflight
		openDevnetCheckDependencies = oldOpen
	})
	devnetPreflight = func(string) preflightSummary {
		return preflightSummary{Status: preflightFailed}
	}
	opened := false
	openDevnetCheckDependencies = func(config) (devnetCheckDependencies, error) {
		opened = true
		return devnetCheckDependencies{}, nil
	}

	var output bytes.Buffer
	if err := run([]string{"devnet-check", "--config", configPath}, &output); !errors.Is(err, errDevnetCheckFailed) {
		t.Fatalf("error = %v", err)
	}
	if opened {
		t.Fatal("live dependencies opened after preflight failed")
	}
}

func TestDevnetCheckDirectsSwapProfilesToSwapCheck(t *testing.T) {
	configPath := writeSwapDemoConfig(t)
	oldPreflight := devnetPreflight
	oldOpen := openDevnetCheckDependencies
	t.Cleanup(func() {
		devnetPreflight = oldPreflight
		openDevnetCheckDependencies = oldOpen
	})
	preflightCalled := false
	devnetPreflight = func(string) preflightSummary {
		preflightCalled = true
		return preflightSummary{Status: preflightFailed}
	}
	opened := false
	openDevnetCheckDependencies = func(config) (devnetCheckDependencies, error) {
		opened = true
		return devnetCheckDependencies{}, nil
	}

	var output bytes.Buffer
	err := run([]string{"devnet-check", "--config", configPath}, &output)
	if !errors.Is(err, errDevnetCheckFailed) {
		t.Fatalf("error = %v", err)
	}
	if opened {
		t.Fatal("legacy dependencies opened for a swap profile")
	}
	if preflightCalled {
		t.Fatal("legacy preflight ran for a swap profile")
	}
	var summary devnetCheckSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != preflightFailed || summary.Checks.Preflight != preflightSkipped ||
		summary.NextCommand != "mithril-agent swap check --config PATH" {
		t.Fatalf("summary = %+v", summary)
	}
	if strings.Contains(output.String(), configPath) {
		t.Fatalf("output disclosed config path: %s", output.String())
	}
}

func installDevnetCheckStubs(t *testing.T, dependencies devnetCheckDependencies) {
	t.Helper()
	oldPreflight := devnetPreflight
	oldOpen := openDevnetCheckDependencies
	t.Cleanup(func() {
		devnetPreflight = oldPreflight
		openDevnetCheckDependencies = oldOpen
	})
	devnetPreflight = func(string) preflightSummary {
		return preflightSummary{Status: preflightOK}
	}
	openDevnetCheckDependencies = func(config) (devnetCheckDependencies, error) {
		return dependencies, nil
	}
}

func writeDevnetCheckConfig(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.json")
	content := []byte("{\"profile\":{\"source\":\"source-secret\",\"destination\":\"destination-secret\"}}\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func healthyDevnetCheckObservation() agent.NodeObservation {
	return agent.NodeObservation{
		Account: agent.Observation{Slot: 42},
		Health: agent.NodeHealth{
			Status:              "healthy",
			AssessmentScope:     "point_in_time_snapshot",
			ObservedAt:          time.Now().UTC(),
			EvidenceComplete:    true,
			DivergenceArtifacts: 0,
		},
	}
}

func unhealthyDevnetCheckObservation() agent.NodeObservation {
	observation := healthyDevnetCheckObservation()
	observation.Health.Status = "degraded"
	return observation
}
