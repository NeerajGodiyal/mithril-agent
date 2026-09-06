package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const shadowReplayRejectionMaxBytes = 16 << 10
const shadowReplayRoundTripAbsent = "training_round_trip_absent"

const shadowResearchRejectionUsage = `Usage: mithril-agent shadow research-rejection --receipt PATH --policy PATH --max-age DURATION

Reads one private replay-rejection receipt and emits a bounded, current-policy
learning hint. A replay rejection is not a forward outcome or trading authority.`

// shadowReplayRejection retains one exact candidate's typed replay failure.
// InputJournals were verified before search; not every fold necessarily ran.
type shadowReplayRejection struct {
	Version               uint32                           `json:"version"`
	Status                string                           `json:"status"`
	PaperOnly             bool                             `json:"paper_only"`
	AdvisoryOnly          bool                             `json:"advisory_only"`
	Authorized            bool                             `json:"authorized"`
	Promotable            bool                             `json:"promotable"`
	EvaluatedAt           time.Time                        `json:"evaluated_at"`
	Market                string                           `json:"market"`
	BasePolicySHA256      string                           `json:"base_policy_sha256"`
	CandidatePolicySHA256 string                           `json:"candidate_policy_sha256"`
	ResearchPacketSHA256  string                           `json:"research_packet_sha256"`
	ParameterChanges      []researchpacket.ParameterChange `json:"parameter_changes"`
	InputJournals         []shadowJournalProvenance        `json:"input_journals"`
	Reason                string                           `json:"reason"`
}

func (receipt shadowReplayRejection) validate(now time.Time) error {
	if receipt.Version != 1 || receipt.Status != "replay_rejected" ||
		!receipt.PaperOnly || !receipt.AdvisoryOnly || receipt.Authorized || receipt.Promotable ||
		(receipt.Market != shadow.MarketSOLUSDC && receipt.Market != shadow.MarketJUPUSDC) ||
		receipt.EvaluatedAt.IsZero() || receipt.EvaluatedAt.Location() != time.UTC || receipt.EvaluatedAt.After(now) ||
		!validLowerSHA256(receipt.BasePolicySHA256) || !validLowerSHA256(receipt.CandidatePolicySHA256) ||
		!validLowerSHA256(receipt.ResearchPacketSHA256) || receipt.Reason != shadowReplayRoundTripAbsent ||
		!validShadowResearchOutcomeChanges(receipt.ParameterChanges) || len(receipt.InputJournals) != shadowWalkForwardWindows+1 {
		return errors.New("replay rejection envelope is invalid")
	}
	first := receipt.EvaluatedAt.Truncate(24*time.Hour).AddDate(0, 0, -len(receipt.InputJournals))
	for index, provenance := range receipt.InputJournals {
		if provenance.validate() != nil || provenance.Day != first.AddDate(0, 0, index).Format("2006-01-02") {
			return errors.New("replay rejection input journals are invalid")
		}
	}
	return nil
}

func (receipt shadowReplayRejection) validateCandidate(base shadow.Policy) error {
	if base.Cluster != shadow.Mainnet || base.Adaptive == nil || shadowMarketPair(base) != receipt.Market {
		return errors.New("replay rejection policy is invalid")
	}
	fingerprint, err := base.Fingerprint()
	if err != nil || fingerprint != receipt.BasePolicySHA256 {
		return errors.New("replay rejection base policy differs")
	}
	candidate := base
	adaptive := *base.Adaptive
	for _, change := range receipt.ParameterChanges {
		current, ok := shadowAdaptiveParameter(adaptive, change.Name)
		if !ok || current != change.Current || !setShadowAdaptiveParameter(&adaptive, change.Name, change.Proposed) {
			return errors.New("replay rejection parameter change differs from current policy")
		}
	}
	candidate.Adaptive = &adaptive
	fingerprint, err = candidate.Fingerprint()
	if err != nil || fingerprint != receipt.CandidatePolicySHA256 ||
		validateAdaptiveCandidateDelta(*base.Adaptive, adaptive) != nil {
		return errors.New("replay rejection candidate is outside the reviewed boundary")
	}
	return nil
}

func readShadowReplayRejection(path string, now time.Time) (shadowReplayRejection, error) {
	raw, err := securefile.ReadPrivate(path, shadowReplayRejectionMaxBytes)
	if err != nil {
		return shadowReplayRejection{}, err
	}
	var receipt shadowReplayRejection
	if err := strictjson.Decode(raw, &receipt); err != nil {
		return shadowReplayRejection{}, err
	}
	return receipt, receipt.validate(now)
}

// retainReplayRejection preserves the original search error. Only the existing
// typed inactivity condition from a packet-bound candidate becomes a lesson.
func (controller *shadowResearchController) retainReplayRejection(
	cause error, binding *researchpacket.Packet, candidates []shadow.Policy,
	days []shadowWalkForwardDay, now time.Time,
) error {
	if !errors.Is(cause, errNoAdaptiveTrainingRoundTrip) || controller.replayRejection == "" || binding == nil || len(candidates) != 1 {
		return cause
	}
	if err := controller.writeReplayRejection(binding, candidates[0], days, now); err != nil {
		return errors.Join(cause, fmt.Errorf("retain replay rejection: %w", err))
	}
	return cause
}

func (controller *shadowResearchController) writeReplayRejection(
	binding *researchpacket.Packet, candidate shadow.Policy, days []shadowWalkForwardDay, now time.Time,
) error {
	bound, _, _, err := controller.bindResearchPacket(binding.ContentSHA256, now)
	if err != nil {
		return err
	}
	baseHash, err := controller.policy.Fingerprint()
	if err != nil {
		return err
	}
	candidateHash, err := candidate.Fingerprint()
	if err != nil {
		return err
	}
	boundHash, err := bound.Fingerprint()
	if err != nil || boundHash != candidateHash {
		return errors.New("replay rejection candidate differs from its research packet")
	}
	receipt := shadowReplayRejection{
		Version: 1, Status: "replay_rejected", PaperOnly: true, AdvisoryOnly: true,
		EvaluatedAt: now.UTC(), Market: shadowMarketPair(controller.policy),
		BasePolicySHA256: baseHash, CandidatePolicySHA256: candidateHash,
		ResearchPacketSHA256: binding.ContentSHA256,
		ParameterChanges:     shadowAdaptiveParameterDiff(*controller.policy.Adaptive, *candidate.Adaptive),
		Reason:               shadowReplayRoundTripAbsent,
	}
	for _, day := range days {
		receipt.InputJournals = append(receipt.InputJournals, day.Provenance)
	}
	if err := receipt.validate(now); err != nil {
		return err
	}
	if err := receipt.validateCandidate(controller.policy); err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return withShadowLifecycleLock(controller.lifecycleLock, func() error {
		previous, err := readShadowReplayRejection(controller.replayRejection, now)
		if err == nil {
			if previous.BasePolicySHA256 == baseHash {
				if err := previous.validateCandidate(controller.policy); err != nil {
					return err
				}
			}
			// Repeating the same experiment must not refresh its evidence age.
			previous.EvaluatedAt = receipt.EvaluatedAt
			old, marshalErr := json.Marshal(previous)
			if marshalErr != nil {
				return marshalErr
			}
			if bytes.Equal(old, encoded) {
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// ponytail: retain only the latest rejection per challenger pointer; use a bounded
		// journal if comparing older rejected experiments becomes necessary.
		return securefile.ReplacePrivate(controller.replayRejection, encoded, shadowReplayRejectionMaxBytes)
	})
}

func runShadowResearchRejection(args []string, output io.Writer) error {
	return runShadowResearchRejectionWith(args, output, time.Now().UTC())
}

func runShadowResearchRejectionWith(args []string, output io.Writer, now time.Time) error {
	flags := flag.NewFlagSet("shadow research-rejection", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("receipt", "", "private replay-rejection receipt")
	policyPath := flags.String("policy", "", "exact current paper policy")
	maxAge := flags.Duration("max-age", 0, "maximum hint age, greater than 0 and at most 168h")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowResearchRejectionUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !absoluteClean(*path) || !absoluteClean(*policyPath) ||
		*maxAge <= 0 || *maxAge > shadowResearchOutcomeMaxAge {
		return errors.New("shadow research-rejection requires clean absolute receipt and policy paths and max-age in (0,168h]")
	}
	receipt, err := readShadowReplayRejection(*path, now)
	if err != nil {
		return err
	}
	policy, err := loadActiveShadowPolicy(*policyPath)
	if err != nil {
		return err
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return err
	}
	var hints []shadowResearchOutcomeHint
	if receipt.BasePolicySHA256 == fingerprint && receipt.Market == shadowMarketPair(policy) && !receipt.EvaluatedAt.Before(now.Add(-*maxAge)) {
		if err := receipt.validateCandidate(policy); err != nil {
			return err
		}
		hints = append(hints, shadowResearchOutcomeHint{Market: receipt.Market, ParameterChanges: receipt.ParameterChanges,
			State: "replay_rejected", Reasons: []string{receipt.Reason}})
	}
	return json.NewEncoder(output).Encode(shadowResearchOutcomePromptSummary{
		Version: 1, Status: "replay_rejection_learning_hints", PaperOnly: true, AdvisoryOnly: true, Hints: hints,
	})
}
