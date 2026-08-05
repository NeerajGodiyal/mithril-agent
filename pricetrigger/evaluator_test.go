package pricetrigger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEvaluatorReadsBothBoundSources(t *testing.T) {
	now := time.Now().UTC()
	policy := testPolicy()
	policy.ThresholdMicros = 149_900_000
	primary := &sourceStub{
		identity: policy.PrimarySourceSHA256,
		sample:   testSample(policy.PrimarySourceSHA256, 150_100_000, now),
	}
	secondary := &sourceStub{
		identity: policy.SecondarySourceSHA256,
		sample:   testSample(policy.SecondarySourceSHA256, 150_000_000, now),
	}
	evaluator, err := NewEvaluator(primary, secondary, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := evaluator.Evaluate(t.Context(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Triggered || primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("evidence=%+v calls=%d/%d", evidence, primary.calls, secondary.calls)
	}
}

func TestEvaluatorRejectsBindingDriftBeforeQueries(t *testing.T) {
	policy := testPolicy()
	primary := &sourceStub{identity: strings.Repeat("c", 64)}
	secondary := &sourceStub{identity: policy.SecondarySourceSHA256}
	evaluator, err := NewEvaluator(primary, secondary, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(t.Context(), policy); err == nil ||
		!strings.Contains(err.Error(), "do not match") {
		t.Fatalf("binding error = %v", err)
	}
	if primary.calls != 0 || secondary.calls != 0 {
		t.Fatal("binding drift reached a price source")
	}
}

func TestEvaluatorDoesNotExposeSourceErrors(t *testing.T) {
	policy := testPolicy()
	primary := &sourceStub{
		identity: policy.PrimarySourceSHA256,
		err:      errors.New("secret provider response"),
	}
	secondary := &sourceStub{identity: policy.SecondarySourceSHA256}
	evaluator, err := NewEvaluator(primary, secondary, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluator.Evaluate(t.Context(), policy)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("source error = %v", err)
	}
}

type sourceStub struct {
	identity string
	sample   Sample
	err      error
	calls    int
}

func (stub *sourceStub) IdentitySHA256() string { return stub.identity }

func (stub *sourceStub) Latest(context.Context, string) (Sample, error) {
	stub.calls++
	return stub.sample, stub.err
}

type slotBoundStub struct {
	id    string
	slots []uint64
	plain int
	fixed Sample
}

func (s *slotBoundStub) IdentitySHA256() string { return s.id }

func (s *slotBoundStub) Latest(context.Context, string) (Sample, error) {
	s.plain++
	return s.fixed, nil
}

func (s *slotBoundStub) LatestAtSlot(_ context.Context, _ string, slot uint64) (Sample, error) {
	s.slots = append(s.slots, slot)
	return s.fixed, nil
}

// A slot-bound source must always be queried through its bound method, so a
// caller cannot accidentally take an unbound reading from it.
func TestEvaluatorAlwaysUsesTheBoundReadForSlotBoundSources(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	sample := Sample{
		Feed: FeedSOLUSD, PriceMicros: 73_000_000,
		ConfidenceMicros: 10_000, PublishedAt: now.Add(-10 * time.Second),
	}
	bound := &slotBoundStub{id: strings.Repeat("a", 64), fixed: sample}
	bound.fixed.SourceSHA256 = bound.id
	plain := &plainStub{id: strings.Repeat("b", 64), fixed: sample}
	plain.fixed.SourceSHA256 = plain.id

	evaluator, err := NewEvaluator(bound, plain, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Version: Version, Feed: FeedSOLUSD, Direction: SellAtOrAbove,
		ThresholdMicros: 1, MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
		MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
		PrimarySourceSHA256: bound.id, SecondarySourceSHA256: plain.id,
	}

	if _, err := evaluator.EvaluateAtSlot(t.Context(), policy, 4242); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(t.Context(), policy); err != nil {
		t.Fatal(err)
	}
	if len(bound.slots) != 2 {
		t.Fatalf("bound source was queried through LatestAtSlot %d times, want 2", len(bound.slots))
	}
	if bound.slots[0] != 4242 {
		t.Fatalf("authorizing read used slot %d, want 4242", bound.slots[0])
	}
	// Evaluate carries no proven slot and must say so rather than inventing one.
	if bound.slots[1] != 0 {
		t.Fatalf("advisory read used slot %d, want 0", bound.slots[1])
	}
	if bound.plain != 0 {
		t.Fatalf("bound source was queried unbound %d times, want 0", bound.plain)
	}
}

type plainStub struct {
	id    string
	fixed Sample
}

func (s *plainStub) IdentitySHA256() string                         { return s.id }
func (s *plainStub) Latest(context.Context, string) (Sample, error) { return s.fixed, nil }
