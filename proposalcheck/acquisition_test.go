package proposalcheck

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
