package proposalcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type acquisitionBuilder struct {
	fakeBuilder
	delay time.Duration
}

func (b *acquisitionBuilder) Build(ctx context.Context, request jupiterquote.Request) (jupiterquote.BuildResult, error) {
	time.Sleep(b.delay)
	return b.fakeBuilder.Build(ctx, request)
}

type slowAcquisitionEvidence struct {
	*fakeEvidence
	delay time.Duration
}

func (e slowAcquisitionEvidence) NodeBlockHeight(ctx context.Context, slot uint64) (uint64, error) {
	time.Sleep(e.delay)
	return e.fakeEvidence.NodeBlockHeight(ctx, slot)
}

func acquireFixture(t *testing.T, path string, received time.Time, maxAge, buildDelay, checkDelay time.Duration) (Result, error) {
	t.Helper()
	policy, request, proposal := proposalFixture()
	proposal.Quote.ReceivedAt = received
	proposal.Quote.ResponseSHA256 = strings.Repeat("a", 64)
	return CheckAndRecordAcquisition(t.Context(), path, maxAge,
		&acquisitionBuilder{fakeBuilder: fakeBuilder{result: proposal}, delay: buildDelay},
		slowAcquisitionEvidence{fakeEvidence: &fakeEvidence{
			fee: txflow.FeeEvidence{Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100},
			simulations: []txflow.LegacySimulationEvidence{
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: strings.Repeat("0", 64)},
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: strings.Repeat("0", 64)},
			},
		}, delay: checkDelay}, primarySlot(100), secondarySlot(100),
		"primary-provider", "secondary-provider", archiveProbeSignature(), policy, request)
}

func TestAcquisitionUsesCompletionClockAndNeverRenews(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "acquisition.jsonl")
		start := time.Now().UTC()
		// The response arrives after the call starts, then checks consume time.
		received := start.Add(time.Second)
		result, err := acquireFixture(t, path, received, time.Minute, time.Second, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := result.Candidate()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := EncodeCandidate(candidate)
		if err != nil || bytes.Contains(encoded, []byte("received_at")) || bytes.Contains(encoded, []byte("response_sha256")) {
			t.Fatalf("portable candidate changed: %v", err)
		}
		portable, err := DecodeCandidate(encoded)
		if err != nil || !portable.Quote.ReceivedAt.IsZero() || portable.Quote.ResponseSHA256 != "" {
			t.Fatalf("portable candidate manufactured provenance: %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := acquireFixture(t, path, received, time.Minute, 0, 0); err != nil {
			t.Fatalf("exact acquisition repeat: %v", err)
		}
		if _, err := acquireFixture(t, path, time.Now().UTC(), time.Minute, 0, 0); err == nil {
			t.Fatal("retimestamping renewed original acquisition")
		}
		if _, err := VerifyAcquisition(path, portable, received.Add(time.Minute), time.Minute); err != nil {
			t.Fatalf("exact age boundary: %v", err)
		}
		retained, err := ReadAcquiredCandidate(path, received.Add(time.Minute), time.Minute)
		if err != nil || !reflect.DeepEqual(retained, portable) {
			t.Fatalf("retained candidate differs after reopen: %+v, %v", retained, err)
		}
		if !retained.Quote.ReceivedAt.IsZero() || retained.Quote.ResponseSHA256 != "" {
			t.Fatal("retained portable candidate manufactured receipt metadata")
		}
		acquired, err := ReadAcquisition(path, received.Add(time.Minute), time.Minute)
		if err != nil || !reflect.DeepEqual(acquired.Candidate, retained) || !acquired.ReceivedAt.Equal(received) {
			t.Fatalf("original acquisition receipt was not preserved: %+v, %v", acquired, err)
		}
		digest, err := VerifyAcquisition(path, retained, received.Add(time.Minute), time.Minute)
		if err != nil || acquired.SHA256 != digest {
			t.Fatalf("acquisition reader returned a different receipt identity: %v", err)
		}
		if acquired, err := ReadAcquisition(path, received.Add(time.Minute+time.Nanosecond), time.Minute); err == nil || acquired.SHA256 != "" || !acquired.ReceivedAt.IsZero() {
			t.Fatal("expired acquisition returned usable receipt metadata")
		}
		if _, err := ReadAcquiredCandidate(path, received.Add(time.Minute+time.Nanosecond), time.Minute); err == nil {
			t.Fatal("retained candidate renewed expired acquisition")
		}
		if _, err := ReadAcquiredCandidate(path, received.Add(time.Minute), 2*time.Minute); err == nil {
			t.Fatal("reader changed original maximum age")
		}
		if _, err := VerifyAcquisition(path, portable, received.Add(time.Minute+time.Nanosecond), time.Minute); err == nil {
			t.Fatal("expired receipt accepted after reopen")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("receipt was rewritten: %v", err)
		}
	})
}

func TestAcquisitionRejectsMissingFutureAndSlowExpiredProvenance(t *testing.T) {
	for _, name := range []string{"missing", "future", "slow expired", "zero age", "negative age"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "acquisition.jsonl")
				received, age, delay := time.Now().UTC(), time.Second, time.Duration(0)
				switch name {
				case "missing":
					received = time.Time{}
				case "future":
					received = received.Add(time.Nanosecond)
				case "slow expired":
					delay = time.Second + time.Nanosecond
				case "zero age":
					age = 0
				case "negative age":
					age = -1
				}
				result, err := acquireFixture(t, path, received, age, 0, delay)
				if err == nil || len(result.Message()) != 0 {
					t.Fatalf("invalid provenance returned checked material: %v", err)
				}
				if records, err := journal.ReadRecords(path); err == nil && len(records) != 0 {
					t.Fatal("invalid provenance appended receipt")
				}
			})
		})
	}
}

func TestAcquisitionMissingReadLockAndFailedAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acquisition.jsonl")
	candidate := candidateFixture(t)
	if _, err := VerifyAcquisition(path, candidate, time.Now(), time.Minute); err == nil {
		t.Fatal("imported candidate minted acquisition")
	}
	if _, err := ReadAcquiredCandidate(path, time.Now(), time.Minute); err == nil {
		t.Fatal("missing receipt reconstructed a candidate")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verification created receipt: %v", err)
	}
	store, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireFixture(t, path, time.Now(), time.Minute, 0, 0); !errors.Is(err, journal.ErrLocked) {
		t.Fatalf("concurrent acquisition was not blocked: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := appendAcquisition(store, acquisitionReceipt{}, time.Now()); err == nil {
		t.Fatal("failed append accepted")
	}
}

func TestAcquisitionHistoricalVerificationPreservesBytesAndDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical.jsonl")
	candidate := candidateFixture(t)
	digest, err := acquisitionCandidateHash(candidate)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	receipt := acquisitionReceipt{CandidateSHA256: digest, ResponseSHA256: strings.Repeat("a", 64), ReceivedAt: now, MaxAge: time.Minute}
	payload, err := json.Marshal(receipt)
	if err != nil || bytes.Contains(payload, []byte(`"candidate":`)) {
		t.Fatalf("legacy encoding changed: %s, %v", payload, err)
	}
	store, err := journal.OpenStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendAcquisition(store, receipt, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(append([]byte(acquisitionEvent+"\x00"), payload...))
	got, err := VerifyAcquisition(path, candidate, now, time.Minute)
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("legacy verification digest = %q, %v", got, err)
	}
	if _, err := ReadAcquiredCandidate(path, now, time.Minute); err == nil || err.Error() != "acquisition candidate was not retained" {
		t.Fatalf("historical receipt reconstructed candidate: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("historical receipt was rewritten")
	}
}

func TestAcquisitionRetainedCandidateTamperingAndTornStateFailClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "acquisition.jsonl")
		now := time.Now().UTC()
		result, err := acquireFixture(t, path, now, time.Minute, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := result.Candidate()
		if err != nil {
			t.Fatal(err)
		}
		_, receipt, err := readAcquisition(path)
		if err != nil || receipt.Candidate == nil {
			t.Fatalf("receipt lacks atomic candidate: %v", err)
		}
		receipt.Candidate.LastValidBlockHeight++
		other := filepath.Join(t.TempDir(), "substituted.jsonl")
		store, err := journal.OpenStrict(other)
		if err != nil {
			t.Fatal(err)
		}
		if err := appendAcquisition(store, receipt, now); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadAcquiredCandidate(other, now, time.Minute); err == nil {
			t.Fatal("substituted retained candidate was accepted")
		}
		if _, err := VerifyAcquisition(other, candidate, now, time.Minute); err == nil {
			t.Fatal("external original candidate hid substituted retained candidate")
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		torn := append(before, '{')
		if err := os.WriteFile(path, torn, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadAcquiredCandidate(path, now, time.Minute); err == nil {
			t.Fatal("torn receipt read succeeded")
		}
		if _, err := acquireFixture(t, path, now, time.Minute, 0, 0); err == nil {
			t.Fatal("new acquisition repaired torn evidence")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(torn, after) {
			t.Fatal("torn acquisition evidence was changed")
		}
	})
}
