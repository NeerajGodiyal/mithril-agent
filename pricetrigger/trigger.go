// Package pricetrigger evaluates bounded market-price conditions using two
// independently identified sources and integer arithmetic.
package pricetrigger

import (
	"encoding/hex"
	"errors"
	"math/bits"
	"strings"
	"time"
)

var (
	ErrSourceTimestampSkew = errors.New("price evidence timestamps disagree")
	ErrSourceDeviation     = errors.New("price evidence exceeds the deviation limit")
)

const (
	Version               = uint32(1)
	MultiFeedVersion      = uint32(2)
	AdmittedFeedVersion   = uint32(3)
	FeedSOLUSD            = "SOL/USD"
	FeedJUPUSD            = "JUP/USD"
	FeedUSDCUSD           = "USDC/USD"
	MaxPriceMicros        = uint64(1_000_000_000_000)
	USDCBandMinimumMicros = uint64(990_000)
	USDCBandMaximumMicros = uint64(1_010_000)
)

type Direction string

const (
	SellAtOrAbove Direction = "sell_at_or_above"
	BuyAtOrBelow  Direction = "buy_at_or_below"
)

type Policy struct {
	Version               uint32    `json:"version"`
	Feed                  string    `json:"feed"`
	Direction             Direction `json:"direction"`
	ThresholdMicros       uint64    `json:"threshold_micros"`
	MaxAgeSeconds         uint64    `json:"max_age_seconds"`
	MaxSourceSkewSeconds  uint64    `json:"max_source_skew_seconds"`
	MaxDeviationBPS       uint16    `json:"max_deviation_bps"`
	MaxConfidenceBPS      uint16    `json:"max_confidence_bps"`
	PrimarySourceSHA256   string    `json:"primary_source_sha256"`
	SecondarySourceSHA256 string    `json:"secondary_source_sha256"`
}

func (p Policy) Validate() error {
	if p.Version == Version && p.Feed != FeedSOLUSD {
		return errors.New("price trigger v1 must use SOL/USD")
	}
	if p.Version == MultiFeedVersion && p.Feed != FeedSOLUSD && p.Feed != FeedJUPUSD {
		return errors.New("price trigger v2 feed is unsupported")
	}
	if p.Version == AdmittedFeedVersion && !ValidUSDFeed(p.Feed) {
		return errors.New("admitted price trigger feed is unsupported")
	}
	if p.Version != Version && p.Version != MultiFeedVersion && p.Version != AdmittedFeedVersion {
		return errors.New("price trigger version is unsupported")
	}
	if p.Direction != SellAtOrAbove && p.Direction != BuyAtOrBelow {
		return errors.New("price trigger direction is invalid")
	}
	if p.ThresholdMicros == 0 || p.ThresholdMicros > MaxPriceMicros {
		return errors.New("price trigger threshold is outside policy")
	}
	if p.MaxAgeSeconds == 0 || p.MaxAgeSeconds > 120 ||
		p.MaxSourceSkewSeconds > p.MaxAgeSeconds {
		return errors.New("price trigger freshness limits are invalid")
	}
	if p.MaxDeviationBPS == 0 || p.MaxDeviationBPS > 500 ||
		p.MaxConfidenceBPS == 0 || p.MaxConfidenceBPS > 500 {
		return errors.New("price trigger evidence limits are invalid")
	}
	if !validDigest(p.PrimarySourceSHA256) ||
		!validDigest(p.SecondarySourceSHA256) ||
		p.PrimarySourceSHA256 == p.SecondarySourceSHA256 {
		return errors.New("price trigger sources must have distinct bound identities")
	}
	return nil
}

// ValidUSDFeed accepts the narrow canonical symbol form used by allowlisted
// candidate markets. A ticker remains display metadata; admission pins the
// exact mint, decimals, authorities, and source accounts.
func ValidUSDFeed(feed string) bool {
	base, ok := strings.CutSuffix(feed, "/USD")
	if !ok || len(base) < 2 || len(base) > 12 {
		return false
	}
	for _, character := range base {
		if character < 'A' || character > 'Z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

// ObservationPolicy validates a canonical USD feed without widening the
// funded trigger-policy versions accepted by existing runners.
type ObservationPolicy struct {
	Feed                  string
	MaxAgeSeconds         uint64
	MaxSourceSkewSeconds  uint64
	MaxDeviationBPS       uint16
	MaxConfidenceBPS      uint16
	PrimarySourceSHA256   string
	SecondarySourceSHA256 string
}

func (policy ObservationPolicy) Validate() error {
	if !ValidUSDFeed(policy.Feed) || policy.MaxAgeSeconds == 0 ||
		policy.MaxAgeSeconds > 120 || policy.MaxSourceSkewSeconds > policy.MaxAgeSeconds ||
		policy.MaxDeviationBPS == 0 || policy.MaxDeviationBPS > 500 ||
		policy.MaxConfidenceBPS == 0 || policy.MaxConfidenceBPS > 500 ||
		!validDigest(policy.PrimarySourceSHA256) ||
		!validDigest(policy.SecondarySourceSHA256) ||
		policy.PrimarySourceSHA256 == policy.SecondarySourceSHA256 {
		return errors.New("price observation policy is invalid")
	}
	return nil
}

// ValidateObservation applies two-source freshness, confidence, skew, and
// deviation checks without evaluating a buy or sell threshold.
func ValidateObservation(
	policy ObservationPolicy,
	primary, secondary Sample,
	now time.Time,
) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	now = now.UTC()
	if now.IsZero() {
		return errors.New("price observation time is required")
	}
	return validatePair(
		policy.Feed, policy.MaxAgeSeconds, policy.MaxSourceSkewSeconds,
		policy.MaxDeviationBPS, policy.MaxConfidenceBPS,
		policy.PrimarySourceSHA256, policy.SecondarySourceSHA256,
		primary, secondary, now,
	)
}

type Sample struct {
	SourceSHA256     string    `json:"source_sha256"`
	Feed             string    `json:"feed"`
	PriceMicros      uint64    `json:"price_micros"`
	ConfidenceMicros uint64    `json:"confidence_micros"`
	PublishedAt      time.Time `json:"published_at"`
}

type Evidence struct {
	Primary           Sample    `json:"primary"`
	Secondary         Sample    `json:"secondary"`
	ConservativePrice uint64    `json:"conservative_price_micros"`
	Triggered         bool      `json:"triggered"`
	ObservedAt        time.Time `json:"observed_at"`
}

// BandPolicy bounds a non-triggering market invariant with the same two-source
// freshness and identity checks used by a price trigger. The first use is the
// USDC/USD guard: shadow accounting may treat USDC as a dollar only while both
// independent confidence intervals remain inside this band.
type BandPolicy struct {
	Version               uint32 `json:"version"`
	Feed                  string `json:"feed"`
	MinimumMicros         uint64 `json:"minimum_micros"`
	MaximumMicros         uint64 `json:"maximum_micros"`
	MaxAgeSeconds         uint64 `json:"max_age_seconds"`
	MaxSourceSkewSeconds  uint64 `json:"max_source_skew_seconds"`
	MaxDeviationBPS       uint16 `json:"max_deviation_bps"`
	MaxConfidenceBPS      uint16 `json:"max_confidence_bps"`
	PrimarySourceSHA256   string `json:"primary_source_sha256"`
	SecondarySourceSHA256 string `json:"secondary_source_sha256"`
}

func (p BandPolicy) Validate() error {
	if p.Version != Version || p.Feed != FeedUSDCUSD {
		return errors.New("price band must use the USDC/USD v1 contract")
	}
	// This contract is a stablecoin guard, not a general-purpose range oracle.
	// Keeping the allowed policy itself close to one dollar prevents a typo from
	// turning a depeg check into an always-open gate.
	if p.MinimumMicros != USDCBandMinimumMicros ||
		p.MaximumMicros != USDCBandMaximumMicros {
		return errors.New("USDC/USD price band must be $0.99-$1.01")
	}
	if p.MaxAgeSeconds == 0 || p.MaxAgeSeconds > 120 ||
		p.MaxSourceSkewSeconds > p.MaxAgeSeconds {
		return errors.New("price band freshness limits are invalid")
	}
	if p.MaxDeviationBPS == 0 || p.MaxDeviationBPS > 500 ||
		p.MaxConfidenceBPS == 0 || p.MaxConfidenceBPS > 500 {
		return errors.New("price band evidence limits are invalid")
	}
	if !validDigest(p.PrimarySourceSHA256) ||
		!validDigest(p.SecondarySourceSHA256) ||
		p.PrimarySourceSHA256 == p.SecondarySourceSHA256 {
		return errors.New("price band sources must have distinct bound identities")
	}
	return nil
}

type BandEvidence struct {
	Primary     Sample    `json:"primary"`
	Secondary   Sample    `json:"secondary"`
	LowerMicros uint64    `json:"lower_micros"`
	UpperMicros uint64    `json:"upper_micros"`
	InBand      bool      `json:"in_band"`
	ObservedAt  time.Time `json:"observed_at"`
}

// EvaluateBand returns the widest confidence-adjusted interval supported by
// both sources. A caller must require InBand; an out-of-band observation is a
// real risk halt, not permission to silently keep assuming one dollar.
func EvaluateBand(
	policy BandPolicy,
	primary, secondary Sample,
	now time.Time,
) (BandEvidence, error) {
	if err := policy.Validate(); err != nil {
		return BandEvidence{}, err
	}
	now = now.UTC()
	if now.IsZero() {
		return BandEvidence{}, errors.New("price band observation time is required")
	}
	if err := validatePair(
		policy.Feed, policy.MaxAgeSeconds, policy.MaxSourceSkewSeconds,
		policy.MaxDeviationBPS, policy.MaxConfidenceBPS,
		policy.PrimarySourceSHA256, policy.SecondarySourceSHA256,
		primary, secondary, now,
	); err != nil {
		return BandEvidence{}, err
	}
	lower := min(
		primary.PriceMicros-primary.ConfidenceMicros,
		secondary.PriceMicros-secondary.ConfidenceMicros,
	)
	upper := max(
		primary.PriceMicros+primary.ConfidenceMicros,
		secondary.PriceMicros+secondary.ConfidenceMicros,
	)
	return BandEvidence{
		Primary: primary, Secondary: secondary,
		LowerMicros: lower, UpperMicros: upper,
		InBand:     lower >= policy.MinimumMicros && upper <= policy.MaximumMicros,
		ObservedAt: now,
	}, nil
}

// Status is the bounded, non-secret price-rule projection exposed to operators.
// Source identities and provider errors intentionally stay out of this type.
type Status struct {
	Feed              string    `json:"feed"`
	Direction         Direction `json:"direction"`
	ThresholdMicros   uint64    `json:"threshold_micros"`
	Available         bool      `json:"available"`
	ConservativePrice uint64    `json:"conservative_price_micros,omitempty"`
	ConditionMet      bool      `json:"condition_met,omitempty"`
	// ExecutableMinimum is the worst route price implied by its minimum output.
	// It is USD only when the quote asset has independent dollar evidence.
	ExecutableMinimum    uint64    `json:"executable_minimum_micros,omitempty"`
	ExecutableCondition  bool      `json:"executable_condition_met,omitempty"`
	ObservedAt           time.Time `json:"observed_at,omitzero"`
	PrimaryPublishedAt   time.Time `json:"primary_published_at,omitzero"`
	SecondaryPublishedAt time.Time `json:"secondary_published_at,omitzero"`
}

func Project(policy Policy, evidence Evidence) (Status, error) {
	if err := validateStoredEvidence(policy, evidence); err != nil {
		return Status{}, err
	}
	return Status{
		Feed: policy.Feed, Direction: policy.Direction,
		ThresholdMicros: policy.ThresholdMicros, Available: true,
		ConservativePrice: evidence.ConservativePrice,
		ConditionMet:      evidence.Triggered, ObservedAt: evidence.ObservedAt.UTC(),
		PrimaryPublishedAt:   evidence.Primary.PublishedAt.UTC(),
		SecondaryPublishedAt: evidence.Secondary.PublishedAt.UTC(),
	}, nil
}

func Unavailable(policy Policy) Status {
	return Status{
		Feed: policy.Feed, Direction: policy.Direction,
		ThresholdMicros: policy.ThresholdMicros,
	}
}

func ValidateStatus(status Status) error {
	if status.Feed != FeedSOLUSD ||
		(status.Direction != SellAtOrAbove && status.Direction != BuyAtOrBelow) ||
		status.ThresholdMicros == 0 || status.ThresholdMicros > MaxPriceMicros {
		return errors.New("price trigger status policy is invalid")
	}
	if !status.Available {
		if status.ConservativePrice != 0 || status.ConditionMet ||
			status.ExecutableMinimum != 0 || status.ExecutableCondition ||
			!status.ObservedAt.IsZero() || !status.PrimaryPublishedAt.IsZero() ||
			!status.SecondaryPublishedAt.IsZero() {
			return errors.New("unavailable price trigger status has evidence")
		}
		return nil
	}
	if status.ConservativePrice == 0 || status.ConservativePrice > MaxPriceMicros ||
		status.ObservedAt.IsZero() || status.PrimaryPublishedAt.IsZero() ||
		status.SecondaryPublishedAt.IsZero() ||
		status.PrimaryPublishedAt.After(status.ObservedAt.Add(time.Second)) ||
		status.SecondaryPublishedAt.After(status.ObservedAt.Add(time.Second)) {
		return errors.New("price trigger status evidence is invalid")
	}
	wantMet := status.ConservativePrice >= status.ThresholdMicros
	if status.Direction == BuyAtOrBelow {
		wantMet = status.ConservativePrice <= status.ThresholdMicros
	}
	if status.ConditionMet != wantMet {
		return errors.New("price trigger status decision is invalid")
	}
	if status.ExecutableMinimum != 0 {
		wantExecutable := status.ExecutableMinimum >= status.ThresholdMicros
		if status.Direction == BuyAtOrBelow {
			wantExecutable = status.ExecutableMinimum <= status.ThresholdMicros
		}
		if status.ExecutableCondition != wantExecutable {
			return errors.New("price trigger executable decision is invalid")
		}
	} else if status.ExecutableCondition {
		return errors.New("price trigger executable decision has no price")
	}
	return nil
}

func Evaluate(policy Policy, primary, secondary Sample, now time.Time) (Evidence, error) {
	if err := policy.Validate(); err != nil {
		return Evidence{}, err
	}
	now = now.UTC()
	if now.IsZero() {
		return Evidence{}, errors.New("price trigger observation time is required")
	}
	if err := validatePair(
		policy.Feed, policy.MaxAgeSeconds, policy.MaxSourceSkewSeconds,
		policy.MaxDeviationBPS, policy.MaxConfidenceBPS,
		policy.PrimarySourceSHA256, policy.SecondarySourceSHA256,
		primary, secondary, now,
	); err != nil {
		return Evidence{}, err
	}
	evidence := Evidence{
		Primary: primary, Secondary: secondary, ObservedAt: now,
	}
	if policy.Direction == SellAtOrAbove {
		evidence.ConservativePrice = min(
			primary.PriceMicros-primary.ConfidenceMicros,
			secondary.PriceMicros-secondary.ConfidenceMicros,
		)
		evidence.Triggered = evidence.ConservativePrice >= policy.ThresholdMicros
	} else {
		evidence.ConservativePrice = max(
			primary.PriceMicros+primary.ConfidenceMicros,
			secondary.PriceMicros+secondary.ConfidenceMicros,
		)
		evidence.Triggered = evidence.ConservativePrice <= policy.ThresholdMicros
	}
	return evidence, nil
}

func validatePair(
	feed string,
	maxAgeSeconds, maxSourceSkewSeconds uint64,
	maxDeviationBPS, maxConfidenceBPS uint16,
	primarySource, secondarySource string,
	primary, secondary Sample,
	now time.Time,
) error {
	if err := validateSample(
		feed, maxAgeSeconds, maxConfidenceBPS, primary, primarySource, now,
	); err != nil {
		return errors.New("primary price evidence is invalid")
	}
	if err := validateSample(
		feed, maxAgeSeconds, maxConfidenceBPS, secondary, secondarySource, now,
	); err != nil {
		return errors.New("secondary price evidence is invalid")
	}
	skew := primary.PublishedAt.Sub(secondary.PublishedAt)
	if skew < 0 {
		skew = -skew
	}
	if skew > time.Duration(maxSourceSkewSeconds)*time.Second {
		return ErrSourceTimestampSkew
	}
	higher := max(primary.PriceMicros, secondary.PriceMicros)
	lower := min(primary.PriceMicros, secondary.PriceMicros)
	if !ratioWithinBPS(higher-lower, higher, maxDeviationBPS) {
		return ErrSourceDeviation
	}
	return nil
}

func validateSample(
	feed string,
	maxAgeSeconds uint64,
	maxConfidenceBPS uint16,
	sample Sample,
	source string,
	now time.Time,
) error {
	if sample.SourceSHA256 != source || sample.Feed != feed ||
		sample.PriceMicros == 0 || sample.PriceMicros > MaxPriceMicros ||
		sample.ConfidenceMicros >= sample.PriceMicros || sample.PublishedAt.IsZero() {
		return errors.New("price evidence identity is invalid")
	}
	publishedAt := sample.PublishedAt.UTC()
	if publishedAt.After(now.Add(time.Second)) ||
		now.Sub(publishedAt) > time.Duration(maxAgeSeconds)*time.Second {
		return errors.New("price evidence is stale")
	}
	if !ratioWithinBPS(
		sample.ConfidenceMicros,
		sample.PriceMicros,
		maxConfidenceBPS,
	) {
		return errors.New("price confidence exceeds policy")
	}
	return nil
}

func validateStoredEvidence(policy Policy, evidence Evidence) error {
	evaluated, err := Evaluate(policy, evidence.Primary, evidence.Secondary, evidence.ObservedAt)
	if err != nil || evidence.Triggered != evaluated.Triggered ||
		evidence.ConservativePrice != evaluated.ConservativePrice {
		return errors.New("price trigger evidence is invalid")
	}
	return nil
}

func ratioWithinBPS(numerator, denominator uint64, limit uint16) bool {
	if denominator == 0 {
		return false
	}
	leftHigh, leftLow := bits.Mul64(numerator, 10_000)
	rightHigh, rightLow := bits.Mul64(denominator, uint64(limit))
	return leftHigh < rightHigh || leftHigh == rightHigh && leftLow <= rightLow
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
