package main

import (
	"errors"
	"slices"

	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	shadowPaperExperimentLegacyVersion = uint32(1)
	shadowPaperExperimentExactVersion  = uint32(2)
	shadowPaperExperimentVersion       = uint32(3)
)

var (
	shadowExperimentEnforcedFields = []string{
		"cadence_seconds", "max_drawdown_bps", "minimum_order_micros",
		"maximum_order_micros", "preference",
	}
	shadowExperimentPortfolioFields  = []string{"paper_capital_micros"}
	shadowExperimentExactEnforced    = []string{"cadence_seconds", "max_drawdown_bps", "preference"}
	shadowExperimentLegacyEnforced   = []string{"cadence_seconds", "max_drawdown_bps"}
	shadowExperimentExactUnsupported = []string{"paper_capital_micros", "minimum_order_micros", "maximum_order_micros"}
	shadowExperimentLegacyAdvisory   = []string{"preference"}
)

// shadowPaperExperiment binds the operator-owned requirement that constrained
// a paper candidate. Total capital is enforced by the separate portfolio
// manifest; this artifact binds the per-order and strategy requirements.
type shadowPaperExperiment struct {
	Version           uint32                     `json:"version"`
	Authorized        bool                       `json:"authorized"`
	PaperOnly         bool                       `json:"paper_only"`
	InstructionSHA256 string                     `json:"instruction_sha256"`
	Instruction       paperdashboard.Instruction `json:"instruction"`
	EnforcedFields    []string                   `json:"enforced_fields"`
	UnsupportedFields []string                   `json:"unsupported_fields"`
	PortfolioFields   []string                   `json:"portfolio_fields,omitempty"`
	AdvisoryFields    []string                   `json:"advisory_fields"`
}

func loadShadowPaperExperiment(path string, policy shadow.Policy) (*shadowPaperExperiment, error) {
	instruction, digest, err := paperdashboard.LoadInstruction(path)
	if err != nil {
		return nil, errors.New("shadow paper experiment instruction is invalid")
	}
	binding := &shadowPaperExperiment{
		Version: shadowPaperExperimentVersion, PaperOnly: true,
		InstructionSHA256: digest, Instruction: *instruction,
		EnforcedFields:  append([]string(nil), shadowExperimentEnforcedFields...),
		PortfolioFields: append([]string(nil), shadowExperimentPortfolioFields...),
	}
	if err := binding.validate(policy); err != nil {
		return nil, err
	}
	return binding, nil
}

func (binding shadowPaperExperiment) validate(policy shadow.Policy) error {
	if binding.Version != shadowPaperExperimentLegacyVersion &&
		binding.Version != shadowPaperExperimentExactVersion && binding.Version != shadowPaperExperimentVersion ||
		binding.Authorized || !binding.PaperOnly {
		return errors.New("shadow paper experiment safety markers are invalid")
	}
	instruction := binding.Instruction
	digest, err := paperdashboard.InstructionSHA256(instruction)
	if err != nil || digest != binding.InstructionSHA256 {
		return errors.New("shadow paper experiment instruction digest does not match")
	}
	enforced, unsupported, portfolio, advisory := shadowExperimentEnforcedFields, []string(nil), shadowExperimentPortfolioFields, []string(nil)
	switch binding.Version {
	case shadowPaperExperimentVersion:
		if instruction.Version != paperdashboard.InstructionVersion ||
			policy.MinimumOrderValueMicros != instruction.MinimumOrderMicros ||
			policy.MaximumOrderValueMicros < instruction.MinimumOrderMicros ||
			policy.MaximumOrderValueMicros > instruction.MaximumOrderMicros {
			return errors.New("shadow paper experiment order sizing is not applied to the candidate policy")
		}
	case shadowPaperExperimentExactVersion:
		if instruction.Version != 3 || instruction.PaperCapitalMicros != 0 ||
			instruction.MinimumOrderMicros != 0 || instruction.MaximumOrderMicros != 0 {
			return errors.New("shadow paper experiment v2 instruction is invalid")
		}
		enforced, unsupported, portfolio = shadowExperimentExactEnforced, shadowExperimentExactUnsupported, nil
	case shadowPaperExperimentLegacyVersion:
		if instruction.Version != 3 || instruction.PaperCapitalMicros != 0 ||
			instruction.MinimumOrderMicros != 0 || instruction.MaximumOrderMicros != 0 {
			return errors.New("shadow paper experiment legacy instruction is invalid")
		}
		enforced, unsupported, portfolio, advisory = shadowExperimentLegacyEnforced, shadowExperimentExactUnsupported, nil, shadowExperimentLegacyAdvisory
	}
	if !slices.Equal(binding.EnforcedFields, enforced) ||
		!slices.Equal(binding.UnsupportedFields, unsupported) ||
		!slices.Equal(binding.PortfolioFields, portfolio) ||
		!slices.Equal(binding.AdvisoryFields, advisory) {
		return errors.New("shadow paper experiment field classification is invalid")
	}
	market := policy.Market
	if market == "" && policy.Version == shadow.LegacyVersion {
		market = shadow.MarketSOLUSDC
	}
	if instruction.Market != "all" && instruction.Market != market {
		return errors.New("shadow paper experiment market does not match the candidate policy")
	}
	if policy.Adaptive == nil {
		return errors.New("shadow paper experiment drawdown requires an adaptive candidate policy")
	}
	if instruction.CadenceSeconds != policy.TickSeconds {
		return errors.New("shadow paper experiment cadence cannot be applied to the candidate policy")
	}
	if instruction.MaxDrawdownBPS != policy.Adaptive.MaxDrawdownBPS {
		return errors.New("shadow paper experiment drawdown cannot be applied to the candidate policy")
	}
	return nil
}

func (binding shadowPaperExperiment) validatePreference(base, candidate shadow.Policy) error {
	if binding.Version == shadowPaperExperimentLegacyVersion {
		return nil
	}
	if base.Adaptive == nil || candidate.Adaptive == nil {
		return errors.New("shadow paper experiment preference requires adaptive policies")
	}
	if !adaptivePolicyMatchesPreference(*base.Adaptive, *candidate.Adaptive, binding.Instruction.Preference) {
		return errors.New("shadow paper candidate does not match the operator research preference")
	}
	return nil
}
