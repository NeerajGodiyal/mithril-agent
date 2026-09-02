// Package marketadmission collects and evaluates candidate-market evidence.
// A qualified artifact proves only source and route quality; it cannot start a
// runner, authorize paper risk, sign, submit, or claim profitability.
package marketadmission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/bits"
	"sort"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	Version = uint32(1)

	ProvisionalStatus                 = "development_provisional"
	ProvisionalWindowHours            = uint16(6)
	ProvisionalMinimumAvailabilityBPS = uint16(9_500)

	mainnetUSDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	tokenProgram    = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"

	MarketWIFUSDC  = "WIF/USDC"
	MarketJTOUSDC  = "JTO/USDC"
	MarketPYTHUSDC = "PYTH/USDC"

	EventOpened   = "market_admission.opened"
	EventObserved = "market_admission.observed"
)

const (
	FailureMintState   = "mint_state_unavailable"
	FailureMarketPrice = "market_price_unavailable"
	FailureQuotePeg    = "quote_peg_unavailable"
	FailureNativePrice = "native_fee_price_unavailable"
	FailureBuyQuote    = "buy_quote_unavailable"
	FailureSellQuote   = "sell_quote_unavailable"
)

type Candidate struct {
	Version                 uint32                   `json:"version"`
	Market                  string                   `json:"market"`
	BaseMint                string                   `json:"base_mint"`
	QuoteMint               string                   `json:"quote_mint"`
	TokenProgram            string                   `json:"token_program"`
	BaseDecimals            uint8                    `json:"base_decimals"`
	MintAuthorityDisabled   bool                     `json:"mint_authority_disabled"`
	FreezeAuthorityDisabled bool                     `json:"freeze_authority_disabled"`
	Pyth                    pricesource.PythPushSpec `json:"pyth"`
	Kraken                  pricesource.KrakenSpec   `json:"kraken"`
	QuoteNotionalUSDC       uint64                   `json:"quote_notional_usdc"`
	QuoteSlippageBPS        uint16                   `json:"quote_slippage_bps"`
}

func Lookup(market string) (Candidate, bool) {
	var candidate Candidate
	switch market {
	case MarketWIFUSDC:
		pyth, err := pricesource.NewPythPushSpec(
			"WIF/USD",
			"4ca4beeca86f0d164160323817a4e42b10010a724c2217c6ee41b54cd4cc61fc",
			"6B23K3tkb51vLZA14jcEQVCA1pfHptzEHFA93V5dYwbT",
		)
		if err != nil {
			return Candidate{}, false
		}
		candidate = Candidate{
			Version: Version, Market: MarketWIFUSDC,
			BaseMint:  "EKpQGSJtjMFqKZ9KQanSqYXRcF8fBopzLHYxdM65zcjm",
			QuoteMint: mainnetUSDCMint, TokenProgram: tokenProgram, BaseDecimals: 6,
			MintAuthorityDisabled: true, FreezeAuthorityDisabled: true,
			Pyth: pyth, Kraken: pricesource.KrakenSpec{Feed: "WIF/USD", Product: "WIF/USD"},
			QuoteNotionalUSDC: 25_000_000, QuoteSlippageBPS: 100,
		}
	case MarketJTOUSDC:
		pyth, err := pricesource.NewPythPushSpecFromFeed(
			"JTO/USD",
			"b43660a5f790c69354b0729a5ef9d50d68f1df92107540210b9cccba1f947cc2",
		)
		if err != nil {
			return Candidate{}, false
		}
		candidate = Candidate{
			Version: Version, Market: MarketJTOUSDC,
			BaseMint:  "jtojtomepa8beP8AuQc6eXt5FriJwfFMwQx2v2f9mCL",
			QuoteMint: mainnetUSDCMint, TokenProgram: tokenProgram, BaseDecimals: 9,
			MintAuthorityDisabled: true, FreezeAuthorityDisabled: true,
			Pyth: pyth, Kraken: pricesource.KrakenSpec{Feed: "JTO/USD", Product: "JTO/USD"},
			QuoteNotionalUSDC: 25_000_000, QuoteSlippageBPS: 100,
		}
	case MarketPYTHUSDC:
		pyth, err := pricesource.NewPythPushSpecFromFeed(
			"PYTH/USD",
			"0bbf28e9a841a1cc788f6a361b17ca072d0ea3098a1e5df1c3922d06719579ff",
		)
		if err != nil {
			return Candidate{}, false
		}
		candidate = Candidate{
			Version: Version, Market: MarketPYTHUSDC,
			BaseMint:  "HZ1JovNiVvGrGNiiYvEozEVgZ58xaU3RKwX8eACQBCt3",
			QuoteMint: mainnetUSDCMint, TokenProgram: tokenProgram, BaseDecimals: 6,
			MintAuthorityDisabled: true, FreezeAuthorityDisabled: true,
			Pyth: pyth, Kraken: pricesource.KrakenSpec{Feed: "PYTH/USD", Product: "PYTH/USD"},
			QuoteNotionalUSDC: 25_000_000, QuoteSlippageBPS: 100,
		}
	default:
		return Candidate{}, false
	}
	if candidate.Validate() != nil {
		return Candidate{}, false
	}
	return candidate, true
}

// Markets returns the code-owned observation allowlist in display order.
func Markets() []string {
	return []string{MarketWIFUSDC, MarketJTOUSDC, MarketPYTHUSDC}
}

func (candidate Candidate) Validate() error {
	if candidate.Version != Version || candidate.QuoteMint != mainnetUSDCMint ||
		candidate.TokenProgram != tokenProgram || candidate.BaseMint == candidate.QuoteMint {
		return errors.New("market candidate identity is invalid")
	}
	for _, address := range []string{candidate.BaseMint, candidate.QuoteMint, candidate.TokenProgram} {
		if _, err := solana.Decode32(address); err != nil {
			return errors.New("market candidate address is invalid")
		}
	}
	base, ok := strings.CutSuffix(candidate.Market, "/USDC")
	if !ok || candidate.Pyth.Feed != base+"/USD" || candidate.Kraken.Feed != base+"/USD" ||
		candidate.BaseDecimals == 0 || candidate.BaseDecimals > 12 ||
		!candidate.MintAuthorityDisabled || !candidate.FreezeAuthorityDisabled {
		return errors.New("market candidate metadata is invalid")
	}
	if err := candidate.Pyth.Validate(); err != nil {
		return err
	}
	if err := candidate.Kraken.Validate(); err != nil {
		return err
	}
	if candidate.QuoteNotionalUSDC < 10_000_000 || candidate.QuoteNotionalUSDC > 1_000_000_000 ||
		candidate.QuoteSlippageBPS == 0 || candidate.QuoteSlippageBPS > 500 {
		return errors.New("market quote probe is outside policy")
	}
	return nil
}

func (candidate Candidate) Fingerprint() (string, error) {
	if err := candidate.Validate(); err != nil {
		return "", err
	}
	return digest(candidate)
}

type Thresholds struct {
	Version                   uint32 `json:"version"`
	CadenceSeconds            uint64 `json:"cadence_seconds"`
	MinimumDays               uint16 `json:"minimum_days"`
	MinimumAvailabilityBPS    uint16 `json:"minimum_availability_bps"`
	MedianRouteCostBPS        uint16 `json:"median_route_cost_bps"`
	P95RouteCostBPS           uint16 `json:"p95_route_cost_bps"`
	MaximumSourceAgeSeconds   uint16 `json:"maximum_source_age_seconds"`
	MaximumSourceSkewSeconds  uint16 `json:"maximum_source_skew_seconds"`
	MaximumSourceDeviationBPS uint16 `json:"maximum_source_deviation_bps"`
	MaximumConfidenceBPS      uint16 `json:"maximum_confidence_bps"`
	MaximumQuoteImpactBPS     uint16 `json:"maximum_quote_impact_bps"`
	MaximumQuoteLatencyMillis uint32 `json:"maximum_quote_latency_millis"`
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		Version: Version, CadenceSeconds: 60, MinimumDays: 30,
		MinimumAvailabilityBPS: 9_900, MedianRouteCostBPS: 20, P95RouteCostBPS: 50,
		MaximumSourceAgeSeconds: 120, MaximumSourceSkewSeconds: 30,
		MaximumSourceDeviationBPS: 200, MaximumConfidenceBPS: 200,
		MaximumQuoteImpactBPS:     500,
		MaximumQuoteLatencyMillis: 15_000,
	}
}

func (thresholds Thresholds) Validate() error {
	if thresholds.Version != Version || thresholds.CadenceSeconds < 5 ||
		thresholds.CadenceSeconds > 3600 || thresholds.MinimumDays < 30 ||
		thresholds.MinimumDays > 365 || thresholds.MinimumAvailabilityBPS == 0 ||
		thresholds.MinimumAvailabilityBPS > 10_000 || thresholds.MedianRouteCostBPS == 0 ||
		thresholds.P95RouteCostBPS < thresholds.MedianRouteCostBPS ||
		thresholds.P95RouteCostBPS > 500 || thresholds.MaximumSourceAgeSeconds == 0 ||
		thresholds.MaximumSourceAgeSeconds > 120 ||
		thresholds.MaximumSourceSkewSeconds > thresholds.MaximumSourceAgeSeconds ||
		thresholds.MaximumSourceDeviationBPS == 0 || thresholds.MaximumSourceDeviationBPS > 500 ||
		thresholds.MaximumConfidenceBPS == 0 || thresholds.MaximumConfidenceBPS > 500 ||
		thresholds.MaximumQuoteImpactBPS == 0 || thresholds.MaximumQuoteImpactBPS > 5_000 ||
		thresholds.MaximumQuoteLatencyMillis == 0 || thresholds.MaximumQuoteLatencyMillis > 60_000 {
		return errors.New("market evidence thresholds are invalid")
	}
	return nil
}

type Opening struct {
	Version         uint32     `json:"version"`
	Candidate       Candidate  `json:"candidate"`
	CandidateSHA256 string     `json:"candidate_sha256"`
	Observe         string     `json:"observe"`
	Thresholds      Thresholds `json:"thresholds"`
	ContentSHA256   string     `json:"content_sha256"`
}

func NewOpening(candidate Candidate, observe string, thresholds Thresholds) (Opening, error) {
	candidateHash, err := candidate.Fingerprint()
	if err != nil {
		return Opening{}, err
	}
	if thresholds != DefaultThresholds() || thresholds.Validate() != nil {
		return Opening{}, errors.New("market evidence must use the code-owned thresholds")
	}
	if _, err := solana.Decode32(observe); err != nil {
		return Opening{}, errors.New("market evidence observe address is invalid")
	}
	opening := Opening{
		Version: Version, Candidate: candidate, CandidateSHA256: candidateHash,
		Observe: observe, Thresholds: thresholds,
	}
	opening.ContentSHA256, err = openingFingerprint(opening)
	return opening, err
}

func (opening Opening) Validate() error {
	if opening.Version != Version || opening.ContentSHA256 == "" {
		return errors.New("market evidence opening is invalid")
	}
	pinned, ok := Lookup(opening.Candidate.Market)
	if !ok {
		return errors.New("market is not on the operator allowlist")
	}
	pinnedHash, err := pinned.Fingerprint()
	if err != nil || pinnedHash != opening.CandidateSHA256 {
		return errors.New("market evidence opening candidate does not match")
	}
	want, err := NewOpening(pinned, opening.Observe, DefaultThresholds())
	if err != nil || want != opening {
		return errors.New("market evidence opening differs from the code-owned contract")
	}
	return nil
}

type MintEvidence struct {
	Address         string `json:"address"`
	Owner           string `json:"owner"`
	Decimals        uint8  `json:"decimals"`
	MintAuthority   string `json:"mint_authority,omitempty"`
	FreezeAuthority string `json:"freeze_authority,omitempty"`
	ContextSlot     uint64 `json:"context_slot"`
	DataSHA256      string `json:"data_sha256"`
}

func (evidence MintEvidence) Validate(candidate Candidate) error {
	if evidence.Address != candidate.BaseMint || evidence.Owner != candidate.TokenProgram ||
		evidence.Decimals != candidate.BaseDecimals || evidence.ContextSlot == 0 ||
		!validDigest(evidence.DataSHA256) ||
		candidate.MintAuthorityDisabled && evidence.MintAuthority != "" ||
		candidate.FreezeAuthorityDisabled && evidence.FreezeAuthority != "" {
		return errors.New("market mint evidence does not match the candidate")
	}
	return nil
}

type Quote struct {
	InputMint       string    `json:"input_mint"`
	OutputMint      string    `json:"output_mint"`
	InputAmount     uint64    `json:"input_amount"`
	EstimatedOutput uint64    `json:"estimated_output"`
	MinimumOutput   uint64    `json:"minimum_output"`
	ReceivedAt      time.Time `json:"received_at"`
	LatencyMillis   uint32    `json:"latency_millis"`
	ResponseSHA256  string    `json:"response_sha256"`
}

type Observation struct {
	Version       uint32    `json:"version"`
	OpeningSHA256 string    `json:"opening_sha256"`
	Bucket        time.Time `json:"bucket"`
	ObservedAt    time.Time `json:"observed_at"`
	Failure       string    `json:"failure,omitempty"`

	Mint            MintEvidence                `json:"mint,omitzero"`
	MarketPrimary   pricesource.PythObservation `json:"market_primary,omitzero"`
	MarketSecondary pricetrigger.Sample         `json:"market_secondary,omitzero"`
	USDCPrimary     pricesource.PythObservation `json:"usdc_primary,omitzero"`
	USDCSecondary   pricetrigger.Sample         `json:"usdc_secondary,omitzero"`
	SOLPrimary      pricesource.PythObservation `json:"sol_primary,omitzero"`
	SOLSecondary    pricetrigger.Sample         `json:"sol_secondary,omitzero"`
	Buy             Quote                       `json:"buy,omitzero"`
	Sell            Quote                       `json:"sell,omitzero"`
}

func (observation Observation) Validate(opening Opening) error {
	if err := opening.Validate(); err != nil {
		return err
	}
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	bucket, observed := observation.Bucket.UTC(), observation.ObservedAt.UTC()
	if observation.Version != Version || observation.OpeningSHA256 != opening.ContentSHA256 ||
		bucket.IsZero() || observed.IsZero() ||
		bucket.Unix()%int64(opening.Thresholds.CadenceSeconds) != 0 ||
		observed.Before(bucket) || !observed.Before(bucket.Add(2*cadence)) ||
		observation.Failure != "" && !validFailure(observation.Failure) {
		return errors.New("market evidence observation envelope is invalid")
	}
	return nil
}

type Artifact struct {
	Version                uint32                `json:"version"`
	Candidate              Candidate             `json:"candidate"`
	CandidateSHA256        string                `json:"candidate_sha256"`
	Observe                string                `json:"observe"`
	OpeningSHA256          string                `json:"opening_sha256"`
	Thresholds             Thresholds            `json:"thresholds"`
	From                   time.Time             `json:"from"`
	Through                time.Time             `json:"through"`
	Journal                journal.DurablePrefix `json:"journal"`
	ExpectedBuckets        uint64                `json:"expected_buckets"`
	ObservedBuckets        uint64                `json:"observed_buckets"`
	AvailableBuckets       uint64                `json:"available_buckets"`
	AvailabilityBPS        uint16                `json:"availability_bps"`
	MedianRouteCostBPS     uint16                `json:"median_route_cost_bps,omitempty"`
	P95RouteCostBPS        uint16                `json:"p95_route_cost_bps,omitempty"`
	OperationallyQualified bool                  `json:"operationally_qualified"`
	Reasons                []string              `json:"reasons,omitempty"`
	ContentSHA256          string                `json:"content_sha256"`
}

// Diagnostic summarizes a recent partial window without changing the 30-day
// qualification contract. It is intentionally not an admission artifact and
// has no digest or loader accepted by policy code.
type Diagnostic struct {
	Version                  uint32            `json:"version"`
	Market                   string            `json:"market"`
	From                     time.Time         `json:"from"`
	Through                  time.Time         `json:"through"`
	DiagnosticOnly           bool              `json:"diagnostic_only"`
	OperationallyQualified   bool              `json:"operationally_qualified"`
	ExpectedBuckets          uint64            `json:"expected_buckets"`
	ObservedBuckets          uint64            `json:"observed_buckets"`
	AvailableBuckets         uint64            `json:"available_buckets"`
	AvailabilityBPS          uint16            `json:"availability_bps"`
	MedianRouteCostBPS       uint16            `json:"median_route_cost_bps,omitempty"`
	P95RouteCostBPS          uint16            `json:"p95_route_cost_bps,omitempty"`
	MedianQuoteLatencyMillis uint32            `json:"median_quote_latency_millis,omitempty"`
	P95QuoteLatencyMillis    uint32            `json:"p95_quote_latency_millis,omitempty"`
	FailureCounts            map[string]uint64 `json:"failure_counts"`
}

// ProvisionalArtifact is a short, current paper-testing checkpoint. It is
// deliberately a different type from Artifact: six hours can expose broken
// sources, rate limits, and unusable routes, but it cannot establish long-run
// reliability or authorize an executable proposal.
type ProvisionalArtifact struct {
	Version               uint32                `json:"version"`
	Status                string                `json:"status"`
	PaperOnly             bool                  `json:"paper_only"`
	Authorized            bool                  `json:"authorized"`
	ProvisionalPaperReady bool                  `json:"provisional_paper_ready"`
	WindowHours           uint16                `json:"window_hours"`
	Candidate             Candidate             `json:"candidate"`
	CandidateSHA256       string                `json:"candidate_sha256"`
	Observe               string                `json:"observe"`
	OpeningSHA256         string                `json:"opening_sha256"`
	Thresholds            Thresholds            `json:"thresholds"`
	From                  time.Time             `json:"from"`
	Through               time.Time             `json:"through"`
	Journal               journal.DurablePrefix `json:"journal"`
	ExpectedBuckets       uint64                `json:"expected_buckets"`
	ObservedBuckets       uint64                `json:"observed_buckets"`
	AvailableBuckets      uint64                `json:"available_buckets"`
	AvailabilityBPS       uint16                `json:"availability_bps"`
	MedianRouteCostBPS    uint16                `json:"median_route_cost_bps,omitempty"`
	P95RouteCostBPS       uint16                `json:"p95_route_cost_bps,omitempty"`
	Reasons               []string              `json:"reasons,omitempty"`
	ContentSHA256         string                `json:"content_sha256"`
}

// EvaluateProvisionalJournal derives the most recent six complete hours from
// one exact durable prefix. It never creates a long-run admission artifact.
func EvaluateProvisionalJournal(
	path string,
	prefix journal.DurablePrefix,
	now time.Time,
) (ProvisionalArtifact, error) {
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		return ProvisionalArtifact{}, err
	}
	opening, observations, err := decodeRecords(records)
	if err != nil {
		return ProvisionalArtifact{}, err
	}
	if now.IsZero() {
		return ProvisionalArtifact{}, errors.New("provisional market evidence evaluation time is required")
	}
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	through := now.UTC().Truncate(cadence)
	return evaluateProvisional(
		opening, through.Add(-time.Duration(ProvisionalWindowHours)*time.Hour),
		through, prefix, observations,
	)
}

func evaluateProvisional(
	opening Opening,
	from, through time.Time,
	prefix journal.DurablePrefix,
	observations []Observation,
) (ProvisionalArtifact, error) {
	if err := opening.Validate(); err != nil {
		return ProvisionalArtifact{}, err
	}
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	from, through = from.UTC(), through.UTC()
	window := time.Duration(ProvisionalWindowHours) * time.Hour
	if from.IsZero() || from != from.Truncate(cadence) ||
		through != through.Truncate(cadence) || through.Sub(from) != window {
		return ProvisionalArtifact{}, errors.New("provisional market evidence must cover exactly six complete hours")
	}
	expected := uint64(window / cadence)
	selected := make([]Observation, 0, expected)
	for _, observation := range observations {
		if !observation.Bucket.Before(from) && observation.Bucket.Before(through) {
			selected = append(selected, observation)
		}
	}
	costs := make([]uint16, 0, len(selected))
	available := uint64(0)
	for _, observation := range selected {
		if cost, ok := usableObservation(opening, observation); ok {
			available++
			costs = append(costs, cost)
		}
	}
	artifact := ProvisionalArtifact{
		Version: Version, Status: ProvisionalStatus, PaperOnly: true,
		WindowHours: ProvisionalWindowHours, Candidate: opening.Candidate,
		CandidateSHA256: opening.CandidateSHA256, Observe: opening.Observe,
		OpeningSHA256: opening.ContentSHA256, Thresholds: opening.Thresholds,
		From: from, Through: through, Journal: prefix,
		ExpectedBuckets: expected, ObservedBuckets: uint64(len(selected)),
		AvailableBuckets: available, AvailabilityBPS: availabilityBPS(available, expected),
	}
	if len(costs) != 0 {
		sort.Slice(costs, func(left, right int) bool { return costs[left] < costs[right] })
		artifact.MedianRouteCostBPS = percentile(costs, 50)
		artifact.P95RouteCostBPS = percentile(costs, 95)
	}
	artifact.Reasons = provisionalReasons(artifact)
	artifact.ProvisionalPaperReady = len(artifact.Reasons) == 0
	contentSHA256, err := provisionalFingerprint(artifact)
	if err != nil {
		return ProvisionalArtifact{}, err
	}
	artifact.ContentSHA256 = contentSHA256
	return artifact, nil
}

func provisionalReasons(artifact ProvisionalArtifact) []string {
	var reasons []string
	if artifact.AvailabilityBPS < ProvisionalMinimumAvailabilityBPS {
		reasons = append(reasons, "six-hour bidirectional availability is below the paper-testing minimum")
	}
	if artifact.AvailableBuckets == 0 {
		reasons = append(reasons, "no complete bidirectional quote evidence is available")
	} else {
		if artifact.MedianRouteCostBPS > artifact.Thresholds.MedianRouteCostBPS {
			reasons = append(reasons, "median round-trip route cost exceeds the limit")
		}
		if artifact.P95RouteCostBPS > artifact.Thresholds.P95RouteCostBPS {
			reasons = append(reasons, "p95 round-trip route cost exceeds the limit")
		}
	}
	return reasons
}

func (artifact ProvisionalArtifact) Validate() error {
	cadence := time.Duration(DefaultThresholds().CadenceSeconds) * time.Second
	window := time.Duration(ProvisionalWindowHours) * time.Hour
	if artifact.Version != Version || artifact.Status != ProvisionalStatus ||
		!artifact.PaperOnly || artifact.Authorized || artifact.ContentSHA256 == "" ||
		artifact.WindowHours != ProvisionalWindowHours ||
		artifact.ProvisionalPaperReady != (len(artifact.Reasons) == 0) ||
		artifact.From != artifact.From.UTC().Truncate(cadence) ||
		artifact.Through != artifact.Through.UTC().Truncate(cadence) ||
		artifact.Through.Sub(artifact.From) != window {
		return errors.New("provisional market evidence artifact is invalid")
	}
	opening, err := NewOpening(artifact.Candidate, artifact.Observe, artifact.Thresholds)
	if err != nil || opening.CandidateSHA256 != artifact.CandidateSHA256 ||
		opening.ContentSHA256 != artifact.OpeningSHA256 ||
		artifact.Journal.Format != journal.Format || artifact.Journal.Bytes <= 0 ||
		artifact.Journal.Records <= 0 || !validDigest(artifact.Journal.ChainHeadSHA256) {
		return errors.New("provisional market evidence identity is invalid")
	}
	expected := uint64(window / cadence)
	if artifact.ExpectedBuckets != expected || artifact.ObservedBuckets > expected ||
		artifact.AvailableBuckets > artifact.ObservedBuckets ||
		artifact.AvailabilityBPS != availabilityBPS(artifact.AvailableBuckets, expected) ||
		!equalStrings(artifact.Reasons, provisionalReasons(artifact)) {
		return errors.New("provisional market evidence counters are invalid")
	}
	want, err := provisionalFingerprint(artifact)
	if err != nil || want != artifact.ContentSHA256 {
		return errors.New("provisional market evidence digest does not match")
	}
	return nil
}

// Current reports whether a paper-only artifact is still usable at startup.
// It expires after two collection cadences and never crosses a UTC day.
func (artifact ProvisionalArtifact) Current(now time.Time) bool {
	if artifact.Validate() != nil || now.IsZero() {
		return false
	}
	now = now.UTC()
	cadence := time.Duration(artifact.Thresholds.CadenceSeconds) * time.Second
	return !now.Before(artifact.Through) && now.Sub(artifact.Through) <= 2*cadence &&
		artifact.Through.Truncate(24*time.Hour).Equal(now.Truncate(24*time.Hour))
}

func (artifact ProvisionalArtifact) VerifyJournal(path string) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	records, err := journal.ReadDurablePrefix(path, artifact.Journal)
	if err != nil {
		return err
	}
	opening, observations, err := decodeRecords(records)
	if err != nil {
		return err
	}
	want, err := evaluateProvisional(
		opening, artifact.From, artifact.Through, artifact.Journal, observations,
	)
	if err != nil || want.ContentSHA256 != artifact.ContentSHA256 {
		return errors.New("provisional market evidence artifact does not match its journal")
	}
	return nil
}

// DiagnoseJournal derives a recent read-only operational summary from one
// exact durable prefix. It cannot produce evidence that policy loading accepts.
func DiagnoseJournal(
	path string,
	prefix journal.DurablePrefix,
	now time.Time,
	window time.Duration,
) (Diagnostic, error) {
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		return Diagnostic{}, err
	}
	opening, observations, err := decodeRecords(records)
	if err != nil {
		return Diagnostic{}, err
	}
	return diagnose(opening, observations, now, window)
}

func diagnose(
	opening Opening,
	observations []Observation,
	now time.Time,
	window time.Duration,
) (Diagnostic, error) {
	if err := opening.Validate(); err != nil {
		return Diagnostic{}, err
	}
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	if now.IsZero() || window < time.Hour || window > 7*24*time.Hour ||
		window%time.Hour != 0 || window%cadence != 0 {
		return Diagnostic{}, errors.New("market diagnostic window must be 1 to 168 whole hours")
	}
	through := now.UTC().Truncate(cadence)
	from := through.Add(-window)
	expected := uint64(window / cadence)
	diagnostic := Diagnostic{
		Version: Version, Market: opening.Candidate.Market,
		From: from, Through: through, DiagnosticOnly: true,
		ExpectedBuckets: expected, FailureCounts: make(map[string]uint64),
	}
	costs := make([]uint16, 0, len(observations))
	latencies := make([]uint32, 0, len(observations))
	for _, observation := range observations {
		if observation.Bucket.Before(from) || !observation.Bucket.Before(through) {
			continue
		}
		diagnostic.ObservedBuckets++
		cost, usable := usableObservation(opening, observation)
		if !usable {
			reason := observation.Failure
			if reason == "" {
				reason = "evidence_rejected"
			}
			diagnostic.FailureCounts[reason]++
			continue
		}
		diagnostic.AvailableBuckets++
		costs = append(costs, cost)
		latencies = append(latencies, observation.Buy.LatencyMillis+observation.Sell.LatencyMillis)
	}
	if missing := expected - min(expected, diagnostic.ObservedBuckets); missing != 0 {
		diagnostic.FailureCounts["missing_bucket"] = missing
	}
	diagnostic.AvailabilityBPS = availabilityBPS(diagnostic.AvailableBuckets, expected)
	if len(costs) != 0 {
		sort.Slice(costs, func(left, right int) bool { return costs[left] < costs[right] })
		sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
		diagnostic.MedianRouteCostBPS = percentile(costs, 50)
		diagnostic.P95RouteCostBPS = percentile(costs, 95)
		diagnostic.MedianQuoteLatencyMillis = percentileUint32(latencies, 50)
		diagnostic.P95QuoteLatencyMillis = percentileUint32(latencies, 95)
	}
	return diagnostic, nil
}

func percentileUint32(values []uint32, wanted uint64) uint32 {
	if len(values) == 0 {
		return 0
	}
	index := (uint64(len(values))*wanted + 99) / 100
	if index == 0 {
		index = 1
	}
	return values[index-1]
}

// EvaluateJournal derives the latest complete UTC window from an exact durable
// journal prefix; callers cannot supply a separate observation slice.
func EvaluateJournal(
	path string,
	prefix journal.DurablePrefix,
	now time.Time,
) (Artifact, error) {
	records, err := journal.ReadDurablePrefix(path, prefix)
	if err != nil {
		return Artifact{}, err
	}
	opening, observations, err := decodeRecords(records)
	if err != nil {
		return Artifact{}, err
	}
	now = now.UTC()
	if now.IsZero() {
		return Artifact{}, errors.New("market evidence evaluation time is required")
	}
	through := now.Truncate(24 * time.Hour)
	from := through.Add(-time.Duration(opening.Thresholds.MinimumDays) * 24 * time.Hour)
	return evaluate(opening, from, through, prefix, observations)
}

// VerifyJournal recomputes an artifact from its bound durable prefix.
func (artifact Artifact) VerifyJournal(path string) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	records, err := journal.ReadDurablePrefix(path, artifact.Journal)
	if err != nil {
		return err
	}
	opening, observations, err := decodeRecords(records)
	if err != nil {
		return err
	}
	want, err := evaluate(opening, artifact.From, artifact.Through, artifact.Journal, observations)
	if err != nil || want.ContentSHA256 != artifact.ContentSHA256 {
		return errors.New("market evidence artifact does not match its journal")
	}
	return nil
}

func decodeRecords(records []journal.Record) (Opening, []Observation, error) {
	if len(records) == 0 || records[0].Type != EventOpened {
		return Opening{}, nil, errors.New("market evidence journal has no opening record")
	}
	var opening Opening
	if err := strictjson.Decode(records[0].Payload, &opening); err != nil || opening.Validate() != nil ||
		records[0].ActionID != opening.ContentSHA256 {
		return Opening{}, nil, errors.New("market evidence opening record is invalid")
	}
	observations := make([]Observation, 0, len(records)-1)
	seen := make(map[int64]struct{}, len(records)-1)
	for _, record := range records[1:] {
		if record.Type == journal.EventRotated {
			continue
		}
		if record.Type != EventObserved {
			return Opening{}, nil, errors.New("market evidence journal contains an unsupported event")
		}
		var observation Observation
		if err := strictjson.Decode(record.Payload, &observation); err != nil ||
			observation.Validate(opening) != nil ||
			!record.At.UTC().Equal(observation.ObservedAt.UTC()) ||
			record.ActionID != observation.Bucket.UTC().Format(time.RFC3339) {
			return Opening{}, nil, errors.New("market evidence observation record is invalid")
		}
		bucket := observation.Bucket.Unix()
		if _, duplicate := seen[bucket]; duplicate {
			return Opening{}, nil, errors.New("market evidence observation bucket is duplicated")
		}
		seen[bucket] = struct{}{}
		observations = append(observations, observation)
	}
	return opening, observations, nil
}

// ValidateResume refuses a journal created for another candidate, observer, or
// threshold contract and returns its latest scheduled bucket.
func ValidateResume(records []journal.Record, opening Opening) (time.Time, error) {
	stored, observations, err := decodeRecords(records)
	if err != nil {
		return time.Time{}, err
	}
	if stored.ContentSHA256 != opening.ContentSHA256 {
		return time.Time{}, errors.New("market evidence journal belongs to another opening")
	}
	var last time.Time
	for _, observation := range observations {
		if observation.Bucket.After(last) {
			last = observation.Bucket.UTC()
		}
	}
	return last, nil
}

func evaluate(
	opening Opening,
	from, through time.Time,
	prefix journal.DurablePrefix,
	observations []Observation,
) (Artifact, error) {
	from, through = from.UTC(), through.UTC()
	if from.IsZero() || !through.After(from) ||
		from != from.Truncate(24*time.Hour) || through != through.Truncate(24*time.Hour) ||
		through.Sub(from) != time.Duration(opening.Thresholds.MinimumDays)*24*time.Hour {
		return Artifact{}, errors.New("market evidence window must be the exact complete UTC period")
	}
	cadence := time.Duration(opening.Thresholds.CadenceSeconds) * time.Second
	expected := uint64(through.Sub(from) / cadence)
	selected := make([]Observation, 0, len(observations))
	for _, observation := range observations {
		if !observation.Bucket.Before(from) && observation.Bucket.Before(through) {
			selected = append(selected, observation)
		}
	}
	available := uint64(0)
	costs := make([]uint16, 0, len(selected))
	for _, observation := range selected {
		cost, ok := usableObservation(opening, observation)
		if ok {
			available++
			costs = append(costs, cost)
		}
	}
	artifact := Artifact{
		Version: Version, Candidate: opening.Candidate,
		CandidateSHA256: opening.CandidateSHA256, Observe: opening.Observe,
		OpeningSHA256: opening.ContentSHA256, Thresholds: opening.Thresholds,
		From: from, Through: through, Journal: prefix,
		ExpectedBuckets: expected, ObservedBuckets: uint64(len(selected)),
		AvailableBuckets: available, AvailabilityBPS: availabilityBPS(available, expected),
	}
	if len(costs) != 0 {
		sort.Slice(costs, func(left, right int) bool { return costs[left] < costs[right] })
		artifact.MedianRouteCostBPS = percentile(costs, 50)
		artifact.P95RouteCostBPS = percentile(costs, 95)
	}
	artifact.Reasons = qualificationReasons(artifact)
	artifact.OperationallyQualified = len(artifact.Reasons) == 0
	var err error
	artifact.ContentSHA256, err = artifactFingerprint(artifact)
	if err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func usableObservation(opening Opening, observation Observation) (uint16, bool) {
	thresholds := opening.Thresholds
	cadence := time.Duration(thresholds.CadenceSeconds) * time.Second
	if observation.Failure != "" || !observation.ObservedAt.Before(observation.Bucket.Add(cadence)) ||
		observation.Mint.Validate(opening.Candidate) != nil {
		return 0, false
	}
	marketPrimary, err := opening.Candidate.Pyth.IdentitySHA256()
	if err != nil || !validPythObservation(
		observation.MarketPrimary, opening.Candidate.Pyth, marketPrimary,
	) {
		return 0, false
	}
	marketPolicy := observationPolicy(
		opening.Candidate.Pyth.Feed, marketPrimary,
		mustIdentity(opening.Candidate.Kraken.IdentitySHA256()), thresholds,
	)
	if pricetrigger.ValidateObservation(
		marketPolicy, observation.MarketPrimary.Sample, observation.MarketSecondary,
		observation.ObservedAt,
	) != nil {
		return 0, false
	}
	usdcSpec := pricesource.PythPushUSDCSpec()
	if !validPythObservation(
		observation.USDCPrimary, usdcSpec, pricesource.PythPushUSDCIdentitySHA256(),
	) {
		return 0, false
	}
	peg := pricetrigger.BandPolicy{
		Version: pricetrigger.Version, Feed: pricetrigger.FeedUSDCUSD,
		MinimumMicros:         pricetrigger.USDCBandMinimumMicros,
		MaximumMicros:         pricetrigger.USDCBandMaximumMicros,
		MaxAgeSeconds:         uint64(thresholds.MaximumSourceAgeSeconds),
		MaxSourceSkewSeconds:  uint64(thresholds.MaximumSourceSkewSeconds),
		MaxDeviationBPS:       thresholds.MaximumSourceDeviationBPS,
		MaxConfidenceBPS:      thresholds.MaximumConfidenceBPS,
		PrimarySourceSHA256:   pricesource.PythPushUSDCIdentitySHA256(),
		SecondarySourceSHA256: pricesource.KrakenIdentitySHA256(),
	}
	pegEvidence, err := pricetrigger.EvaluateBand(
		peg, observation.USDCPrimary.Sample, observation.USDCSecondary, observation.ObservedAt,
	)
	if err != nil || !pegEvidence.InBand {
		return 0, false
	}
	solSpec := pricesource.PythPushSOLSpec()
	if !validPythObservation(
		observation.SOLPrimary, solSpec, pricesource.PythPushIdentitySHA256(),
	) || pricetrigger.ValidateObservation(
		observationPolicy(
			pricetrigger.FeedSOLUSD, pricesource.PythPushIdentitySHA256(),
			pricesource.KrakenSOLIdentitySHA256(), thresholds,
		),
		observation.SOLPrimary.Sample, observation.SOLSecondary, observation.ObservedAt,
	) != nil {
		return 0, false
	}
	if !validQuote(opening, observation.Buy, false, observation, thresholds) ||
		!validQuote(opening, observation.Sell, true, observation, thresholds) ||
		observation.Sell.InputAmount != observation.Buy.EstimatedOutput ||
		!quotePricesUsable(opening.Candidate, observation, thresholds.MaximumQuoteImpactBPS) {
		return 0, false
	}
	return routeCostBPS(opening.Candidate.QuoteNotionalUSDC, observation.Sell.EstimatedOutput), true
}

func quotePricesUsable(candidate Candidate, observation Observation, maximumBPS uint16) bool {
	for _, check := range []struct {
		quote   Quote
		sell    bool
		minimum bool
	}{
		{observation.Buy, false, false},
		{observation.Buy, false, true},
		{observation.Sell, true, false},
		{observation.Sell, true, true},
	} {
		quoted, ok := quotedPriceMicros(candidate, check.quote, check.sell, check.minimum)
		if !ok || !withinBPS(
			quoted, observation.MarketPrimary.Sample.PriceMicros, maximumBPS,
		) || !withinBPS(quoted, observation.MarketSecondary.PriceMicros, maximumBPS) {
			return false
		}
	}
	return true
}

func quotedPriceMicros(candidate Candidate, quote Quote, sell, minimum bool) (uint64, bool) {
	baseAmount, quoteAmount := quote.EstimatedOutput, quote.InputAmount
	if sell {
		baseAmount, quoteAmount = quote.InputAmount, quote.EstimatedOutput
	}
	if minimum {
		if sell {
			quoteAmount = quote.MinimumOutput
		} else {
			baseAmount = quote.MinimumOutput
		}
	}
	if baseAmount == 0 {
		return 0, false
	}
	scale := uint64(1)
	for range candidate.BaseDecimals {
		scale *= 10
	}
	high, low := bits.Mul64(quoteAmount, scale)
	if high >= baseAmount {
		return 0, false
	}
	value, _ := bits.Div64(high, low, baseAmount)
	return value, value != 0
}

func withinBPS(value, reference uint64, maximum uint16) bool {
	if value == 0 || reference == 0 {
		return false
	}
	difference := value - reference
	if value < reference {
		difference = reference - value
	}
	leftHigh, leftLow := bits.Mul64(difference, 10_000)
	rightHigh, rightLow := bits.Mul64(reference, uint64(maximum))
	return leftHigh < rightHigh || leftHigh == rightHigh && leftLow <= rightLow
}

func observationPolicy(
	feed, primary, secondary string,
	thresholds Thresholds,
) pricetrigger.ObservationPolicy {
	return pricetrigger.ObservationPolicy{
		Feed: feed, MaxAgeSeconds: uint64(thresholds.MaximumSourceAgeSeconds),
		MaxSourceSkewSeconds: uint64(thresholds.MaximumSourceSkewSeconds),
		MaxDeviationBPS:      thresholds.MaximumSourceDeviationBPS,
		MaxConfidenceBPS:     thresholds.MaximumConfidenceBPS,
		PrimarySourceSHA256:  primary, SecondarySourceSHA256: secondary,
	}
}

func validPythObservation(
	observation pricesource.PythObservation,
	spec pricesource.PythPushSpec,
	identity string,
) bool {
	return observation.Sample.SourceSHA256 == identity &&
		observation.Sample.Feed == spec.Feed && observation.ContextSlot != 0 &&
		observation.FeedID == spec.FeedID &&
		(observation.Account == spec.LegacyAccount || observation.Account == spec.UpgradedAccount)
}

func validQuote(
	opening Opening,
	quote Quote,
	sell bool,
	observation Observation,
	thresholds Thresholds,
) bool {
	candidate := opening.Candidate
	input, output := candidate.QuoteMint, candidate.BaseMint
	wantAmount := candidate.QuoteNotionalUSDC
	if sell {
		input, output = candidate.BaseMint, candidate.QuoteMint
		wantAmount = observation.Buy.EstimatedOutput
	}
	request := jupiterquote.Request{
		Taker: opening.Observe, InputMint: input,
		OutputMint: output, InputAmount: wantAmount,
		SlippageBPS: candidate.QuoteSlippageBPS,
	}
	result := jupiterquote.Result{
		InputAmount: quote.InputAmount, EstimatedOutput: quote.EstimatedOutput,
		MinimumOutput: quote.MinimumOutput,
	}
	return quote.InputMint == input && quote.OutputMint == output &&
		result.Validate(request) == nil &&
		!quote.ReceivedAt.Before(observation.Bucket) &&
		!quote.ReceivedAt.After(observation.ObservedAt) &&
		quote.LatencyMillis <= thresholds.MaximumQuoteLatencyMillis &&
		validDigest(quote.ResponseSHA256)
}

func qualificationReasons(artifact Artifact) []string {
	var reasons []string
	if artifact.AvailabilityBPS < artifact.Thresholds.MinimumAvailabilityBPS {
		reasons = append(reasons, "bidirectional evidence availability is below the minimum")
	}
	if artifact.AvailableBuckets == 0 {
		reasons = append(reasons, "no complete bidirectional quote evidence is available")
	} else {
		if artifact.MedianRouteCostBPS > artifact.Thresholds.MedianRouteCostBPS {
			reasons = append(reasons, "median round-trip route cost exceeds the limit")
		}
		if artifact.P95RouteCostBPS > artifact.Thresholds.P95RouteCostBPS {
			reasons = append(reasons, "p95 round-trip route cost exceeds the limit")
		}
	}
	return reasons
}

func (artifact Artifact) Validate() error {
	if artifact.Version != Version || artifact.ContentSHA256 == "" ||
		artifact.OperationallyQualified != (len(artifact.Reasons) == 0) ||
		artifact.From != artifact.From.UTC().Truncate(24*time.Hour) ||
		artifact.Through != artifact.Through.UTC().Truncate(24*time.Hour) ||
		artifact.Through.Sub(artifact.From) !=
			time.Duration(DefaultThresholds().MinimumDays)*24*time.Hour {
		return errors.New("market evidence artifact is invalid")
	}
	opening, err := NewOpening(artifact.Candidate, artifact.Observe, artifact.Thresholds)
	if err != nil || opening.CandidateSHA256 != artifact.CandidateSHA256 ||
		opening.ContentSHA256 != artifact.OpeningSHA256 ||
		artifact.Journal.Format != journal.Format || artifact.Journal.Bytes <= 0 ||
		artifact.Journal.Records <= 0 || !validDigest(artifact.Journal.ChainHeadSHA256) {
		return errors.New("market evidence artifact identity is invalid")
	}
	expected := uint64(artifact.Through.Sub(artifact.From) /
		(time.Duration(artifact.Thresholds.CadenceSeconds) * time.Second))
	if artifact.ExpectedBuckets != expected || artifact.ExpectedBuckets == 0 ||
		artifact.ObservedBuckets > expected || artifact.AvailableBuckets > artifact.ObservedBuckets ||
		artifact.AvailabilityBPS != availabilityBPS(artifact.AvailableBuckets, expected) ||
		!equalStrings(artifact.Reasons, qualificationReasons(artifact)) {
		return errors.New("market evidence artifact counters are invalid")
	}
	want, err := artifactFingerprint(artifact)
	if err != nil || want != artifact.ContentSHA256 {
		return errors.New("market evidence artifact digest does not match")
	}
	return nil
}

func availabilityBPS(available, expected uint64) uint16 {
	if expected == 0 {
		return 0
	}
	high, low := bits.Mul64(available, 10_000)
	if high != 0 {
		return 0
	}
	return uint16(low / expected)
}

func routeCostBPS(input, output uint64) uint16 {
	if output >= input {
		return 0
	}
	high, low := bits.Mul64(input-output, 10_000)
	value, remainder := bits.Div64(high, low, input)
	if remainder != 0 {
		value++
	}
	if value > 10_000 {
		value = 10_000
	}
	return uint16(value)
}

func percentile(values []uint16, percent int) uint16 {
	index := (len(values)*percent + 99) / 100
	if index == 0 {
		index = 1
	}
	return values[index-1]
}

func validFailure(value string) bool {
	switch value {
	case FailureMintState, FailureMarketPrice, FailureQuotePeg, FailureNativePrice,
		FailureBuyQuote, FailureSellQuote:
		return true
	default:
		return false
	}
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mustIdentity(value string, err error) string {
	if err != nil {
		return ""
	}
	return value
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func openingFingerprint(opening Opening) (string, error) {
	opening.ContentSHA256 = ""
	return digest(opening)
}

func artifactFingerprint(artifact Artifact) (string, error) {
	artifact.ContentSHA256 = ""
	return digest(artifact)
}

func provisionalFingerprint(artifact ProvisionalArtifact) (string, error) {
	artifact.ContentSHA256 = ""
	return digest(artifact)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
