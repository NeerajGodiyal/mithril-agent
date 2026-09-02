package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
)

func TestShadowAllocationBuildsAnInactiveExactPaperGeneration(t *testing.T) {
	_, _, solPath, jupPath := shadowPortfolioTestPolicies(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	portfolioPath := filepath.Join(root, "current-portfolio.json")
	if err := runShadowPortfolio([]string{
		"--out", portfolioPath, "--limit-usd", "150", "--max-sol-usd", "300",
		"--book", "sol=" + solPath, "--book", "jup=" + jupPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	instruction := paperdashboard.Instruction{
		Version:   paperdashboard.InstructionVersion,
		UpdatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		Market:    "all", Preference: "more-opportunities",
		PaperCapitalMicros: 270_000_000, MinimumOrderMicros: 5_000_000,
		MaximumOrderMicros: 100_000_000, CadenceSeconds: 15, MaxDrawdownBPS: 750,
	}
	encoded, err := json.Marshal(instruction)
	if err != nil {
		t.Fatal(err)
	}
	instructionPath := filepath.Join(root, "instruction.json")
	if err := os.WriteFile(instructionPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "generation")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShadowAllocation([]string{
		"--portfolio", portfolioPath, "--instruction", instructionPath,
		"--out-dir", outDir,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Status                  string `json:"status"`
		PaperOnly               bool   `json:"paper_only"`
		Authorized              bool   `json:"authorized"`
		CapitalAtCeilingMicros  uint64 `json:"capital_at_ceiling_micros"`
		TotalCapitalLimitMicros uint64 `json:"total_capital_limit_micros"`
	}
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil ||
		receipt.Status != "paper_allocation_ready_not_active" || !receipt.PaperOnly ||
		receipt.Authorized || receipt.CapitalAtCeilingMicros != 270_000_000 ||
		receipt.TotalCapitalLimitMicros != 270_000_000 {
		t.Fatalf("allocation receipt = %+v err=%v", receipt, err)
	}
	var manifest shadowPortfolioManifest
	if err := readStrictJSON(filepath.Join(outDir, "portfolio.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	total, policies, err := validateShadowPortfolio(manifest)
	if err != nil || total != 270_000_000 || len(policies) != 2 {
		t.Fatalf("resized portfolio total=%d policies=%d err=%v", total, len(policies), err)
	}
	for id, policy := range policies {
		if policy.MinimumOrderValueMicros != 5_000_000 ||
			policy.MaximumOrderValueMicros == 0 ||
			policy.MaximumOrderValueMicros > 100_000_000 || policy.TickSeconds != 15 ||
			policy.Adaptive == nil || policy.Adaptive.MaxDrawdownBPS != 750 ||
			policy.Adaptive.MaxObservationGapSeconds != 30 {
			t.Fatalf("%s resized policy = %+v", id, policy)
		}
	}
}

func TestShadowAllocationRejectsAnInvalidInstructionBeforeCreatingOutput(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "not-created")
	err = runShadowAllocation([]string{
		"--portfolio", filepath.Join(root, "missing-portfolio.json"),
		"--instruction", filepath.Join(root, "missing-instruction.json"),
		"--out-dir", outDir,
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("invalid allocation inputs were accepted")
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Fatalf("invalid allocation created output: %v", statErr)
	}
}

func TestPrepareShadowAllocationDirectoryAcceptsOneEmptyPrivateDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "prepared")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareShadowAllocationDirectory(path); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareShadowAllocationDirectoryCreatesOneNewPrivateDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "created")
	if err := prepareShadowAllocationDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("created directory = %+v, %v", info, err)
	}
}

func TestPrepareShadowAllocationDirectoryRejectsUnsafeExistingDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "open")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := prepareShadowAllocationDirectory(path); err == nil {
		t.Fatal("non-private prepared directory was accepted")
	}
}

func TestPrepareShadowAllocationDirectoryRejectsSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "prepared")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := prepareShadowAllocationDirectory(path); err == nil {
		t.Fatal("symlinked prepared directory was accepted")
	}
}

func TestPrepareShadowAllocationDirectoryRejectsExistingContent(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "prepared")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareShadowAllocationDirectory(path); err == nil {
		t.Fatal("non-empty prepared directory was accepted")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "do not replace\n" {
		t.Fatalf("existing content changed: %q, %v", raw, err)
	}
}

func TestShadowAllocationKeepsAnAdmittedMarketAtItsEvidenceNotional(t *testing.T) {
	artifactPath, journalPath, now := writeReadyProvisionalEvidence(t)
	artifact, err := loadProvisionalMarketAdmission(artifactPath, journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildAdaptiveProvisionalPolicy(
		artifact, artifact.Candidate.QuoteNotionalUSDC, 80_000_000, 3_000_000,
		artifact.Candidate.QuoteSlippageBPS, 100_000, artifact.Observe, 60,
	)
	if err != nil {
		t.Fatal(err)
	}
	instruction := paperdashboard.Instruction{
		Version:   paperdashboard.InstructionVersion,
		UpdatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		Market:    "all", Preference: "balanced", PaperCapitalMicros: 150_000_000,
		MinimumOrderMicros: 5_000_000, MaximumOrderMicros: 75_000_000,
		CadenceSeconds: 60, MaxDrawdownBPS: policy.Adaptive.MaxDrawdownBPS,
	}
	resized, err := resizePaperPolicy(policy, 100_000_000, instruction, 300_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if resized.InputAmount != artifact.Candidate.QuoteNotionalUSDC ||
		resized.MaximumOrderValueMicros != 75_000_000 ||
		resized.StartingInputUnits <= resized.InputAmount ||
		!provisionalPolicyMatchesArtifact(resized, artifact) {
		t.Fatalf("admitted allocation changed its evidence notional: %+v", resized)
	}
}

func TestShadowAllocationSOLRoundingNeverExceedsItsTarget(t *testing.T) {
	policy, _, _, _ := shadowPortfolioTestPolicies(t)
	instruction := paperdashboard.Instruction{
		Version:   paperdashboard.InstructionVersion,
		UpdatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		Market:    "all", Preference: "balanced", PaperCapitalMicros: 100_000_000,
		MinimumOrderMicros: 5_000_000, MaximumOrderMicros: 50_000_000,
		CadenceSeconds: 60, MaxDrawdownBPS: policy.Adaptive.MaxDrawdownBPS,
	}
	const target = uint64(50_123_457)
	const oddSOLCeiling = uint64(200_000_008)
	resized, err := resizePaperPolicy(policy, target, instruction, oddSOLCeiling)
	if err != nil {
		t.Fatal(err)
	}
	capital, err := shadowPortfolioCapital(resized, oddSOLCeiling)
	if err != nil || capital != target {
		t.Fatalf("rounded SOL capital = %d, want %d: %v", capital, target, err)
	}
}
