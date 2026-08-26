package signer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestAuthorizeAndSignDurablyRecoversExactRequest(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()

	first, err := AuthorizeAndSign(policy, privateKey, request, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalentSignerResponses(t, first, second)

	records := authorizationRecords(t, policy.AuthorizationLedgerPath)
	if len(records) != 2 {
		t.Fatalf("ledger records = %d, want header plus one reservation", len(records))
	}
	if records[1].ActionID != request.ActionID ||
		records[1].Type != authorizationReserveType {
		t.Fatalf("reservation record = %+v", records[1])
	}
	var reservation authorizationReservation
	if err := strictjson.Decode(records[1].Payload, &reservation); err != nil {
		t.Fatal(err)
	}
	if reservation.MessageSHA256 != first.MessageSHA256 ||
		reservation.AmountLamports != 42 ||
		reservation.FeeLamports != request.FeeLamports ||
		reservation.DebitLamports != 42+request.FeeLamports {
		t.Fatalf("reservation = %+v", reservation)
	}
}

func TestAuthorizeRejectsActionIDReuseWithDifferentMessage(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatal(err)
	}
	changed := request
	message, err := solana.BuildTransferMessage(
		policy.Source,
		policy.Destination,
		request.RecentBlockhash,
		43,
	)
	if err != nil {
		t.Fatal(err)
	}
	changed.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	grantSignerRequest(t, policy, &changed, now)
	if _, err := AuthorizeAndSign(policy, privateKey, changed, now); err == nil ||
		!strings.Contains(err.Error(), "different request") {
		t.Fatalf("changed action error = %v", err)
	}
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 2 {
		t.Fatalf("changed action appended a record: %d", got)
	}
}

func TestAuthorizationLedgerCapsCumulativeDailyDebit(t *testing.T) {
	policy, privateKey, first := signerFixture(t)
	policy.DailyDebitCapLamports = 10_000
	firstNow := time.Unix(first.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, first, firstNow); err != nil {
		t.Fatal(err)
	}

	second := first
	second.ScheduleWindowStartUnix += int64(policy.ScheduleWindowSeconds)
	second.ScheduleWindowEndUnix += int64(policy.ScheduleWindowSeconds)
	actionID, err := agent.ComputeActionID(
		policy.ProfileFingerprint,
		second.ScheduleWindowStartUnix,
	)
	if err != nil {
		t.Fatal(err)
	}
	second.ActionID = actionID
	secondNow := time.Unix(second.ScheduleWindowStartUnix+1, 0).UTC()
	grantSignerRequest(t, policy, &second, secondNow)
	if _, err := AuthorizeAndSign(policy, privateKey, second, secondNow); err == nil ||
		!strings.Contains(err.Error(), "daily debit cap") {
		t.Fatalf("daily cap error = %v", err)
	}
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 2 {
		t.Fatalf("daily-cap rejection appended a record: %d", got)
	}
}

func TestAuthorizationLedgerKeepsCapsAcrossRotationMarkers(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	policy.DailyDebitCapLamports = 2 * (42 + request.FeeLamports)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatal(err)
	}
	records := authorizationRecords(t, policy.AuthorizationLedgerPath)
	second := records[1]
	second.ActionID = "second-action-after-rotation"
	records = append(records, journal.Record{
		Type: journal.EventRotated,
	}, second)

	ledger := &authorizationLedger{
		policy: policy, reservations: make(map[string]authorizationReservation),
		dailyDebits: make(map[int64]uint64),
	}
	policyHash, err := authorizationPolicyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.load(records, policyHash); err != nil {
		t.Fatal(err)
	}
	day := now.Unix() - now.Unix()%secondsPerDay
	if got := ledger.dailyDebits[day]; got != policy.DailyDebitCapLamports {
		t.Fatalf("daily debit after rotation = %d, want %d", got, policy.DailyDebitCapLamports)
	}
	if len(ledger.reservations) != 2 {
		t.Fatalf("reservations after rotation = %d, want 2", len(ledger.reservations))
	}
}

func TestAuthorizationReservationSurvivesCrashBeforeResponse(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	ledger, err := openAuthorizationLedger(policy, now)
	if err != nil {
		t.Fatal(err)
	}
	response, err := signAt(policy, privateKey, request, now)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reservationFor(policy, request, response, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.reserve(now, request.ActionID, reservation); err != nil {
		t.Fatal(err)
	}
	if err := ledger.close(); err != nil {
		t.Fatal(err)
	}

	grantSignerRequest(t, policy, &request, now.Add(time.Second))
	recovered, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalentSignerResponses(t, response, recovered)
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 2 {
		t.Fatalf("crash recovery double-reserved: %d", got)
	}
}

func assertEquivalentSignerResponses(t *testing.T, first, second Response) {
	t.Helper()
	firstEnvelope := first.SealedTransaction
	secondEnvelope := second.SealedTransaction
	first.SealedTransaction = sealedtx.Envelope{}
	second.SealedTransaction = sealedtx.Envelope{}
	if first != second {
		t.Fatalf("recovered response identity changed: %+v / %+v", first, second)
	}
	privateKey, _ := signerTestSubmitterKeys(t)
	firstTransaction, err := sealedtx.Open(privateKey, firstEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	secondTransaction, err := sealedtx.Open(privateKey, secondEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstTransaction, secondTransaction) {
		t.Fatal("recovered response contains a different signed transaction")
	}
}

func TestAuthorizationLedgerRecoversTornTailAndRejectsTamper(t *testing.T) {
	t.Run("torn tail", func(t *testing.T) {
		policy, privateKey, request := signerFixture(t)
		now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
		if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(policy.AuthorizationLedgerPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"sequence":3`); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(policy.AuthorizationLedgerPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`{"sequence":3`)) {
			t.Fatal("torn ledger tail was not removed")
		}
	})

	t.Run("hash-chain tamper", func(t *testing.T) {
		policy, privateKey, request := signerFixture(t)
		now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
		if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(policy.AuthorizationLedgerPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
		var record map[string]any
		if err := json.Unmarshal(lines[1], &record); err != nil {
			t.Fatal(err)
		}
		payload := record["payload"].(map[string]any)
		payload["debit_lamports"] = float64(1)
		lines[1], err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		tampered := append(bytes.Join(lines, []byte{'\n'}), '\n')
		if err := os.WriteFile(policy.AuthorizationLedgerPath, tampered, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err == nil {
			t.Fatal("tampered authorization ledger was accepted")
		}
	})

	t.Run("complete truncation", func(t *testing.T) {
		policy, privateKey, request := signerFixture(t)
		now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
		if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(policy.AuthorizationLedgerPath, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err == nil {
			t.Fatal("truncated authorization ledger was reinitialized")
		}
	})
}

func TestAuthorizationLedgerRejectsWrongTimeUnsafePathAndChangedPolicy(t *testing.T) {
	t.Run("trusted time outside window", func(t *testing.T) {
		for _, delta := range []int64{-1, int64(3_600)} {
			policy, privateKey, request := signerFixture(t)
			now := time.Unix(request.ScheduleWindowStartUnix+delta, 0).UTC()
			if _, err := AuthorizeAndSign(policy, privateKey, request, now); err == nil {
				t.Fatalf("time %s was accepted", now)
			}
			if _, err := os.Lstat(policy.AuthorizationLedgerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("out-of-window request created a ledger: %v", err)
			}
		}
	})

	t.Run("symlink ledger", func(t *testing.T) {
		policy, privateKey, request := signerFixture(t)
		target := filepath.Join(filepath.Dir(policy.AuthorizationLedgerPath), "target.jsonl")
		if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(filepath.Dir(policy.AuthorizationLedgerPath), "link.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		policy.AuthorizationLedgerPath = link
		now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
		if _, err := AuthorizeAndSign(policy, privateKey, request, now); err == nil {
			t.Fatal("symlink authorization ledger was accepted")
		}
	})

	t.Run("changed policy binding", func(t *testing.T) {
		policy, privateKey, request := signerFixture(t)
		now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
		if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
			t.Fatal(err)
		}
		policy.DailyDebitCapLamports++
		if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err == nil {
			t.Fatal("ledger created under another policy binding was accepted")
		}
	})
}

func TestAuthorizationLedgerRejectsPreviousSchema(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	policyHash, err := authorizationPolicyHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(policy.AuthorizationLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := store.Append(now, authorizationHeaderType, "", authorizationHeader{
		Version: authorizationLedgerVersion - 1, PolicySHA256: policyHash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err == nil {
		t.Fatal("authorization ledger from the previous schema was accepted")
	} else if strings.Contains(err.Error(), "a cap was edited") {
		t.Fatalf("previous ledger schema was misreported as a cap edit: %v", err)
	}
}

func TestAuthorizationLedgerRejectsConcurrentWriter(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatal(err)
	}
	held, err := journal.Open(policy.AuthorizationLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err == nil ||
		!strings.Contains(err.Error(), "already in use") {
		t.Fatalf("concurrent writer error = %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentExactAuthorizationsReserveOnce(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := AuthorizeAndSign(policy, privateKey, request, now)
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	var successes int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case strings.Contains(err.Error(), "already in use"):
		default:
			t.Errorf("concurrent authorization error = %v", err)
		}
	}
	if successes == 0 {
		t.Fatal("no concurrent authorization succeeded")
	}
	if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 2 {
		t.Fatalf("concurrent exact requests created %d records", got)
	}
}

func TestPolicyRequiresDurableAuthorizationFields(t *testing.T) {
	policy, _, _ := signerFixture(t)
	policy.DailyDebitCapLamports = 0
	if err := policy.validateAuthorization(); err == nil {
		t.Fatal("zero daily debit cap was accepted")
	}
	policy, _, _ = signerFixture(t)
	policy.AuthorizationLedgerPath = "relative.jsonl"
	if err := policy.validateAuthorization(); err == nil {
		t.Fatal("relative authorization ledger path was accepted")
	}
}

func authorizationRecords(t *testing.T, path string) []journal.Record {
	t.Helper()
	store, err := journal.OpenRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	records := store.Records()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return records
}

// A crash between ledger creation and the header append leaves a zero-length
// file. It must stay rejected — an attacker can truncate a real ledger to the
// same state — but the operator needs to be told which file to inspect.
func TestAuthorizationLedgerEmptyFileReportsRecoverableState(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	if err := os.WriteFile(policy.AuthorizationLedgerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()

	_, err := AuthorizeAndSign(policy, privateKey, request, now)
	if err == nil {
		t.Fatal("empty authorization ledger was accepted")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error %q does not identify the empty ledger; an operator cannot tell this from a corrupt one", err)
	}
}
