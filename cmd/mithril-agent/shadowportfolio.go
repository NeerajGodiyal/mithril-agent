package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	shadowPortfolioLegacyVersion = uint32(1)
	shadowPortfolioVersion       = uint32(2)
	shadowPortfolioMaxBooks      = 16
)

const shadowPortfolioUsage = `Usage: mithril-agent shadow portfolio --out PATH
       --limit-usd N --max-sol-usd N --book ID=POLICY [--book ID=POLICY ...]

Writes or atomically replaces one private paper-only capital manifest. A book is counted once even
when base, champion, and challenger observers run as counterfactual copies.
The manifest cannot authorize, sign, submit, or configure live trading.`

type shadowPortfolioManifest struct {
	Version                 uint32                `json:"version"`
	Status                  string                `json:"status"`
	PaperOnly               bool                  `json:"paper_only"`
	Authorized              bool                  `json:"authorized"`
	InstructionSHA256       string                `json:"instruction_sha256,omitempty"`
	TotalCapitalLimitMicros uint64                `json:"total_capital_limit_micros"`
	MaxSOLUSDMicros         uint64                `json:"max_sol_usd_micros"`
	Books                   []shadowPortfolioBook `json:"books"`
}

type shadowPortfolioBook struct {
	ID           string `json:"id"`
	Market       string `json:"market"`
	PolicyPath   string `json:"policy_path"`
	PolicySHA256 string `json:"policy_sha256"`
}

type shadowPortfolioBooks []shadowPortfolioBook

func (books *shadowPortfolioBooks) String() string {
	items := make([]string, 0, len(*books))
	for _, book := range *books {
		items = append(items, book.ID+"="+book.PolicyPath)
	}
	return strings.Join(items, ",")
}

func (books *shadowPortfolioBooks) Set(value string) error {
	id, path, found := strings.Cut(value, "=")
	if !found || !validShadowPortfolioID(id) || !absoluteClean(path) {
		return errors.New("--book must be ID=/absolute/clean/policy.json")
	}
	*books = append(*books, shadowPortfolioBook{ID: id, PolicyPath: path})
	return nil
}

func runShadowPortfolio(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow portfolio", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outPath := flags.String("out", "", "new private portfolio manifest")
	limitUSD := flags.String("limit-usd", "", "total paper capital limit")
	maxSOLUSD := flags.String("max-sol-usd", "", "conservative SOL/USD planning ceiling")
	var books shadowPortfolioBooks
	flags.Var(&books, "book", "ID=/absolute/policy.json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowPortfolioUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !absoluteClean(*outPath) || *limitUSD == "" ||
		*maxSOLUSD == "" || len(books) == 0 {
		return errors.New("shadow portfolio requires --out, --limit-usd, --max-sol-usd, and at least one --book")
	}
	limit, err := parseUSDThreshold(*limitUSD, "paper portfolio limit")
	if err != nil {
		return err
	}
	maximumSOL, err := parseUSDThreshold(*maxSOLUSD, "paper portfolio SOL/USD ceiling")
	if err != nil {
		return err
	}
	sort.Slice(books, func(left, right int) bool { return books[left].ID < books[right].ID })
	manifest := shadowPortfolioManifest{
		Version: shadowPortfolioLegacyVersion, Status: "paper_portfolio", PaperOnly: true,
		TotalCapitalLimitMicros: limit, MaxSOLUSDMicros: maximumSOL, Books: books,
	}
	for index := range manifest.Books {
		policy, loadErr := loadActiveShadowPolicy(manifest.Books[index].PolicyPath)
		if loadErr != nil {
			return errors.New("paper portfolio policy is invalid")
		}
		manifest.Books[index].Market = policy.Market
		manifest.Books[index].PolicySHA256, err = policy.Fingerprint()
		if err != nil {
			return errors.New("paper portfolio policy identity is invalid")
		}
	}
	total, _, err := validateShadowPortfolio(manifest)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := securefile.ReplacePrivate(*outPath, append(encoded, '\n'), maxInputBytes); err != nil {
		return err
	}
	return writeShadowMarketJSON(output, struct {
		Status                  string `json:"status"`
		PaperOnly               bool   `json:"paper_only"`
		Authorized              bool   `json:"authorized"`
		Books                   int    `json:"books"`
		CapitalAtCeilingMicros  uint64 `json:"capital_at_ceiling_micros"`
		TotalCapitalLimitMicros uint64 `json:"total_capital_limit_micros"`
	}{"paper_portfolio_written", true, false, len(manifest.Books), total, limit})
}

func loadShadowPortfolioForBook(
	manifestPath, bookID, policyPath string,
	base shadow.Policy,
) (uint64, error) {
	maximum, _, _, err := loadShadowPortfolioBindingForBook(manifestPath, bookID, policyPath, base)
	return maximum, err
}

func loadShadowPortfolioBindingForBook(
	manifestPath, bookID, policyPath string,
	base shadow.Policy,
) (uint64, string, uint64, error) {
	if !absoluteClean(manifestPath) || !validShadowPortfolioID(bookID) {
		return 0, "", 0, errors.New("shadow run portfolio path and book ID are invalid")
	}
	var manifest shadowPortfolioManifest
	if err := readStrictJSON(manifestPath, &manifest); err != nil {
		return 0, "", 0, errors.New("shadow run portfolio manifest is invalid")
	}
	_, policies, err := validateShadowPortfolio(manifest)
	if err != nil {
		return 0, "", 0, err
	}
	for _, book := range manifest.Books {
		if book.ID != bookID {
			continue
		}
		fingerprint, fingerprintErr := base.Fingerprint()
		cleanPolicy, cleanErr := cleanExistingPath(policyPath)
		if fingerprintErr != nil || cleanErr != nil || cleanPolicy != book.PolicyPath ||
			fingerprint != book.PolicySHA256 || policies[bookID].Market != base.Market {
			return 0, "", 0, errors.New("shadow run policy does not match its portfolio book")
		}
		return manifest.MaxSOLUSDMicros, manifest.InstructionSHA256, manifest.TotalCapitalLimitMicros, nil
	}
	return 0, "", 0, errors.New("shadow run portfolio book is not present")
}

func validateShadowPortfolio(
	manifest shadowPortfolioManifest,
) (uint64, map[string]shadow.Policy, error) {
	if manifest.Version != shadowPortfolioLegacyVersion && manifest.Version != shadowPortfolioVersion ||
		manifest.Version == shadowPortfolioLegacyVersion && manifest.InstructionSHA256 != "" ||
		manifest.Version == shadowPortfolioVersion && !validLowerSHA256(manifest.InstructionSHA256) ||
		manifest.Status != "paper_portfolio" ||
		!manifest.PaperOnly || manifest.Authorized || manifest.TotalCapitalLimitMicros == 0 ||
		manifest.TotalCapitalLimitMicros > math.MaxInt64 || manifest.MaxSOLUSDMicros == 0 ||
		len(manifest.Books) == 0 || len(manifest.Books) > shadowPortfolioMaxBooks {
		return 0, nil, errors.New("paper portfolio safety markers or limits are invalid")
	}
	policies := make(map[string]shadow.Policy, len(manifest.Books))
	markets := make(map[string]struct{}, len(manifest.Books))
	paths := make(map[string]struct{}, len(manifest.Books))
	var total uint64
	previousID := ""
	for _, book := range manifest.Books {
		if !validShadowPortfolioID(book.ID) || !absoluteClean(book.PolicyPath) ||
			previousID != "" && book.ID <= previousID {
			return 0, nil, errors.New("paper portfolio books must have unique sorted IDs and clean paths")
		}
		previousID = book.ID
		if _, duplicate := paths[book.PolicyPath]; duplicate {
			return 0, nil, errors.New("paper portfolio policy paths must be unique")
		}
		policy, err := loadActiveShadowPolicy(book.PolicyPath)
		if err != nil {
			return 0, nil, errors.New("paper portfolio policy is invalid")
		}
		fingerprint, err := policy.Fingerprint()
		if err != nil || book.Market != policy.Market || book.PolicySHA256 != fingerprint {
			return 0, nil, errors.New("paper portfolio policy identity does not match")
		}
		if _, duplicate := markets[policy.Market]; duplicate {
			return 0, nil, errors.New("paper portfolio markets must be unique")
		}
		capital, err := shadowPortfolioCapital(policy, manifest.MaxSOLUSDMicros)
		if err != nil || capital > math.MaxInt64-total {
			return 0, nil, errors.New("paper portfolio capital is not representable")
		}
		total += capital
		markets[policy.Market], paths[book.PolicyPath], policies[book.ID] = struct{}{}, struct{}{}, policy
	}
	if total > manifest.TotalCapitalLimitMicros {
		return 0, nil, errors.New("paper portfolio exceeds its total capital limit")
	}
	return total, policies, nil
}

func validateShadowPortfolioCandidateBinding(
	candidateSelected bool,
	portfolioInstructionSHA256 string,
	portfolioPaperCapitalMicros uint64,
	candidateInstructionSHA256 string,
	candidatePaperCapitalMicros uint64,
) error {
	if !candidateSelected || portfolioInstructionSHA256 == "" {
		return nil
	}
	if candidateInstructionSHA256 == "" ||
		portfolioInstructionSHA256 != candidateInstructionSHA256 ||
		portfolioPaperCapitalMicros != candidatePaperCapitalMicros {
		return errors.New("shadow paper candidate instruction does not match its portfolio")
	}
	return nil
}

func shadowPortfolioCapital(policy shadow.Policy, maxSOLUSDMicros uint64) (uint64, error) {
	baseUnits := policy.StartingOutputUnits
	if policy.IsSell() {
		baseUnits = policy.StartingInputUnits
	}
	if policy.Market != shadow.MarketSOLUSDC && baseUnits != 0 {
		return 0, errors.New("non-SOL paper portfolio books must start without base inventory")
	}
	if policy.NativeFeePrice != nil {
		ledger, err := shadow.NewLedger(policy, 1, maxSOLUSDMicros)
		if err != nil {
			return 0, err
		}
		return ledger.OpeningEquityMicros, nil
	}
	ledger, err := shadow.NewLedger(policy, maxSOLUSDMicros)
	if err != nil {
		return 0, err
	}
	return ledger.OpeningEquityMicros, nil
}

func validShadowPortfolioID(value string) bool {
	if value == "" || len(value) > 32 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '-' && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}
