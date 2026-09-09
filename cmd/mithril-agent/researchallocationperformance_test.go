package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestResearchAllocationChampionUsesDayPinnedPolicy(t *testing.T) {
	generation, now := researchAllocationPerformanceFixture(t)
	base := adaptiveShadowSearchPolicy()
	base.InputAmount = 20_000_000
	base.MinimumOrderValueMicros, base.MaximumOrderValueMicros = 1_000_000, 100_000_000
	base.Adaptive.MaxDrawdownBPS = 750
	var prices []uint64
	for range 8 {
		prices = append(prices, 100_000_000, 98_000_000, 96_000_000, 94_000_000, 94_000_000, 96_000_000, 98_000_000, 100_000_000, 102_000_000, 102_000_000)
	}
	result, err := searchShadowCandidate(base, prices, prices, 100)
	if err != nil {
		t.Fatal(err)
	}
	result.TrainDay, result.ValidationDay = "2026-08-17", "2026-08-18"
	candidate, err := newShadowPaperCandidate(base, result,
		shadowJournalProvenance{Day: result.TrainDay, Records: len(prices) + 1, ChainHeadSHA256: strings.Repeat("a", 64)},
		shadowJournalProvenance{Day: result.ValidationDay, Records: len(prices) + 1, ChainHeadSHA256: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	baseSHA, err := base.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if baseSHA == candidate.CandidatePolicySHA256 {
		t.Fatal("fixture must select a different policy")
	}
	write := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	policyPath := filepath.Join(generation, "sol-policy.json")
	write(policyPath, base)
	capital, err := shadowPortfolioCapital(base, 300_000_000)
	if err != nil {
		t.Fatal(err)
	}
	instruction := paperdashboard.Instruction{Version: paperdashboard.InstructionVersion, UpdatedAt: now.Add(-time.Hour), Market: "all", Preference: "balanced",
		PaperCapitalMicros: capital, MinimumOrderMicros: 1_000_000, MaximumOrderMicros: 100_000_000, CadenceSeconds: base.TickSeconds, MaxDrawdownBPS: 750}
	instructionPath := filepath.Join(generation, "instruction.json")
	write(instructionPath, instruction)
	digest, err := paperdashboard.InstructionSHA256(instruction)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(generation, "portfolio.json"), shadowPortfolioManifest{Version: shadowPortfolioVersion, Status: "paper_portfolio", PaperOnly: true, InstructionSHA256: digest, TotalCapitalLimitMicros: capital, MaxSOLUSDMicros: 300_000_000,
		Books: []shadowPortfolioBook{{ID: "sol", Market: base.Market, PolicyPath: policyPath, PolicySHA256: baseSHA}}})
	candidate.Experiment, err = loadShadowPaperExperiment(instructionPath, candidate.Policy)
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(generation, "selection", "sol", "champion", "candidate.json")
	if err := writeShadowPaperCandidate(candidatePath, candidate); err != nil {
		t.Fatal(err)
	}
	_, candidateSHA, err := loadBoundShadowPaperCandidate(candidatePath, base)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(generation, "selection", "sol", "champion", "active.json"), shadowCandidatePointer{Version: shadowPaperCandidateVersion, CandidatePath: candidatePath, CandidateSHA256: candidateSHA, CandidatePolicySHA256: candidate.CandidatePolicySHA256})
	if err := os.WriteFile(filepath.Join(generation, "status", "sol", "champion-owned"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(generation, "runs", "sol", "champion")
	pinned := filepath.Join(root, baseSHA)
	if err := os.MkdirAll(pinned, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureShadowPolicySnapshot(pinned, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pinned, "shadow-"+dayKey(now)+".jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Resolver-only: the existing prefix integration tests cover replay. An empty
	// current-day file still pins startup identity but cannot fabricate P&L.
	source, err := resolveResearchAllocation(generation, "sol", "champion", now)
	if err != nil {
		t.Fatal(err)
	}
	actualSHA, err := source.policy.Fingerprint()
	if err != nil || actualSHA != baseSHA || source.directory != pinned || source.binding.SelectedCandidatePolicySHA256 != candidate.CandidatePolicySHA256 {
		t.Fatalf("selected pointer replaced day-pinned policy: %+v %v", source, err)
	}
	quotes := researchAllocationQuoteFunc(func(_ context.Context, request jupiterquote.Request) (jupiterquote.Result, error) {
		return researchAllocationQuoteResult(request, now), nil
	})
	quoted, err := collectResearchAllocationQuotes(t.Context(), generation, "sol", "champion", time.Minute, quotes, func() time.Time { return now }, false)
	if err != nil || quoted.PolicySHA256 != baseSHA || quoted.Binding.SelectedCandidatePolicySHA256 != candidate.CandidatePolicySHA256 || quoted.Initial.InputAmount != base.InputAmount {
		t.Fatalf("allocation quotes did not retain day-pinned policy: %+v %v", quoted, err)
	}
	other := filepath.Join(root, candidate.CandidatePolicySHA256)
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureShadowPolicySnapshot(other, candidate.Policy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "shadow-"+dayKey(now)+".jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveResearchAllocation(generation, "sol", "champion", now); err == nil {
		t.Fatal("ambiguous current-day journals resolved to a champion policy")
	}
	var output bytes.Buffer
	if err := runResearchAllocationPerformance([]string{"--generation", generation, "--market", "sol", "--role", "champion"}, &output, func() time.Time { return now }); err == nil || output.Len() != 0 {
		t.Fatalf("ambiguous champion produced output: %v %s", err, output.String())
	}
}

func researchAllocationPerformanceFixture(t *testing.T) (string, time.Time) {
	t.Helper()
	policy, source, _, now := researchPerformanceFixture(t, false)
	return researchAllocationJournalFixture(t, policy, source.directory, now), now
}

func researchAllocationJournalFixture(t *testing.T, policy shadow.Policy, directory string, now time.Time) string {
	t.Helper()
	market := "sol"
	if shadowMarketPair(policy) == "JUP/USDC" {
		market = "jup"
	}
	generation := privateTestDirectory(t)
	role := filepath.Join(generation, "runs", market, "pre-champion")
	for _, path := range []string{role, filepath.Join(generation, "status", market), filepath.Join(generation, "selection", market, "champion")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{".jsonl", ".jsonl.prefix.json"} {
		name := "shadow-" + dayKey(now) + suffix
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(role, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	policyPath := filepath.Join(generation, market+"-policy.json")
	write(policyPath, policy)
	capital, err := shadowPortfolioCapital(policy, 300_000_000)
	if err != nil {
		t.Fatal(err)
	}
	instruction := paperdashboard.Instruction{Version: paperdashboard.InstructionVersion,
		UpdatedAt: now.Add(-time.Hour), Market: "all", Preference: "balanced",
		PaperCapitalMicros: capital, MinimumOrderMicros: 5_000_000, MaximumOrderMicros: min(100_000_000, capital),
		CadenceSeconds: 15, MaxDrawdownBPS: 750}
	digest, err := paperdashboard.InstructionSHA256(instruction)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(generation, "instruction.json"), instruction)
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(generation, "portfolio.json"), shadowPortfolioManifest{
		Version: shadowPortfolioVersion, Status: "paper_portfolio", PaperOnly: true,
		InstructionSHA256: digest, TotalCapitalLimitMicros: capital, MaxSOLUSDMicros: 300_000_000,
		Books: []shadowPortfolioBook{{ID: market, Market: policy.Market, PolicyPath: policyPath, PolicySHA256: fingerprint}},
	})
	return generation
}

func TestResearchAllocationPerformancePreservesLosses(t *testing.T) {
	generation, now := researchAllocationPerformanceFixture(t)
	var output bytes.Buffer
	if err := runResearchAllocationPerformance([]string{"--generation", generation, "--market", "sol", "--role", "pre-champion", "--max-age", "2m"}, &output, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	var got researchPerformance
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Fills == 0 || got.RealizedMicros >= 0 || got.UnrealizedMicros >= 0 || got.FeesMicros <= 0 || !got.DiagnosticOnly || !got.PaperOnly || got.RecordedBasisEligible {
		t.Fatalf("allocation projection lost accounting or scope: %+v", got)
	}
	if bytes.Contains(output.Bytes(), []byte(generation)) {
		t.Fatal("projection exposed generation's private path")
	}
	var provenance struct {
		RoleBindingVerified   bool `json:"role_binding_verified"`
		ProcessHealthVerified bool `json:"process_health_verified"`
		Binding               struct {
			GenerationPathSHA256 string `json:"generation_path_sha256"`
			InstructionSHA256    string `json:"instruction_sha256"`
			PortfolioSHA256      string `json:"portfolio_sha256"`
			BasePolicySHA256     string `json:"base_policy_sha256"`
			Role                 string `json:"role"`
			BookID               string `json:"book_id"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(output.Bytes(), &provenance); err != nil {
		t.Fatal(err)
	}
	if !provenance.RoleBindingVerified || provenance.ProcessHealthVerified || provenance.Binding.Role != "pre-champion" || provenance.Binding.BookID != "sol" {
		t.Fatalf("incorrect role/provenance scope: %+v", provenance)
	}
	for _, digest := range []string{provenance.Binding.GenerationPathSHA256, provenance.Binding.InstructionSHA256, provenance.Binding.PortfolioSHA256, provenance.Binding.BasePolicySHA256} {
		if !validLowerSHA256(digest) {
			t.Fatalf("missing binding digest: %+v", provenance)
		}
	}
}

func TestResearchAllocationPerformanceProtectedGenerationModes(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  os.FileMode
		valid bool
	}{
		{"group readable deployed generation", 0750, true},
		{"group writable generation", 0770, false},
		{"world writable generation", 0702, false},
		{"sticky writable generation", os.ModeSticky | 0777, false},
		{"group readable role journal", 0750, false},
		{"symlink generation", 0750, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			generation, now := researchAllocationPerformanceFixture(t)
			if err := os.Chmod(generation, test.mode); err != nil {
				t.Fatal(err)
			}
			role := filepath.Join(generation, "runs", "sol", "pre-champion")
			for path, want := range map[string]os.FileMode{
				role: 0700,
				filepath.Join(generation, "sol-policy.json"):                    0600,
				filepath.Join(role, "shadow-"+dayKey(now)+".jsonl"):             0600,
				filepath.Join(role, "shadow-"+dayKey(now)+".jsonl.prefix.json"): 0600,
			} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != want {
					t.Fatalf("fixture private evidence mode for %s: %v", path, err)
				}
			}
			if test.name == "group readable role journal" {
				if err := os.Chmod(role, 0750); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "symlink generation" {
				link := filepath.Join(privateTestDirectory(t), "generation")
				if err := os.Symlink(generation, link); err != nil {
					t.Fatal(err)
				}
				generation = link
			}
			var output bytes.Buffer
			err := runResearchAllocationPerformance([]string{"--generation", generation, "--market", "sol", "--role", "pre-champion", "--max-age", "2m"}, &output, func() time.Time { return now })
			if test.valid {
				if err != nil || output.Len() == 0 {
					t.Fatalf("protected deployed generation rejected: %v", err)
				}
			} else if err == nil || output.Len() != 0 {
				t.Fatalf("writable generation accepted: %v %s", err, output.String())
			}
		})
	}
}

func TestResearchAllocationPerformanceRejectsMisboundOrWrongRole(t *testing.T) {
	for _, mode := range []string{"wrong role", "champion owned", "instruction mismatch", "capital mismatch", "base only", "wrong market", "midnight", "ownership changed"} {
		t.Run(mode, func(t *testing.T) {
			generation, now := researchAllocationPerformanceFixture(t)
			role, market := "pre-champion", "sol"
			switch mode {
			case "wrong role":
				role = "champion"
			case "champion owned":
				if err := os.WriteFile(filepath.Join(generation, "status", "sol", "champion-owned"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "instruction mismatch", "capital mismatch":
				path := filepath.Join(generation, "portfolio.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var portfolio shadowPortfolioManifest
				if err := json.Unmarshal(raw, &portfolio); err != nil {
					t.Fatal(err)
				}
				if mode == "instruction mismatch" {
					portfolio.InstructionSHA256 = string(bytes.Repeat([]byte("a"), 64))
				} else {
					portfolio.TotalCapitalLimitMicros += 1_000_000
				}
				raw, err = json.Marshal(portfolio)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "base only":
				if err := os.Rename(filepath.Join(generation, "runs", "sol", "pre-champion"), filepath.Join(generation, "runs", "sol", "base")); err != nil {
					t.Fatal(err)
				}
			case "wrong market":
				market = "jup"
			}
			calls := 0
			clock := func() time.Time {
				calls++
				if mode == "ownership changed" && calls == 2 {
					if err := os.WriteFile(filepath.Join(generation, "status", "sol", "champion-owned"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "midnight" && calls > 1 {
					return now.Add(24 * time.Hour)
				}
				return now
			}
			var output bytes.Buffer
			if err := runResearchAllocationPerformance([]string{"--generation", generation, "--market", market, "--role", role, "--max-age", "2m"}, &output, clock); err == nil || output.Len() != 0 {
				t.Fatalf("invalid allocation produced output: %v %s", err, output.String())
			}
		})
	}
}
