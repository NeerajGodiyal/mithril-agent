package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowPortfolioCountsCounterfactualProcessesAsTwoBooks(t *testing.T) {
	sol, jup, solPath, jupPath := shadowPortfolioTestPolicies(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "portfolio.json")
	var output bytes.Buffer
	if err := runShadowPortfolio([]string{
		"--out", out, "--limit-usd", "150", "--max-sol-usd", "300",
		"--book", "sol=" + solPath, "--book", "jup=" + jupPath,
	}, &output); err != nil {
		t.Fatal(err)
	}
	var manifest shadowPortfolioManifest
	if err := readStrictJSON(out, &manifest); err != nil {
		t.Fatal(err)
	}
	total, _, err := validateShadowPortfolio(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if total != 145_240_000 || len(manifest.Books) != 2 ||
		manifest.Books[0].ID != "jup" || manifest.Books[1].ID != "sol" {
		t.Fatalf("portfolio total=%d books=%+v", total, manifest.Books)
	}
	if maximum, err := loadShadowPortfolioForBook(out, "sol", solPath, sol); err != nil || maximum != 300_000_000 {
		t.Fatalf("SOL book maximum=%d err=%v", maximum, err)
	}
	if maximum, err := loadShadowPortfolioForBook(out, "jup", jupPath, jup); err != nil || maximum != 300_000_000 {
		t.Fatalf("JUP book maximum=%d err=%v", maximum, err)
	}
}

func TestShadowPortfolioSOLPriceUsesTheUpperValidatedBound(t *testing.T) {
	policy := validShadowPolicy().Trigger
	at := time.Now().UTC().Add(-time.Second)
	primary := candidatePriceSource{identity: policy.PrimarySourceSHA256, at: at}
	secondary := candidatePriceSource{identity: policy.SecondarySourceSHA256, at: at}
	if err := validateShadowPortfolioSOLPrice(
		t.Context(), policy, primary, secondary, 150_000_001,
	); err != nil {
		t.Fatalf("upper bound was refused: %v", err)
	}
	if err := validateShadowPortfolioSOLPrice(
		t.Context(), policy, primary, secondary, 150_000_000,
	); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("confidence-adjusted ceiling breach was accepted: %v", err)
	}
}

func TestShadowPortfolioRejectsDriftDuplicatesAndOversubscription(t *testing.T) {
	_, jup, solPath, jupPath := shadowPortfolioTestPolicies(t)
	solPolicy, err := loadActiveShadowPolicy(solPath)
	if err != nil {
		t.Fatal(err)
	}
	solSHA, _ := solPolicy.Fingerprint()
	jupSHA, _ := jup.Fingerprint()
	manifest := shadowPortfolioManifest{
		Version: shadowPortfolioLegacyVersion, Status: "paper_portfolio", PaperOnly: true,
		TotalCapitalLimitMicros: 145_239_999, MaxSOLUSDMicros: 300_000_000,
		Books: []shadowPortfolioBook{
			{ID: "jup", Market: jup.Market, PolicyPath: jupPath, PolicySHA256: jupSHA},
			{ID: "sol", Market: solPolicy.Market, PolicyPath: solPath, PolicySHA256: solSHA},
		},
	}
	if _, _, err := validateShadowPortfolio(manifest); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversubscribed portfolio was accepted: %v", err)
	}
	manifest.TotalCapitalLimitMicros = 150_000_000
	manifest.Books[0].PolicySHA256 = strings.Repeat("0", 64)
	if _, _, err := validateShadowPortfolio(manifest); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("drifted policy was accepted: %v", err)
	}
	manifest.Books[0].PolicySHA256 = jupSHA
	manifest.Books[1].Market = jup.Market
	if _, _, err := validateShadowPortfolio(manifest); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("wrong market was accepted: %v", err)
	}
	manifest.Books[1].Market = solPolicy.Market
	manifest.Books[1].ID = "jup"
	if _, _, err := validateShadowPortfolio(manifest); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("duplicate ID was accepted: %v", err)
	}
	manifest.Books[1].ID = "sol"
	manifest.Books[1].PolicyPath = jupPath
	if _, _, err := validateShadowPortfolio(manifest); err == nil || !strings.Contains(err.Error(), "paths") {
		t.Fatalf("duplicate policy path was accepted: %v", err)
	}

	secondSOLPath := writeShadowPolicy(t, solPolicy)
	manifest.Books = []shadowPortfolioBook{
		{ID: "sol-a", Market: solPolicy.Market, PolicyPath: solPath, PolicySHA256: solSHA},
		{ID: "sol-b", Market: solPolicy.Market, PolicyPath: secondSOLPath, PolicySHA256: solSHA},
	}
	if _, _, err := validateShadowPortfolio(manifest); err == nil || !strings.Contains(err.Error(), "markets") {
		t.Fatalf("duplicate market was accepted: %v", err)
	}
}

func TestShadowPortfolioManifestRejectsMalformedAndUnknownFields(t *testing.T) {
	sol, _, solPath, _ := shadowPortfolioTestPolicies(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "portfolio.json")
	if err := runShadowPortfolio([]string{
		"--out", out, "--limit-usd", "75", "--max-sol-usd", "300",
		"--book", "sol=" + solPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	for name, encoded := range map[string][]byte{
		"malformed":     []byte("{"),
		"unknown field": bytes.Replace(valid, []byte("{"), []byte(`{"unexpected":true,`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(out, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadShadowPortfolioForBook(out, "sol", solPath, sol); err == nil {
				t.Fatal("invalid portfolio manifest was accepted")
			}
		})
	}
}

func TestInstructionBoundPortfolioRequiresTheSelectedCandidateToMatch(t *testing.T) {
	digest := strings.Repeat("a", 64)
	if err := validateShadowPortfolioCandidateBinding(true, digest, 270_000_000, "", 0); err == nil {
		t.Fatal("instruction-bound portfolio accepted a candidate without an experiment")
	}
	if err := validateShadowPortfolioCandidateBinding(true, digest, 270_000_000, digest, 150_000_000); err == nil {
		t.Fatal("instruction-bound portfolio accepted a different capital ceiling")
	}
	if err := validateShadowPortfolioCandidateBinding(true, digest, 270_000_000, strings.Repeat("b", 64), 270_000_000); err == nil {
		t.Fatal("instruction-bound portfolio accepted a different instruction")
	}
	if err := validateShadowPortfolioCandidateBinding(true, digest, 270_000_000, digest, 270_000_000); err != nil {
		t.Fatalf("matching candidate instruction was rejected: %v", err)
	}
	if err := validateShadowPortfolioCandidateBinding(true, "", 150_000_000, "", 0); err != nil {
		t.Fatalf("legacy portfolio compatibility was rejected: %v", err)
	}
}

func TestShadowPortfolioRejectsMissingMismatchedAndNonselectedDriftedBooks(t *testing.T) {
	sol, jup, solPath, jupPath := shadowPortfolioTestPolicies(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "portfolio.json")
	if err := runShadowPortfolio([]string{
		"--out", out, "--limit-usd", "150", "--max-sol-usd", "300",
		"--book", "sol=" + solPath, "--book", "jup=" + jupPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	if _, err := loadShadowPortfolioForBook(out, "missing", solPath, sol); err == nil ||
		!strings.Contains(err.Error(), "not present") {
		t.Fatalf("missing book refusal = %v", err)
	}
	if _, err := loadShadowPortfolioForBook(out, "sol", jupPath, sol); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("selected path mismatch refusal = %v", err)
	}

	jup.Adaptive.MinimumSignalBPS++
	if err := jup.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(jup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jupPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadShadowPortfolioForBook(out, "sol", solPath, sol); err == nil ||
		!strings.Contains(err.Error(), "identity") {
		t.Fatalf("nonselected policy drift was accepted: %v", err)
	}
}

func TestShadowPortfolioDocumentedStagingPlanLeavesOneDollarHeadroom(t *testing.T) {
	const (
		limitMicros = uint64(150_000_000)
		maxSOL      = uint64(300_000_000)
	)
	observe := "So11111111111111111111111111111111111111112"
	sol, err := buildAdaptiveShadowPolicy(
		shadow.Mainnet, 246_000_000, 100, 100_000, observe, 60, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	sol.StartingFeeReserveLamports = 4_000_000
	if err := sol.Validate(); err != nil {
		t.Fatal(err)
	}
	jup, err := buildAdaptiveJUPPolicy(
		50_000_000, 80_000_000, 3_000_000, 100, 100_000, observe, 60,
	)
	if err != nil {
		t.Fatal(err)
	}
	solPath, jupPath := writeShadowPolicy(t, sol), writeShadowPolicy(t, jup)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "portfolio.json")
	if err := runShadowPortfolio([]string{
		"--out", out, "--limit-usd", "150", "--max-sol-usd", "300",
		"--book", "sol=" + solPath, "--book", "jup=" + jupPath,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var manifest shadowPortfolioManifest
	if err := readStrictJSON(out, &manifest); err != nil {
		t.Fatal(err)
	}
	total, _, err := validateShadowPortfolio(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if total != 149_000_000 || limitMicros-total != 1_000_000 ||
		manifest.MaxSOLUSDMicros != maxSOL {
		t.Fatalf("capital=%d headroom=%d max SOL=%d", total, limitMicros-total, manifest.MaxSOLUSDMicros)
	}
}

func TestShadowRunInvalidPortfolioCreatesNoRunArtifacts(t *testing.T) {
	sol, _, solPath, _ := shadowPortfolioTestPolicies(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "invalid-portfolio.json")
	if err := os.WriteFile(manifestPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	journalRoot := filepath.Join(root, "journal")
	alertStatus := filepath.Join(root, "alert-status.json")
	_, err = openShadowRun(t.Context(), sol, shadowRunOptions{
		policyPath: solPath, directory: journalRoot,
		portfolioPath: manifestPath, portfolioBook: "sol", alertStatus: alertStatus,
	})
	if err == nil {
		t.Fatal("shadow run accepted an invalid portfolio manifest")
	}
	for _, path := range []string{journalRoot, alertStatus} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("startup side effect %q: %v", path, statErr)
		}
	}
}

func shadowPortfolioTestPolicies(t *testing.T) (
	shadow.Policy, shadow.Policy, string, string,
) {
	t.Helper()
	observe := "So11111111111111111111111111111111111111112"
	sol, err := buildAdaptiveShadowPolicy(
		shadow.Mainnet, 246_000_000, 100, 100_000, observe, 60, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	sol.StartingFeeReserveLamports = 4_000_000
	if err := sol.Validate(); err != nil {
		t.Fatal(err)
	}
	jup, err := buildAdaptiveJUPPolicy(
		69_040_000, 4_000_000, 3_000_000, 100, 100_000, observe, 60,
	)
	if err != nil {
		t.Fatal(err)
	}
	return sol, jup, writeShadowPolicy(t, sol), writeShadowPolicy(t, jup)
}
