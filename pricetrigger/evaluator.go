package pricetrigger

import (
	"context"
	"errors"
	"time"
)

type Source interface {
	IdentitySHA256() string
	Latest(context.Context, string) (Sample, error)
}

// SlotBoundSource reads through a node whose progress the caller has already
// proved. A source that implements this is always queried through it, so a
// caller that cannot supply a proven slot gets a refusal rather than a reading
// taken from a node that may have stopped advancing.
type SlotBoundSource interface {
	Source
	LatestAtSlot(ctx context.Context, feed string, minContextSlot uint64) (Sample, error)
}

type Evaluator struct {
	primary   Source
	secondary Source
	now       func() time.Time
}

func NewEvaluator(primary, secondary Source, now func() time.Time) (*Evaluator, error) {
	if primary == nil || secondary == nil {
		return nil, errors.New("two price sources are required")
	}
	primaryID := primary.IdentitySHA256()
	secondaryID := secondary.IdentitySHA256()
	if !validDigest(primaryID) || !validDigest(secondaryID) || primaryID == secondaryID {
		return nil, errors.New("price sources must have distinct valid identities")
	}
	if now == nil {
		now = time.Now
	}
	return &Evaluator{primary: primary, secondary: secondary, now: now}, nil
}

// Evaluate reads both sources without a proven context slot. A slot-bound
// source refuses such a read, which is the intended fail-closed behaviour.
func (e *Evaluator) Evaluate(ctx context.Context, policy Policy) (Evidence, error) {
	return e.EvaluateAtSlot(ctx, policy, 0)
}

// EvaluateAtSlot binds any slot-bound source to a slot the caller has already
// proved is advancing and within its lag threshold.
func (e *Evaluator) EvaluateAtSlot(
	ctx context.Context,
	policy Policy,
	minContextSlot uint64,
) (Evidence, error) {
	if err := policy.Validate(); err != nil {
		return Evidence{}, err
	}
	if e.primary.IdentitySHA256() != policy.PrimarySourceSHA256 ||
		e.secondary.IdentitySHA256() != policy.SecondarySourceSHA256 {
		return Evidence{}, errors.New("price sources do not match the active policy")
	}
	type result struct {
		index  int
		sample Sample
		err    error
	}
	queryContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, 2)
	for index, source := range []Source{e.primary, e.secondary} {
		go func(index int, source Source) {
			var sample Sample
			var err error
			if bound, ok := source.(SlotBoundSource); ok {
				sample, err = bound.LatestAtSlot(queryContext, policy.Feed, minContextSlot)
			} else {
				sample, err = source.Latest(queryContext, policy.Feed)
			}
			results <- result{index: index, sample: sample, err: err}
		}(index, source)
	}
	var samples [2]Sample
	for received := 0; received < len(samples); received++ {
		select {
		case <-ctx.Done():
			return Evidence{}, ctx.Err()
		case result := <-results:
			if result.err != nil {
				cancel()
				if result.index == 0 {
					return Evidence{}, errors.New("primary price source is unavailable")
				}
				return Evidence{}, errors.New("secondary price source is unavailable")
			}
			samples[result.index] = result.sample
		}
	}
	return Evaluate(policy, samples[0], samples[1], e.now().UTC())
}
