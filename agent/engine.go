package agent

import (
	"errors"
	"fmt"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
)

type Engine struct {
	store *journal.Store
	now   func() time.Time
}

type ProposalResult struct {
	Proposal   Proposal
	Decision   string
	Reason     string
	JournalSeq uint64
	Recovered  bool
}

func NewEngine(store *journal.Store, now func() time.Time) (*Engine, error) {
	if store == nil {
		return nil, errors.New("journal store is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Engine{store: store, now: now}, nil
}

func (e *Engine) Propose(profile Profile, obs Observation) (ProposalResult, error) {
	return e.propose(profile, obs, EventActionProposed)
}

func (e *Engine) propose(profile Profile, obs Observation, proposalEvent string) (ProposalResult, error) {
	if proposalEvent != EventActionProposed && proposalEvent != EventActionShadowProposed {
		return ProposalResult{}, errors.New("proposal event type is invalid")
	}
	now := e.now().UTC()
	observedAt, err := profile.validateObservation(obs, now)
	if err != nil {
		return ProposalResult{}, err
	}
	obs.ObservedAt = observedAt
	windowStart, _, err := profile.scheduleWindow(now)
	if err != nil {
		return ProposalResult{}, err
	}
	profileFingerprint, err := profile.Fingerprint()
	if err != nil {
		return ProposalResult{}, err
	}
	intentID, err := ComputeActionID(profileFingerprint, windowStart)
	if err != nil {
		return ProposalResult{}, err
	}

	records := e.store.Records()
	var existing *Proposal
	shadowCount := 0
	reservedToday := uint64(0)
	day := now.Format(time.DateOnly)
	for _, record := range records {
		switch record.Type {
		case EventActionProposed, EventActionShadowProposed:
			var proposal Proposal
			if err := strictjson.Decode(record.Payload, &proposal); err != nil {
				return ProposalResult{}, fmt.Errorf("decode proposed action at sequence %d: %w", record.Sequence, err)
			}
			if proposal.ActionID == "" || proposal.ActionID != record.ActionID ||
				proposal.AmountLamports == 0 || proposal.FeeBudgetLamports == 0 ||
				proposal.ReservedLamports < proposal.AmountLamports ||
				proposal.ReservedLamports-proposal.AmountLamports != proposal.FeeBudgetLamports {
				return ProposalResult{}, fmt.Errorf("invalid proposed action at sequence %d", record.Sequence)
			}
			if proposal.ReserveLamports > ^uint64(0)-proposal.ReservedLamports ||
				proposal.ObservedBalanceLamports < proposal.ReserveLamports+proposal.ReservedLamports {
				return ProposalResult{}, fmt.Errorf("invalid proposed balance at sequence %d", record.Sequence)
			}
			if record.Type != proposalEvent {
				continue
			}
			if proposal.ReservationDayUTC == day {
				if ^uint64(0)-reservedToday < proposal.ReservedLamports {
					return ProposalResult{}, errors.New("journal reservation total overflow")
				}
				reservedToday += proposal.ReservedLamports
			}
			if proposal.ActionID == intentID {
				if existing != nil {
					return ProposalResult{}, errors.New("journal contains a duplicate proposal")
				}
				copy := proposal
				existing = &copy
			}
		case EventActionShadowed:
			if proposalEvent != EventActionShadowProposed {
				continue
			}
			if record.ActionID == intentID {
				shadowCount++
			}
		}
	}

	if existing != nil {
		return ProposalResult{
			Proposal:  *existing,
			Decision:  "proposed",
			Recovered: true,
		}, nil
	}
	if shadowCount != 0 {
		return ProposalResult{}, errors.New("journal contains a shadow completion without a proposal")
	}
	proposal, reason, err := profile.Propose(obs, now, reservedToday)
	if err != nil {
		return ProposalResult{}, err
	}
	if reason != "" {
		return ProposalResult{Decision: "skipped", Reason: reason}, nil
	}
	event, err := e.store.Append(now, proposalEvent, proposal.ActionID, proposal)
	if err != nil {
		return ProposalResult{}, err
	}
	return ProposalResult{
		Proposal:   proposal,
		Decision:   "proposed",
		JournalSeq: event.Sequence,
	}, nil
}

func (e *Engine) RunShadow(profile Profile, obs Observation) (ShadowResult, error) {
	proposed, err := e.propose(profile, obs, EventActionShadowProposed)
	if err != nil {
		return ShadowResult{}, err
	}
	if proposed.Decision == "skipped" {
		return ShadowResult{Decision: proposed.Decision, Reason: proposed.Reason}, nil
	}

	shadowCount := 0
	for _, record := range e.store.Records() {
		if record.Type == EventActionShadowed && record.ActionID == proposed.Proposal.ActionID {
			shadowCount++
		}
	}
	if shadowCount > 1 {
		return ShadowResult{}, errors.New("journal contains duplicate shadow completions")
	}
	if shadowCount == 1 {
		return ShadowResult{
			ActionID:       proposed.Proposal.ActionID,
			Decision:       "shadowed",
			AmountLamports: proposed.Proposal.AmountLamports,
			Recovered:      true,
		}, nil
	}
	event, err := e.store.Append(e.now().UTC(), EventActionShadowed, proposed.Proposal.ActionID, struct {
		Mode string `json:"mode"`
	}{Mode: "shadow"})
	if err != nil {
		return ShadowResult{}, err
	}
	return ShadowResult{
		ActionID:       proposed.Proposal.ActionID,
		Decision:       "shadowed",
		AmountLamports: proposed.Proposal.AmountLamports,
		JournalSeq:     event.Sequence,
		Recovered:      proposed.Recovered,
	}, nil
}
