package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const researchAllocationPerformanceUsage = `Usage: mithril-agent research allocation-performance --generation DIR --market sol|jup --role pre-champion|champion [--max-age 2m]

Reads the current-day durable paper prefix bound to an allocation instruction,
portfolio and status-owning role. A champion's day-pinned policy can differ from
its next selected policy. Rechecks identity before emitting partial diagnostic
results. The host must separately verify the active selector and service health.
No fallback to base journals, policy changes or live trading authority.`

type researchAllocationBinding struct {
	GenerationPathSHA256          string `json:"generation_path_sha256"`
	InstructionSHA256             string `json:"instruction_sha256"`
	PortfolioSHA256               string `json:"portfolio_sha256"`
	BasePolicySHA256              string `json:"base_policy_sha256"`
	SelectedPointerSHA256         string `json:"selected_pointer_sha256,omitempty"`
	SelectedCandidatePolicySHA256 string `json:"selected_candidate_policy_sha256,omitempty"`
	Role                          string `json:"role"`
	BookID                        string `json:"book_id"`
}

type researchAllocationPerformance struct {
	researchPerformance
	Binding               researchAllocationBinding `json:"binding"`
	RoleBindingVerified   bool                      `json:"role_binding_verified"`
	ProcessHealthVerified bool                      `json:"process_health_verified"`
}

type researchAllocationSource struct {
	policy    shadow.Policy
	directory string
	binding   researchAllocationBinding
	marker    os.FileInfo
}

func runResearchAllocationPerformance(args []string, output io.Writer, now func() time.Time) error {
	flags := flag.NewFlagSet("research allocation-performance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	generation := flags.String("generation", "", "exact protected allocation generation")
	market := flags.String("market", "", "sol or jup")
	role := flags.String("role", "", "pre-champion or champion")
	maxAge := flags.Duration("max-age", 2*time.Minute, "maximum prefix and valuation age")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchAllocationPerformanceUsage)
		}
		return err
	}
	if flags.NArg() != 0 || now == nil || *maxAge <= 0 || *maxAge > 24*time.Hour {
		return errors.New("allocation performance requires a clock and positive age no greater than one day")
	}
	started := now().UTC()
	source, err := resolveResearchAllocation(*generation, *market, *role, started)
	if err != nil {
		return err
	}
	performance, err := buildResearchPerformance(source.policy, source.directory, started, *maxAge)
	if err != nil {
		return err
	}
	finished := now().UTC()
	after, err := resolveResearchAllocation(*generation, *market, *role, finished)
	if err != nil {
		return err
	}
	if finished.Before(started) || dayKey(started) != dayKey(finished) ||
		!sameResearchAllocation(source, after) ||
		finished.Sub(performance.ObservedThrough) > *maxAge || finished.Sub(performance.MarkPublishedAt) > *maxAge {
		return errors.New("allocation performance identity or freshness changed during collection")
	}
	performance.CheckedAt = finished
	return json.NewEncoder(output).Encode(researchAllocationPerformance{
		researchPerformance: performance, Binding: source.binding, RoleBindingVerified: true,
	})
}

// sameResearchAllocation compares the resolved policy and role, including a
// champion's ownership marker and day-pinned journal directory.
func sameResearchAllocation(before, after researchAllocationSource) bool {
	beforeSHA, beforeErr := before.policy.Fingerprint()
	afterSHA, afterErr := after.policy.Fingerprint()
	markerUnchanged := before.marker == nil && after.marker == nil
	if before.marker != nil && after.marker != nil {
		markerUnchanged = os.SameFile(before.marker, after.marker) && before.marker.ModTime() == after.marker.ModTime() && before.marker.Size() == after.marker.Size()
	}
	return beforeErr == nil && afterErr == nil && beforeSHA == afterSHA &&
		before.binding == after.binding && before.directory == after.directory && markerUnchanged
}

func resolveResearchAllocation(generation, market, role string, now time.Time) (researchAllocationSource, error) {
	info, statErr := os.Lstat(generation)
	if now.IsZero() || !cleanResearchPath(generation) || statErr != nil ||
		!info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 ||
		secureexec.ValidateProtectedDirectory(generation) != nil ||
		(market != "sol" && market != "jup") || (role != "pre-champion" && role != "champion") {
		return researchAllocationSource{}, errors.New("allocation performance requires a protected generation and supported market role")
	}
	source := researchAllocationSource{binding: researchAllocationBinding{
		GenerationPathSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(generation))), Role: role, BookID: market,
	}}
	markerPath := filepath.Join(generation, "status", market, "champion-owned")
	marker, markerErr := os.Lstat(markerPath)
	if role == "champion" {
		if markerErr != nil || validatePrivateFile(markerPath) != nil || !marker.Mode().IsRegular() || marker.Size() != 0 {
			return researchAllocationSource{}, errors.New("allocation champion ownership marker is unavailable")
		}
		source.marker = marker
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return researchAllocationSource{}, errors.New("allocation pre-champion no longer owns status")
	}
	instruction, instructionSHA, err := paperdashboard.LoadInstruction(filepath.Join(generation, "instruction.json"))
	if err != nil || instruction.Version != paperdashboard.InstructionVersion || instruction.Market != "all" {
		return researchAllocationSource{}, errors.New("allocation performance instruction is unavailable")
	}
	policyPath := filepath.Join(generation, market+"-policy.json")
	base, err := loadActiveShadowPolicy(policyPath)
	if err != nil {
		return researchAllocationSource{}, err
	}
	wantMarket := "SOL/USDC"
	if market == "jup" {
		wantMarket = "JUP/USDC"
	}
	if base.Cluster != shadow.Mainnet || shadowMarketPair(base) != wantMarket {
		return researchAllocationSource{}, errors.New("allocation performance market does not match its policy")
	}
	portfolioPath := filepath.Join(generation, "portfolio.json")
	portfolioRaw, err := securefile.ReadPrivate(portfolioPath, maxInputBytes)
	if err != nil {
		return researchAllocationSource{}, err
	}
	_, portfolioInstruction, capital, err := loadShadowPortfolioBindingForBook(portfolioPath, market, policyPath, base)
	if err != nil || portfolioInstruction != instructionSHA || capital != instruction.PaperCapitalMicros {
		return researchAllocationSource{}, errors.New("allocation performance portfolio and instruction do not match")
	}
	source.binding.InstructionSHA256 = instructionSHA
	source.binding.PortfolioSHA256 = fmt.Sprintf("%x", sha256.Sum256(portfolioRaw))
	source.binding.BasePolicySHA256, err = base.Fingerprint()
	if err != nil {
		return researchAllocationSource{}, err
	}
	source.policy = base
	source.directory = filepath.Join(generation, "runs", market, role)
	if err := validatePrivateDirectory(source.directory); err != nil {
		return researchAllocationSource{}, err
	}
	if role == "champion" {
		pointer := filepath.Join(generation, "selection", market, role, "active.json")
		pointerRaw, err := securefile.ReadPrivate(pointer, shadowCandidatePointerBytes)
		if err != nil {
			return researchAllocationSource{}, err
		}
		candidate, _, _, _, err := loadBoundShadowCandidateSelection(pointer, base)
		if err != nil {
			return researchAllocationSource{}, err
		}
		if candidate.Experiment == nil || validateShadowPortfolioCandidateBinding(true, instructionSHA, capital,
			candidate.Experiment.InstructionSHA256, candidate.Experiment.Instruction.PaperCapitalMicros) != nil {
			return researchAllocationSource{}, errors.New("allocation performance candidate instruction does not match")
		}
		source.binding.SelectedPointerSHA256 = fmt.Sprintf("%x", sha256.Sum256(pointerRaw))
		source.binding.SelectedCandidatePolicySHA256 = candidate.CandidatePolicySHA256
		var actualSHA string
		source.policy, actualSHA, err = resolveStartupShadowPolicy(base, candidate.Policy, candidate.CandidatePolicySHA256, source.directory, now)
		if err != nil {
			return researchAllocationSource{}, err
		}
		source.directory = filepath.Join(source.directory, actualSHA)
	}
	return source, nil
}
