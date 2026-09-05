package researchpacket

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordedPacketFixture(t *testing.T, now time.Time) (Packet, RecordedObservations) {
	t.Helper()
	day := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	observation, err := (RecordedObservations{
		Version: 1, Kind: "recorded_paper_observations", PaperOnly: true, AdvisoryOnly: true,
		Market: "SOL/USDC", PolicySHA256: strings.Repeat("a", 64),
		ObservedFrom: day, ObservedThrough: day.Add(24*time.Hour - time.Nanosecond),
		Journal: RecordedJournal{Day: day.Format("2006-01-02"), Records: 100, ChainHeadSHA256: strings.Repeat("b", 64)},
		Metrics: ObservationMetrics{ObservableBPS: 10_000, Signals: 3, Fills: 2,
			VersusHoldMicros: -123, MaxDrawdownMicros: 456},
	}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	packet := candidatePacket(now)
	packet.Version, packet.Market, packet.VerifiedFacts = RecordedVersion, observation.Market, nil
	packet.RecordedEvidence = &RecordedReference{ContentSHA256: observation.ContentSHA256,
		MetricIDs: []string{"observable_bps", "signals", "fills", "versus_hold_micros", "max_drawdown_micros"}}
	return packet, observation
}

func recordedJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRecordedPacketRequiresHostBinding(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	input, observation := recordedPacketFixture(t, now)
	data := recordedJSON(t, input)
	if _, err := Parse(data, now); err == nil {
		t.Fatal("unbound v2 packet was accepted")
	}
	packet, err := ParseWithRecorded(data, &observation, now)
	if err != nil || packet.Validate() != nil || !packet.StatusAt(now).Actionable ||
		packet.RecordedObservations == nil || !reflect.DeepEqual(*packet.RecordedObservations, observation) {
		t.Fatalf("host binding failed: %+v, %v", packet, err)
	}
	if packet.StatusAt(now).Sources != 0 || packet.StatusAt(now).VerifiedFacts != 0 {
		t.Fatal("recorded measurements were counted as verified web facts")
	}
	observation.Metrics.Fills++
	if packet.RecordedObservations.Metrics.Fills != 2 {
		t.Fatal("packet aliases the caller's mutable host artifact")
	}
	input.RecordedObservations = packet.RecordedObservations
	if _, err := ParseWithRecorded(recordedJSON(t, input), packet.RecordedObservations, now); err == nil {
		t.Fatal("model-supplied embedded artifact was accepted even when it matched the host")
	}
}

func TestRecordedPacketRejectsInvalidReferences(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Packet){
		"missing":          func(p *Packet) { p.RecordedEvidence = nil },
		"wrong digest":     func(p *Packet) { p.RecordedEvidence.ContentSHA256 = strings.Repeat("c", 64) },
		"uppercase digest": func(p *Packet) { p.RecordedEvidence.ContentSHA256 = strings.Repeat("A", 64) },
		"empty metrics":    func(p *Packet) { p.RecordedEvidence.MetricIDs = nil },
		"unknown metric":   func(p *Packet) { p.RecordedEvidence.MetricIDs = []string{"real_wallet_profit"} },
		"duplicate metric": func(p *Packet) { p.RecordedEvidence.MetricIDs = []string{"fills", "fills"} },
		"too many metrics": func(p *Packet) { p.RecordedEvidence.MetricIDs = append(p.RecordedEvidence.MetricIDs, "fills") },
	} {
		t.Run(name, func(t *testing.T) {
			input, observation := recordedPacketFixture(t, now)
			mutate(&input)
			if _, err := ParseWithRecorded(recordedJSON(t, input), &observation, now); err == nil {
				t.Fatal("invalid recorded reference was accepted")
			}
		})
	}
}

func TestRecordedPacketRejectsInvalidHostObservations(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*RecordedObservations){
		"wrong market": func(o *RecordedObservations) { o.Market = "JUP/USDC" },
		"future day": func(o *RecordedObservations) {
			o.ObservedFrom = o.ObservedFrom.AddDate(0, 0, 1)
			o.ObservedThrough = o.ObservedThrough.AddDate(0, 0, 1)
			o.Journal.Day = o.ObservedFrom.Format("2006-01-02")
		},
		"older day": func(o *RecordedObservations) {
			o.ObservedFrom = o.ObservedFrom.AddDate(0, 0, -1)
			o.ObservedThrough = o.ObservedThrough.AddDate(0, 0, -1)
			o.Journal.Day = o.ObservedFrom.Format("2006-01-02")
		},
		"low coverage":           func(o *RecordedObservations) { o.Metrics.ObservableBPS = 9499 },
		"excess coverage":        func(o *RecordedObservations) { o.Metrics.ObservableBPS = 10001 },
		"authorized":             func(o *RecordedObservations) { o.Authorized = true },
		"promotable":             func(o *RecordedObservations) { o.Promotable = true },
		"not paper":              func(o *RecordedObservations) { o.PaperOnly = false },
		"not advisory":           func(o *RecordedObservations) { o.AdvisoryOnly = false },
		"partial start":          func(o *RecordedObservations) { o.ObservedFrom = o.ObservedFrom.Add(time.Second) },
		"partial end":            func(o *RecordedObservations) { o.ObservedThrough = o.ObservedThrough.Add(-time.Second) },
		"too few records":        func(o *RecordedObservations) { o.Journal.Records = 1 },
		"signals exceed records": func(o *RecordedObservations) { o.Metrics.Signals = 101 },
		"fills exceed records":   func(o *RecordedObservations) { o.Metrics.Fills = 101 },
		"bad policy digest":      func(o *RecordedObservations) { o.PolicySHA256 = "bad" },
		"bad journal digest":     func(o *RecordedObservations) { o.Journal.ChainHeadSHA256 = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			input, observation := recordedPacketFixture(t, now)
			mutate(&observation)
			// Preserve valid hashes for market/date cases, proving rejection is
			// binding/freshness rather than merely an outdated digest.
			if name == "wrong market" || name == "future day" || name == "older day" {
				var err error
				observation, err = observation.Seal()
				if err != nil {
					t.Fatal(err)
				}
				input.RecordedEvidence.ContentSHA256 = observation.ContentSHA256
			} else if _, err := observation.Seal(); err == nil {
				t.Fatal("invalid observation envelope could be sealed")
			}
			if _, err := ParseWithRecorded(recordedJSON(t, input), &observation, now); err == nil {
				t.Fatal("invalid host observations were accepted")
			}
		})
	}
}

func TestRecordedPacketMidnightCannotRenewEvidence(t *testing.T) {
	before := time.Date(2026, 9, 5, 23, 59, 30, 0, time.UTC)
	input, observation := recordedPacketFixture(t, before)
	packet, err := ParseWithRecorded(recordedJSON(t, input), &observation, before)
	if err != nil {
		t.Fatal(err)
	}
	after := before.Add(time.Minute)
	if !after.Before(packet.ValidUntil) || packet.StatusAt(after).Current || packet.StatusAt(after).Actionable {
		t.Fatal("midnight did not expire recorded evidence independently of packet expiry")
	}
	if _, err := ParseWithRecorded(recordedJSON(t, input), &observation, after); err == nil {
		t.Fatal("a research run spanning midnight reused yesterday's artifact")
	}
	newInput, newObservation := recordedPacketFixture(t, after)
	// The new day artifact is current now, but was not current at the old
	// research creation time. It cannot be substituted into an ongoing run.
	newInput.CreatedAt = input.CreatedAt
	if _, err := ParseWithRecorded(recordedJSON(t, newInput), &newObservation, after); err == nil {
		t.Fatal("post-midnight artifact was rebound to a pre-midnight research run")
	}
}

func TestRecordedContextPreservesV1BytesAndHash(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	_, observation := recordedPacketFixture(t, now)
	data := recordedJSON(t, candidatePacket(now))
	legacy, err := Parse(data, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []*RecordedObservations{nil, &observation, {}} {
		got, err := ParseWithRecorded(data, host, now)
		if err != nil || got.ContentSHA256 != legacy.ContentSHA256 ||
			!bytes.Equal(recordedJSON(t, got), recordedJSON(t, legacy)) {
			t.Fatalf("optional host context changed v1 serialization: %v", err)
		}
	}
}

func TestRecordedCandidateStillRequiresVerifiedExternalFacts(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, status := range []string{FactSingleSource, FactUnverified} {
		t.Run(status, func(t *testing.T) {
			input, observation := recordedPacketFixture(t, now)
			fact := candidatePacket(now).VerifiedFacts[0]
			fact.Status = status
			fact.Sources = fact.Sources[:1]
			if status == FactUnverified {
				fact.Sources = nil
			}
			if err := validateFact(fact, input.CreatedAt, now); err != nil {
				t.Fatal(err)
			}
			input.VerifiedFacts = []Fact{fact}
			if _, err := ParseWithRecorded(recordedJSON(t, input), &observation, now); err == nil {
				t.Fatal("recorded observations bypassed external fact verification")
			}
		})
	}
}

func TestRecordedArchiveTamperAndResealAreNotProvenance(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	input, observation := recordedPacketFixture(t, now)
	packet, err := ParseWithRecorded(recordedJSON(t, input), &observation, now)
	if err != nil {
		t.Fatal(err)
	}
	stored := recordedJSON(t, packet)
	decoded, err := DecodeStored(stored)
	if err != nil || !reflect.DeepEqual(decoded, packet) {
		t.Fatalf("valid archive rejected: %v", err)
	}
	packet.RecordedObservations.Metrics.Fills++
	if packet.RecordedObservations.Validate() == nil {
		t.Fatal("tampered observation digest accepted")
	}
	if _, err := DecodeStored(recordedJSON(t, packet)); err == nil {
		t.Fatal("tampered archive accepted")
	}
	resealed, err := packet.RecordedObservations.Seal()
	if err != nil {
		t.Fatal(err)
	}
	packet.RecordedObservations = &resealed
	packet.RecordedEvidence.ContentSHA256 = resealed.ContentSHA256
	if _, err := DecodeStored(recordedJSON(t, packet)); err == nil {
		t.Fatal("inner reseal bypassed packet digest")
	}
	packet.ContentSHA256, err = packet.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStored(recordedJSON(t, packet)); err != nil {
		t.Fatalf("fully resealed archive should be structurally valid, not provenance proof: %v", err)
	}
	input.RecordedEvidence.ContentSHA256 = resealed.ContentSHA256
	if _, err := ParseWithRecorded(recordedJSON(t, input), &observation, now); err == nil {
		t.Fatal("resealed claim overrode the independently supplied host artifact")
	}
}
