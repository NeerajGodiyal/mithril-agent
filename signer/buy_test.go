package signer

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func buySignerFixture(t *testing.T) (Policy, ed25519.PrivateKey, Request) {
	t.Helper()
	privateKey := signerTestKey("buy-source")
	owner := solana.Encode(privateKey.Public().(ed25519.PublicKey))
	inputAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	policyRoute := orcaswap.BuyPolicyV2{
		Owner: owner, Pool: orcaswap.DevnetPool,
		TokenMintA: orcaswap.WrappedSOLMint, TokenMintB: orcaswap.DevnetUSDCMint,
		InputTokenAccount:   inputAccount,
		TokenVaultA:         "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:         "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:              "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:         orcaswap.WhirlpoolProgramData,
		UpgradeAuthority:    orcaswap.WhirlpoolUpgradeAuth,
		DeploymentSlot:      orcaswap.WhirlpoolDeploySlot,
		MaxInputTokenAmount: 1_000, MinOutputLamports: 45_348,
		MaxSlippageBPS: 100, MaxTemporaryRentLamports: 3_000_000,
	}
	message, err := solana.BuildLegacyMessage(
		owner,
		solana.Encode(bytes.Repeat([]byte{7}, 32)),
		buySignerInstructions(t, policyRoute),
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintHash := sha256.Sum256([]byte("buy-profile"))
	fingerprint := hex.EncodeToString(fingerprintHash[:])
	anchor := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).Unix()
	start := anchor + 3_600
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := Policy{
		Cluster: "devnet", Profile: orcaswap.BuyProfileName,
		ProfileVersion: orcaswap.BuyProfileVersion, ProfileFingerprint: fingerprint,
		Source: owner, MaxInputTokenAmount: 1_000, MaxFeeLamports: 5_000,
		DailyInputTokenCap: 1_000, DailyNativeFeeCapLamports: 5_000,
		AuthorizationLedgerPath: filepath.Join(ledgerDir, "buy-authorization.jsonl"),
		ScheduleWindowSeconds:   3_600, ScheduleAnchorUnix: anchor,
		MaxBlockHeightWindow: 200, OrcaBuy: &policyRoute,
	}
	authority := signerTestKey("risk-authority")
	policy.RiskAuthorityKeyID = "test-risk-authority"
	policy.RiskAuthorityPublicKey, err = riskgrant.PublicKeyHex(authority)
	if err != nil {
		t.Fatal(err)
	}
	_, policy.SubmitterPublicKey = signerTestSubmitterKeys(t)
	actionID, err := orcaswap.ComputeBuyActionID(fingerprint, start)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Domain: orcaswap.BuyRequestDomain, Cluster: "devnet",
		Profile: orcaswap.BuyProfileName, ProfileVersion: orcaswap.BuyProfileVersion,
		ProfileFingerprint: fingerprint, ActionID: actionID,
		ScheduleWindowStartUnix: start, ScheduleWindowEndUnix: start + 3_600,
		MessageBase64:        base64.StdEncoding.EncodeToString(message),
		BlockhashContextSlot: 90, FeeLamports: 5_000, FeeMinContextSlot: 90,
		PrimaryFeeContextSlot: 90, SecondaryFeeContextSlot: 91,
		RecentBlockhash:     solana.Encode(bytes.Repeat([]byte{7}, 32)),
		ObservedBlockHeight: 100, LastValidBlockHeight: 250,
	}
	grantSignerRequest(t, policy, &request, time.Unix(start+1, 0).UTC())
	return policy, privateKey, request
}

func TestBuySignerSeparatesTokenAndNativeAuthorization(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	validated, err := ValidateRequest(policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if validated.InputMint != orcaswap.DevnetUSDCMint || validated.InputAmount != 1_000 ||
		validated.OutputMint != orcaswap.WrappedSOLMint || validated.MinimumOutput != 45_348 ||
		validated.NativeDebitLamports != request.FeeLamports ||
		validated.TemporaryRentLamports != 2_039_280 || validated.AmountLamports != 0 {
		t.Fatalf("validated buy request = %+v", validated)
	}
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	response, err := AuthorizeAndSign(policy, privateKey, request, now)
	if err != nil {
		t.Fatal(err)
	}
	if response.Signature == "" || response.BlockhashContextSlot != request.BlockhashContextSlot ||
		response.SealedTransaction.Metadata.BlockhashContextSlot != request.BlockhashContextSlot {
		t.Fatal("buy signer response is not bound to its request")
	}
	store, err := journal.Open(policy.AuthorizationLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	records := store.Records()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("buy authorization records = %d, want 2", len(records))
	}
	var header authorizationHeader
	if err := strictjson.Decode(records[0].Payload, &header); err != nil ||
		header.Version != buyAuthorizationLedgerVersion {
		t.Fatalf("buy authorization header = %+v, %v", header, err)
	}
	var reservation buyAuthorizationReservation
	if err := strictjson.Decode(records[1].Payload, &reservation); err != nil ||
		reservation.InputAmount != 1_000 || reservation.FeeLamports != 5_000 ||
		reservation.TemporaryRentLamports != 2_039_280 {
		t.Fatalf("buy authorization reservation = %+v, %v", reservation, err)
	}
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("idempotent buy authorization = %v", err)
	}
}

func TestBuySignerDailyCapsSurviveReopen(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatal(err)
	}
	request.ScheduleWindowStartUnix += 3_600
	request.ScheduleWindowEndUnix += 3_600
	request.ActionID, _ = orcaswap.ComputeBuyActionID(
		policy.ProfileFingerprint, request.ScheduleWindowStartUnix,
	)
	grantSignerRequest(t, policy, &request, now.Add(3_600*time.Second))
	if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(3_600*time.Second)); err == nil {
		t.Fatal("reopened buy authorization ledger exceeded its daily caps")
	}
}

func TestBuyAuthorizationReservationSurvivesCrashBeforeResponse(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	ledger, err := openBuyAuthorizationLedger(policy, now)
	if err != nil {
		t.Fatal(err)
	}
	response, err := signAt(policy, privateKey, request, now)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := buyReservationFor(policy, request, response, now)
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
		t.Fatalf("buy crash recovery created %d records", got)
	}
}

func TestBuyAuthorizationRejectsSemanticDriftForReservedAction(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	policy.DailyNativeFeeCapLamports *= 2
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatal(err)
	}
	request.FeeLamports--
	grantSignerRequest(t, policy, &request, now.Add(time.Second))
	if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err == nil ||
		!strings.Contains(err.Error(), "different request") {
		t.Fatalf("reserved-action drift error = %v", err)
	}
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 2 {
		t.Fatalf("semantic drift appended a record: %d", got)
	}
}

func TestBuyAuthorizationCapsResetAtUTCDayBoundary(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
	day := time.Unix(request.ScheduleWindowStartUnix, 0).UTC().Truncate(24 * time.Hour)
	policy.ScheduleAnchorUnix = day.Unix()
	request.ScheduleWindowStartUnix = day.Add(23 * time.Hour).Unix()
	request.ScheduleWindowEndUnix = request.ScheduleWindowStartUnix + 3_600
	request.ActionID, _ = orcaswap.ComputeBuyActionID(
		policy.ProfileFingerprint, request.ScheduleWindowStartUnix,
	)
	firstNow := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	grantSignerRequest(t, policy, &request, firstNow)
	if _, err := AuthorizeAndSign(policy, privateKey, request, firstNow); err != nil {
		t.Fatal(err)
	}
	request.ScheduleWindowStartUnix += 3_600
	request.ScheduleWindowEndUnix += 3_600
	request.ActionID, _ = orcaswap.ComputeBuyActionID(
		policy.ProfileFingerprint, request.ScheduleWindowStartUnix,
	)
	secondNow := firstNow.Add(time.Hour)
	grantSignerRequest(t, policy, &request, secondNow)
	if _, err := AuthorizeAndSign(policy, privateKey, request, secondNow); err != nil {
		t.Fatalf("first authorization after UTC rollover = %v", err)
	}
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 3 {
		t.Fatalf("UTC rollover records = %d, want 3", got)
	}
}

func TestBuyAuthorizationLedgerRecoversTornTail(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
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
		t.Fatal("torn buy-ledger tail was not removed")
	}
}

func TestBuyAuthorizationLedgerRejectsPreviousSchema(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
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
		Version: buyAuthorizationLedgerVersion - 1, PolicySHA256: policyHash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err == nil {
		t.Fatal("buy authorization ledger from the previous schema was accepted")
	}
}

func TestConcurrentExactBuyAuthorizationsReserveOnce(t *testing.T) {
	policy, privateKey, request := buySignerFixture(t)
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
			t.Errorf("concurrent buy authorization error = %v", err)
		}
	}
	if successes == 0 {
		t.Fatal("no concurrent buy authorization succeeded")
	}
	if _, err := AuthorizeAndSign(policy, privateKey, request, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(authorizationRecords(t, policy.AuthorizationLedgerPath)); got != 2 {
		t.Fatalf("concurrent buy requests created %d records", got)
	}
}

func buySignerInstructions(t *testing.T, policy orcaswap.BuyPolicyV2) []solana.Instruction {
	t.Helper()
	ownerKey, err := solana.Decode32(policy.Owner)
	if err != nil {
		t.Fatal(err)
	}
	tokenProgram, err := solana.Decode32(orcaswap.TokenProgram)
	if err != nil {
		t.Fatal(err)
	}
	seed := []byte("1785688960889")
	hash := sha256.New()
	_, _ = hash.Write(ownerKey[:])
	_, _ = hash.Write(seed)
	_, _ = hash.Write(tokenProgram[:])
	temporary := solana.Encode(hash.Sum(nil))
	create := make([]byte, 0, 105)
	create = binary.LittleEndian.AppendUint32(create, 3)
	create = append(create, ownerKey[:]...)
	create = binary.LittleEndian.AppendUint64(create, uint64(len(seed)))
	create = append(create, seed...)
	create = binary.LittleEndian.AppendUint64(create, 2_039_280)
	create = binary.LittleEndian.AppendUint64(create, 165)
	create = append(create, tokenProgram[:]...)
	initialize := append([]byte{18}, ownerKey[:]...)
	swap := make([]byte, 49)
	copy(swap, []byte{43, 4, 237, 11, 26, 201, 30, 98})
	binary.LittleEndian.PutUint64(swap[8:16], 1_000)
	binary.LittleEndian.PutUint64(swap[16:24], 45_348)
	copy(swap[40:], []byte{1, 0, 1, 1, 0, 0, 0, 6, 2})
	return []solana.Instruction{
		{Program: orcaswap.SystemProgram, Accounts: []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: temporary, Writable: true}, {Address: policy.Owner, Signer: true},
		}, Data: create},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: temporary, Writable: true}, {Address: policy.TokenMintA},
		}, Data: initialize},
		{Program: orcaswap.WhirlpoolProgram, Accounts: []solana.AccountMeta{
			{Address: orcaswap.TokenProgram}, {Address: orcaswap.TokenProgram},
			{Address: orcaswap.MemoProgram}, {Address: policy.Owner, Signer: true},
			{Address: policy.Pool, Writable: true}, {Address: policy.TokenMintA},
			{Address: policy.TokenMintB}, {Address: temporary, Writable: true},
			{Address: policy.TokenVaultA, Writable: true},
			{Address: policy.InputTokenAccount, Writable: true},
			{Address: policy.TokenVaultB, Writable: true},
			{Address: "7knZZ461yySGbSEHeBUwEpg3VtAkQy8B9tp78RGgyUHE", Writable: true},
			{Address: "CpoSFo3ajrizueggtJr2ZjvYgdtkgugXtvhqcwkyCkKP", Writable: true},
			{Address: "9iGzy4mQtJadZDuH8seBFQGiqcb6wyp2KW4M6NKHvsAW", Writable: true},
			{Address: policy.Oracle, Writable: true},
			{Address: "3aBJJLAR3QxGcGsesNXeW3f64Rv3TckF7EQ6sXtAuvGM", Writable: true},
			{Address: "A1vrG379E5ttoaWmyQBiunsMdyrpoUp7mSQwu8DgLcip", Writable: true},
		}, Data: swap},
		{Program: orcaswap.TokenProgram, Accounts: []solana.AccountMeta{
			{Address: temporary, Writable: true}, {Address: policy.Owner, Writable: true},
			{Address: policy.Owner, Signer: true},
		}, Data: []byte{9}},
	}
}
