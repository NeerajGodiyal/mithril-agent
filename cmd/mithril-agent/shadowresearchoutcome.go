package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/researchpacket"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

const (
	shadowResearchOutcomeVersion  = uint32(1)
	shadowResearchOutcomeLimit    = uint32(16)
	shadowResearchOutcomeMaxLimit = uint32(64)

	shadowResearchForwardEvaluated     = "research.forward_evaluated"
	shadowResearchSelectionConfirmed   = "research.paper_selection_confirmed"
	shadowResearchOutcomeSummaryStatus = "research_outcome_summary"
)

const shadowResearchOutcomeSummaryUsage = `Usage: mithril-agent shadow research-outcomes --journal PATH [--limit 16] [--prompt-safe]

Reads and verifies the append-only paper research outcome journal, then emits a
compact advisory summary. It cannot authorize, select, sign, submit, or trade.`

// shadowResearchOutcomeReceipt deliberately stores only immutable identifiers,
// bounded parameter changes, and deterministic forward-test measurements. It
// excludes artifact paths, source prose, policies, keys, and any authority.
type shadowResearchOutcomeReceipt struct {
	Version                  uint32                           `json:"version"`
	PaperOnly                bool                             `json:"paper_only"`
	Authorized               bool                             `json:"authorized"`
	Promotable               bool                             `json:"promotable"`
	Market                   string                           `json:"market"`
	HypothesisID             string                           `json:"hypothesis_id"`
	BasePolicySHA256         string                           `json:"base_policy_sha256"`
	ResearchPacketSHA256     string                           `json:"research_packet_sha256"`
	CandidateSHA256          string                           `json:"candidate_sha256"`
	CandidatePolicySHA256    string                           `json:"candidate_policy_sha256"`
	ParameterChanges         []researchpacket.ParameterChange `json:"parameter_changes"`
	ForwardStatus            string                           `json:"forward_status"`
	CompleteDays             uint32                           `json:"complete_days"`
	ChallengerFullRoundTrips uint64                           `json:"challenger_full_round_trips"`
	RequiredFullRoundTrips   uint64                           `json:"required_full_round_trips"`
	ChallengerDailyWins      uint32                           `json:"challenger_daily_wins"`
	RequiredDailyWins        uint32                           `json:"required_daily_wins"`
	AggregateAdvantageMicros int64                            `json:"aggregate_advantage_micros"`
	RequiredAdvantageMicros  uint64                           `json:"required_advantage_micros"`
	Reasons                  []string                         `json:"reasons,omitempty"`
}

type shadowResearchOutcome struct {
	Receipt            shadowResearchOutcomeReceipt `json:"receipt"`
	EvaluatedAt        time.Time                    `json:"evaluated_at"`
	SelectionConfirmed bool                         `json:"selection_confirmed"`
	SelectedAt         *time.Time                   `json:"selected_at,omitempty"`
}

type shadowResearchOutcomeSummary struct {
	Version             uint32                  `json:"version"`
	Status              string                  `json:"status"`
	PaperOnly           bool                    `json:"paper_only"`
	AdvisoryOnly        bool                    `json:"advisory_only"`
	Authorized          bool                    `json:"authorized"`
	Records             uint64                  `json:"records"`
	ChainHeadSHA256     string                  `json:"chain_head_sha256,omitempty"`
	CandidatesEvaluated uint64                  `json:"candidates_evaluated"`
	SelectionsConfirmed uint64                  `json:"selections_confirmed"`
	Outcomes            []shadowResearchOutcome `json:"outcomes"`
}

type shadowResearchOutcomeHint struct {
	Market           string                           `json:"market"`
	ParameterChanges []researchpacket.ParameterChange `json:"parameter_changes"`
	State            string                           `json:"state"`
	Reasons          []string                         `json:"reasons,omitempty"`
}

type shadowResearchOutcomePromptSummary struct {
	Version      uint32                      `json:"version"`
	Status       string                      `json:"status"`
	PaperOnly    bool                        `json:"paper_only"`
	AdvisoryOnly bool                        `json:"advisory_only"`
	Authorized   bool                        `json:"authorized"`
	Hints        []shadowResearchOutcomeHint `json:"hints"`
}

// recordShadowResearchForwardOutcome durably records the fixed forward result.
// Callers must do this before mutating the paper champion pointer.
func recordShadowResearchForwardOutcome(
	path string,
	at time.Time,
	base shadow.Policy,
	candidate shadowPaperCandidate,
	candidateSHA256 string,
	challenge shadowChallengeResult,
) (shadowResearchOutcomeReceipt, bool, error) {
	receipt, err := newShadowResearchOutcomeReceipt(base, candidate, candidateSHA256, challenge)
	if err != nil {
		return shadowResearchOutcomeReceipt{}, false, err
	}
	appended, err := appendShadowResearchOutcome(path, at, shadowResearchForwardEvaluated, receipt)
	return receipt, appended, err
}

// recordShadowResearchSelectionConfirmation records that the already-qualified
// paper candidate became the paper champion. It requires the exact preceding
// forward receipt and must be called only after the pointer mutation succeeds.
func recordShadowResearchSelectionConfirmation(
	path string,
	at time.Time,
	base shadow.Policy,
	candidate shadowPaperCandidate,
	candidateSHA256 string,
	challenge shadowChallengeResult,
) (shadowResearchOutcomeReceipt, bool, error) {
	receipt, err := newShadowResearchOutcomeReceipt(base, candidate, candidateSHA256, challenge)
	if err != nil {
		return shadowResearchOutcomeReceipt{}, false, err
	}
	if receipt.ForwardStatus != "challenger_qualified_for_paper_selection" {
		return shadowResearchOutcomeReceipt{}, false, errors.New("paper selection confirmation requires a qualified forward result")
	}
	appended, err := appendShadowResearchOutcome(path, at, shadowResearchSelectionConfirmed, receipt)
	return receipt, appended, err
}

// recordShadowResearchSelectionFromForward reconciles the narrow crash window
// where the champion pointer was updated but confirmation could not be written.
// Call it only after independently revalidating that candidateSHA256 is the
// current paper champion; the qualified forward receipt remains the payload.
func recordShadowResearchSelectionFromForward(
	path string,
	at time.Time,
	candidateSHA256 string,
) (receipt shadowResearchOutcomeReceipt, appended bool, err error) {
	if at.IsZero() || at.Location() != time.UTC || !validLowerSHA256(candidateSHA256) {
		return shadowResearchOutcomeReceipt{}, false, errors.New("paper selection reconciliation input is invalid")
	}
	store, err := journal.OpenRotating(path)
	if err != nil {
		return shadowResearchOutcomeReceipt{}, false, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	summary, err := foldShadowResearchOutcomes(store.Records())
	if err != nil {
		return shadowResearchOutcomeReceipt{}, false, err
	}
	for _, outcome := range summary.Outcomes {
		if outcome.Receipt.CandidateSHA256 != candidateSHA256 {
			continue
		}
		if outcome.Receipt.ForwardStatus != "challenger_qualified_for_paper_selection" {
			return shadowResearchOutcomeReceipt{}, false, errors.New("paper champion lacks a qualified forward outcome")
		}
		appended, err := appendShadowResearchOutcomeRecord(
			store, at, shadowResearchSelectionConfirmed, outcome.Receipt,
		)
		return outcome.Receipt, appended, err
	}
	return shadowResearchOutcomeReceipt{}, false, errors.New("paper champion lacks its forward outcome")
}

// reconcileShadowResearchSelectionFromForward records a missing confirmation
// after an upgrade, while leaving an empty pre-journal selection unchanged.
func reconcileShadowResearchSelectionFromForward(
	path string,
	at time.Time,
	candidateSHA256 string,
) (bool, error) {
	summary, err := readShadowResearchOutcomeSummary(path)
	if err != nil {
		missing, inspectErr := shadowResearchOutcomeJournalMissing(path)
		if inspectErr != nil {
			return false, inspectErr
		}
		if missing {
			return false, nil
		}
		return false, err
	}
	if summary.Records == 0 {
		return false, nil
	}
	_, appended, err := recordShadowResearchSelectionFromForward(path, at, candidateSHA256)
	return appended, err
}

func shadowResearchOutcomeJournalMissing(path string) (bool, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	base := filepath.Base(path)
	for _, entry := range entries {
		if entry.Name() == base || strings.HasPrefix(entry.Name(), base+".") {
			return false, nil
		}
	}
	return true, nil
}

func newShadowResearchOutcomeReceipt(
	base shadow.Policy,
	candidate shadowPaperCandidate,
	candidateSHA256 string,
	challenge shadowChallengeResult,
) (shadowResearchOutcomeReceipt, error) {
	if err := candidate.validateAgainst(base); err != nil {
		return shadowResearchOutcomeReceipt{}, errors.New("research outcome candidate is invalid")
	}
	if candidate.ResearchPacket == nil || candidate.ResearchPacket.Validate() != nil {
		return shadowResearchOutcomeReceipt{}, errors.New("research outcome requires a validated research packet")
	}
	if !validLowerSHA256(candidateSHA256) || challenge.Version != 1 ||
		challenge.Authorized || challenge.Promotable || !challenge.PaperOnly ||
		challenge.PointerUpdated || challenge.EvaluationMode != shadow.EvaluationResetDaily ||
		challenge.ChallengerCandidateSHA256 != candidateSHA256 ||
		challenge.ChallengerPolicySHA256 != candidate.CandidatePolicySHA256 {
		return shadowResearchOutcomeReceipt{}, errors.New("research outcome forward result is not bound to the paper candidate")
	}
	qualified := challenge.Status == "challenger_qualified_for_paper_selection"
	if challenge.Status != "challenger_not_qualified" && !qualified {
		return shadowResearchOutcomeReceipt{}, errors.New("research outcome forward status is invalid")
	}
	if qualified != challenge.EligibleForPaperSelection ||
		(qualified && len(challenge.Reasons) != 0) ||
		(!qualified && len(challenge.Reasons) == 0) {
		return shadowResearchOutcomeReceipt{}, errors.New("research outcome forward decision is inconsistent")
	}
	if qualified && (challenge.CompleteDays < 7 || challenge.RequiredFullRoundTrips == 0 ||
		challenge.ChallengerFullRoundTrips < challenge.RequiredFullRoundTrips ||
		challenge.RequiredDailyWins == 0 || challenge.ChallengerDailyWins < challenge.RequiredDailyWins ||
		challenge.AggregateAdvantageMicros < 0 ||
		uint64(challenge.AggregateAdvantageMicros) < challenge.RequiredAggregateAdvantageMicros) {
		return shadowResearchOutcomeReceipt{}, errors.New("research outcome qualified measurements are inconsistent")
	}
	for _, reason := range challenge.Reasons {
		if !validShadowResearchOutcomeToken(reason) {
			return shadowResearchOutcomeReceipt{}, errors.New("research outcome reason is invalid")
		}
	}
	packet := candidate.ResearchPacket
	receipt := shadowResearchOutcomeReceipt{
		Version: shadowResearchOutcomeVersion, PaperOnly: true,
		Market: packet.Market, HypothesisID: packet.HypothesisID,
		BasePolicySHA256:      candidate.BasePolicySHA256,
		ResearchPacketSHA256:  packet.ContentSHA256,
		CandidateSHA256:       candidateSHA256,
		CandidatePolicySHA256: candidate.CandidatePolicySHA256,
		ParameterChanges:      append([]researchpacket.ParameterChange(nil), packet.CandidateParameterDiff...),
		ForwardStatus:         challenge.Status, CompleteDays: challenge.CompleteDays,
		ChallengerFullRoundTrips: challenge.ChallengerFullRoundTrips,
		RequiredFullRoundTrips:   challenge.RequiredFullRoundTrips,
		ChallengerDailyWins:      challenge.ChallengerDailyWins,
		RequiredDailyWins:        challenge.RequiredDailyWins,
		AggregateAdvantageMicros: challenge.AggregateAdvantageMicros,
		RequiredAdvantageMicros:  challenge.RequiredAggregateAdvantageMicros,
		Reasons:                  append([]string(nil), challenge.Reasons...),
	}
	if err := receipt.validate(); err != nil {
		return shadowResearchOutcomeReceipt{}, err
	}
	return receipt, nil
}

func (receipt shadowResearchOutcomeReceipt) validate() error {
	if receipt.Version != shadowResearchOutcomeVersion || !receipt.PaperOnly ||
		receipt.Authorized || receipt.Promotable || !validShadowResearchOutcomeMarket(receipt.Market) ||
		len(receipt.HypothesisID) < 3 || len(receipt.HypothesisID) > 64 ||
		!validShadowResearchOutcomeToken(receipt.HypothesisID) ||
		!validLowerSHA256(receipt.BasePolicySHA256) ||
		!validLowerSHA256(receipt.ResearchPacketSHA256) ||
		!validLowerSHA256(receipt.CandidateSHA256) ||
		!validLowerSHA256(receipt.CandidatePolicySHA256) ||
		!validShadowResearchOutcomeChanges(receipt.ParameterChanges) {
		return errors.New("research outcome receipt envelope is invalid")
	}
	qualified := receipt.ForwardStatus == "challenger_qualified_for_paper_selection"
	if receipt.ForwardStatus != "challenger_not_qualified" && !qualified {
		return errors.New("research outcome receipt status is invalid")
	}
	if len(receipt.Reasons) > 16 || qualified && len(receipt.Reasons) != 0 ||
		!qualified && len(receipt.Reasons) == 0 {
		return errors.New("research outcome receipt decision is inconsistent")
	}
	for _, reason := range receipt.Reasons {
		if !validShadowResearchOutcomeToken(reason) {
			return errors.New("research outcome receipt reason is invalid")
		}
	}
	return nil
}

func validShadowResearchOutcomeMarket(value string) bool {
	base, ok := strings.CutSuffix(value, "/USDC")
	if !ok || len(base) < 2 || len(base) > 12 {
		return false
	}
	for _, character := range base {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validShadowResearchOutcomeChanges(changes []researchpacket.ParameterChange) bool {
	if len(changes) == 0 || len(changes) > 8 {
		return false
	}
	limits := map[string][2]uint64{
		"fast_window": {2, 120}, "slow_window": {3, 240},
		"minimum_signal_bps": {1, 5_000}, "cooldown_seconds": {0, 86_400},
	}
	seen := make(map[string]struct{}, len(changes))
	proposed := make(map[string]uint64, len(changes))
	for _, change := range changes {
		limit, ok := limits[change.Name]
		if !ok || change.Current == change.Proposed || change.Proposed < limit[0] ||
			change.Proposed > limit[1] {
			return false
		}
		if _, duplicate := seen[change.Name]; duplicate {
			return false
		}
		seen[change.Name] = struct{}{}
		proposed[change.Name] = change.Proposed
	}
	if fast, fastChanged := proposed["fast_window"]; fastChanged {
		if slow, slowChanged := proposed["slow_window"]; slowChanged && fast >= slow {
			return false
		}
	}
	return true
}

func validShadowResearchOutcomeToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func appendShadowResearchOutcome(
	path string,
	at time.Time,
	eventType string,
	receipt shadowResearchOutcomeReceipt,
) (appended bool, err error) {
	if at.IsZero() || at.Location() != time.UTC {
		return false, errors.New("research outcome time must be UTC")
	}
	store, err := journal.OpenRotating(path)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return appendShadowResearchOutcomeRecord(store, at, eventType, receipt)
}

func appendShadowResearchOutcomeRecord(
	store *journal.Store,
	at time.Time,
	eventType string,
	receipt shadowResearchOutcomeReceipt,
) (bool, error) {
	if store == nil || receipt.validate() != nil ||
		(eventType != shadowResearchForwardEvaluated && eventType != shadowResearchSelectionConfirmed) {
		return false, errors.New("research outcome append input is invalid")
	}
	if eventType == shadowResearchSelectionConfirmed &&
		receipt.ForwardStatus != "challenger_qualified_for_paper_selection" {
		return false, errors.New("paper selection confirmation requires a qualified forward outcome")
	}
	records := store.Records()
	if _, err := foldShadowResearchOutcomes(records); err != nil {
		return false, err
	}
	want, err := json.Marshal(receipt)
	if err != nil {
		return false, errors.New("could not encode research outcome receipt")
	}
	forwardFound := false
	for _, record := range records {
		if record.ActionID != receipt.CandidateSHA256 {
			continue
		}
		if record.Type == shadowResearchForwardEvaluated && bytes.Equal(record.Payload, want) {
			forwardFound = true
		}
		if record.Type != eventType {
			continue
		}
		if bytes.Equal(record.Payload, want) {
			return false, nil
		}
		return false, errors.New("research outcome event collision")
	}
	if eventType == shadowResearchSelectionConfirmed && !forwardFound {
		return false, errors.New("paper selection confirmation lacks its exact forward outcome")
	}
	if _, err := store.Append(at, eventType, receipt.CandidateSHA256, receipt); err != nil {
		return false, err
	}
	return true, nil
}

func readShadowResearchOutcomeSummary(path string) (shadowResearchOutcomeSummary, error) {
	return readShadowResearchOutcomeSummaryLimit(path, 0)
}

func readShadowResearchOutcomeSummaryLimit(
	path string, limit uint32,
) (shadowResearchOutcomeSummary, error) {
	records, err := journal.ReadRecords(path)
	if err != nil {
		return shadowResearchOutcomeSummary{}, err
	}
	summary, err := foldShadowResearchOutcomes(records)
	if err != nil {
		return shadowResearchOutcomeSummary{}, err
	}
	if limit != 0 && uint32(len(summary.Outcomes)) > limit {
		summary.Outcomes = append([]shadowResearchOutcome(nil), summary.Outcomes[len(summary.Outcomes)-int(limit):]...)
	}
	return summary, nil
}

func foldShadowResearchOutcomes(records []journal.Record) (shadowResearchOutcomeSummary, error) {
	summary := shadowResearchOutcomeSummary{
		Version: shadowResearchOutcomeVersion, Status: shadowResearchOutcomeSummaryStatus,
		PaperOnly: true, AdvisoryOnly: true,
	}
	byCandidate := make(map[string]int)
	forwardPayload := make(map[string][]byte)
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.Type == journal.EventRotated {
			continue
		}
		if record.Type != shadowResearchForwardEvaluated && record.Type != shadowResearchSelectionConfirmed {
			return shadowResearchOutcomeSummary{}, errors.New("research outcome journal contains an unexpected event")
		}
		if !validLowerSHA256(record.ActionID) {
			return shadowResearchOutcomeSummary{}, errors.New("research outcome action ID is invalid")
		}
		var receipt shadowResearchOutcomeReceipt
		if err := strictjson.Decode(record.Payload, &receipt); err != nil || receipt.validate() != nil ||
			receipt.CandidateSHA256 != record.ActionID {
			return shadowResearchOutcomeSummary{}, errors.New("research outcome journal receipt is invalid")
		}
		canonical, err := json.Marshal(receipt)
		if err != nil {
			return shadowResearchOutcomeSummary{}, errors.New("could not encode research outcome receipt")
		}
		key := record.Type + "\x00" + record.ActionID
		if _, duplicate := seen[key]; duplicate {
			return shadowResearchOutcomeSummary{}, errors.New("research outcome journal repeats an event")
		}
		seen[key] = struct{}{}
		switch record.Type {
		case shadowResearchForwardEvaluated:
			forwardPayload[record.ActionID] = canonical
			byCandidate[record.ActionID] = len(summary.Outcomes)
			summary.Outcomes = append(summary.Outcomes, shadowResearchOutcome{
				Receipt: receipt, EvaluatedAt: record.At.UTC(),
			})
			summary.CandidatesEvaluated++
		case shadowResearchSelectionConfirmed:
			forward, ok := forwardPayload[record.ActionID]
			index, known := byCandidate[record.ActionID]
			if !ok || !known || !bytes.Equal(forward, canonical) ||
				receipt.ForwardStatus != "challenger_qualified_for_paper_selection" {
				return shadowResearchOutcomeSummary{}, errors.New("paper selection confirmation does not match a qualified forward outcome")
			}
			selectedAt := record.At.UTC()
			summary.Outcomes[index].SelectionConfirmed = true
			summary.Outcomes[index].SelectedAt = &selectedAt
			summary.SelectionsConfirmed++
		}
	}
	summary.Records = uint64(len(records))
	if len(records) != 0 {
		summary.ChainHeadSHA256 = records[len(records)-1].Hash
	}
	return summary, nil
}

func promptSafeShadowResearchOutcomeSummary(
	summary shadowResearchOutcomeSummary,
) shadowResearchOutcomePromptSummary {
	prompt := shadowResearchOutcomePromptSummary{
		Version: shadowResearchOutcomeVersion, Status: "research_outcome_learning_hints",
		PaperOnly: true, AdvisoryOnly: true,
		Hints: make([]shadowResearchOutcomeHint, 0, len(summary.Outcomes)),
	}
	for _, outcome := range summary.Outcomes {
		state := "rejected"
		if outcome.Receipt.ForwardStatus == "challenger_qualified_for_paper_selection" {
			state = "accepted"
		}
		if outcome.SelectionConfirmed {
			state = "selected"
		}
		prompt.Hints = append(prompt.Hints, shadowResearchOutcomeHint{
			Market: outcome.Receipt.Market,
			ParameterChanges: append(
				[]researchpacket.ParameterChange(nil), outcome.Receipt.ParameterChanges...,
			),
			State:   state,
			Reasons: append([]string(nil), outcome.Receipt.Reasons...),
		})
	}
	return prompt
}

func runShadowResearchOutcomeSummary(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow research-outcomes", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("journal", "", "append-only paper research outcome journal")
	limit := flags.Uint("limit", uint(shadowResearchOutcomeLimit), "latest candidate outcomes, 1..64")
	promptSafe := flags.Bool("prompt-safe", false, "omit identifiers, metrics, counts, and timestamps")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowResearchOutcomeSummaryUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*path) != *path || !absoluteClean(*path) ||
		*limit == 0 || *limit > uint(shadowResearchOutcomeMaxLimit) {
		return errors.New("shadow research-outcomes requires one clean absolute --journal path and --limit from 1 to 64")
	}
	summary, err := readShadowResearchOutcomeSummaryLimit(*path, uint32(*limit))
	if err != nil {
		return err
	}
	if *promptSafe {
		return json.NewEncoder(output).Encode(promptSafeShadowResearchOutcomeSummary(summary))
	}
	return json.NewEncoder(output).Encode(summary)
}
