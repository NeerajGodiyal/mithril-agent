package turnkeycustody

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	turnkey "github.com/tkhq/go-sdk/v2"
)

const (
	liveQualificationEnabled = "MITHRIL_AGENT_TURNKEY_QUALIFY"
	liveIdentityEnabled      = "MITHRIL_AGENT_TURNKEY_IDENTITY_QUALIFY"
	livePrivateKeyFile       = "MITHRIL_AGENT_TURNKEY_API_PRIVATE_KEY_FILE"
	livePublicKey            = "MITHRIL_AGENT_TURNKEY_API_PUBLIC_KEY"
	liveOrganizationID       = "MITHRIL_AGENT_TURNKEY_ORGANIZATION_ID"
	liveSignWith             = "MITHRIL_AGENT_TURNKEY_SIGN_WITH"
	liveSolanaAddress        = "MITHRIL_AGENT_TURNKEY_SOLANA_ADDRESS"

	qualificationDestination = "6HfHQs4q4hH3tXRPmbyVGYpHq1Zbw3xJY6R1dSfeoyNX"
	mutationDestination      = "7fVHLBXgK8QzPp6VVEj8c2X3YJ6FtwFhE3g6dQxKFMpS"
	systemProgram            = "11111111111111111111111111111111"
)

// TestLiveTurnkeyIdentityQualification authenticates the API key and verifies
// that the configured Turnkey signing resource owns the expected Solana
// address. It creates no signing activity and submits no transaction.
func TestLiveTurnkeyIdentityQualification(t *testing.T) {
	if os.Getenv(liveIdentityEnabled) != "1" {
		t.Skip("set MITHRIL_AGENT_TURNKEY_IDENTITY_QUALIFY=1 for read-only Turnkey identity qualification")
	}
	organizationID := os.Getenv(liveOrganizationID)
	signWith := os.Getenv(liveSignWith)
	source := os.Getenv(liveSolanaAddress)
	if organizationID == "" || signWith == "" || source == "" {
		t.Fatal("Turnkey organization, signing resource, and Solana address are required")
	}
	verifyLiveSigningAddress(
		t, verifiedLiveAPIKeyStamper(t), organizationID, signWith, source,
	)
}

// TestLiveTurnkeyPolicyQualification is deliberately a test, not an installed
// command: it exercises the real transaction-only custody adapter while
// remaining unreachable from every service and submission path. The wallet
// must be unfunded. The matching Turnkey policy is documented in OPERATIONS.md.
func TestLiveTurnkeyPolicyQualification(t *testing.T) {
	if os.Getenv(liveQualificationEnabled) != "1" {
		t.Skip("set MITHRIL_AGENT_TURNKEY_QUALIFY=1 for the unfunded Turnkey policy qualification")
	}
	organizationID := os.Getenv(liveOrganizationID)
	signWith := os.Getenv(liveSignWith)
	source := os.Getenv(liveSolanaAddress)
	if organizationID == "" || signWith == "" || source == "" {
		t.Fatal("Turnkey organization, signing resource, and Solana address are required")
	}
	stamper := verifiedLiveAPIKeyStamper(t)
	verifyLiveSigningAddress(t, stamper, organizationID, signWith, source)
	custody, err := newWithStamper(stamper, Config{OrganizationID: organizationID, SignWith: signWith})
	if err != nil {
		t.Fatal(err)
	}

	allowed := qualificationTransaction(t, source, qualificationDestination, 1, 1)
	request := liveCustodyRequest(allowed, time.Now().UTC())
	signed, err := custody.Sign(t.Context(), request)
	if err != nil {
		t.Fatalf("exact qualification transaction was refused: %v", err)
	}
	decoded, err := solana.DecodeSignedV0Transaction(signed, nil)
	if err != nil {
		t.Fatalf("Turnkey returned an invalid signed transaction: %v", err)
	}
	if !bytes.Equal(decoded.Message.Raw, allowed[65:]) {
		t.Fatal("Turnkey changed the qualification transaction message")
	}

	retry, err := custody.Sign(t.Context(), request)
	if err != nil {
		t.Fatalf("exact Turnkey activity retry failed: %v", err)
	}
	if !bytes.Equal(retry, signed) {
		t.Fatal("exact Turnkey activity retry returned different transaction bytes")
	}

	for name, transaction := range map[string][]byte{
		"amount":      qualificationTransaction(t, source, qualificationDestination, 2, 1),
		"recipient":   qualificationTransaction(t, source, mutationDestination, 1, 1),
		"instruction": qualificationTransaction(t, source, qualificationDestination, 1, 2),
	} {
		t.Run("reject_"+name+"_mutation", func(t *testing.T) {
			if err := verifyLivePolicyRejection(
				t.Context(), custody, transaction, request, signed, time.Now().UTC(),
			); err != nil {
				t.Fatalf("Turnkey %s mutation check failed: %v", name, err)
			}
		})
	}
}

// verifyLivePolicyRejection pairs every negative policy probe with the exact
// known-good idempotent activity. Otherwise a provider outage would look like
// successful policy enforcement.
func verifyLivePolicyRejection(
	ctx context.Context,
	custody *Signer,
	mutation []byte,
	control signer.TransactionCustodyRequest,
	expected []byte,
	now time.Time,
) error {
	if _, err := custody.Sign(ctx, liveCustodyRequest(mutation, now)); err == nil {
		return errors.New("policy accepted the mutation")
	} else if !IsSigningRefused(err) {
		return errors.New("mutation did not produce a provider-side refusal; policy enforcement is unproven")
	}
	signed, err := custody.Sign(ctx, control)
	if err != nil || !bytes.Equal(signed, expected) {
		return errors.New("known-good activity failed after the rejection; policy enforcement is unproven")
	}
	return nil
}

func TestLivePolicyRejectionRequiresAHealthyPositiveControl(t *testing.T) {
	control, expected := custodyFixture()
	mutation := bytes.Clone(control.Transaction)
	mutation[len(mutation)-1] ^= 1
	config := Config{OrganizationID: "organization", SignWith: "wallet"}

	t.Run("policy rejection while available", func(t *testing.T) {
		calls := 0
		custody, err := newSigner(transactionClientFunc(func(
			_ context.Context,
			request turnkey.SignTransactionRequest,
		) (*turnkey.SignTransactionResponse, error) {
			calls++
			if calls == 1 {
				return nil, &turnkey.ActivityFailedError{
					ActivityID: "rejected-activity", Status: turnkey.ActivityStatusRejected,
				}
			}
			return completedResponse(request, expected), nil
		}), config)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyLivePolicyRejection(
			t.Context(), custody, mutation, control, expected, time.Now().UTC(),
		); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("Turnkey calls = %d, want mutation plus positive control", calls)
		}
	})

	t.Run("provider outage", func(t *testing.T) {
		const privateProviderError = "private provider outage"
		custody, err := newSigner(transactionClientFunc(func(
			context.Context,
			turnkey.SignTransactionRequest,
		) (*turnkey.SignTransactionResponse, error) {
			return nil, errors.New(privateProviderError)
		}), config)
		if err != nil {
			t.Fatal(err)
		}
		err = verifyLivePolicyRejection(
			t.Context(), custody, mutation, control, expected, time.Now().UTC(),
		)
		if err == nil || !strings.Contains(err.Error(), "policy enforcement is unproven") ||
			strings.Contains(err.Error(), privateProviderError) {
			t.Fatalf("provider outage qualification = %v", err)
		}
	})
}

func verifiedLiveAPIKeyStamper(t *testing.T) *turnkey.APIKeyStamper {
	t.Helper()
	stamper, err := loadVerifiedAPIKeyStamper(
		os.Getenv(livePrivateKeyFile), os.Getenv(livePublicKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	return stamper
}

func TestLiveQualificationVerifiesTheLocalAPIKeyPair(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "qualification.private")
	if err := os.WriteFile(path, []byte(strings.Repeat("1", 64)+":p256\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamper, err := loadAPIKeyStamper(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedAPIKeyStamper(path, stamper.PublicKey()); err != nil {
		t.Fatalf("matching Turnkey API key pair was refused: %v", err)
	}
	for _, publicKey := range []string{"", "different"} {
		if _, err := loadVerifiedAPIKeyStamper(path, publicKey); err == nil {
			t.Fatal("invalid Turnkey API key pair was accepted")
		}
	}
}

func verifyLiveSigningAddress(
	t *testing.T,
	stamper turnkey.Stamper,
	organizationID, privateKeyID, expectedAddress string,
) {
	t.Helper()
	client, err := newTurnkeyClient(stamper, organizationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyTurnkeySigningAddress(
		t.Context(), client, organizationID, privateKeyID, expectedAddress,
	); err != nil {
		t.Fatal(err)
	}
}

func TestTurnkeyIdentityAuthenticationFailureExplainsRegistration(t *testing.T) {
	client := &turnkeyIdentityClientStub{}
	err := verifyTurnkeySigningAddress(
		t.Context(), client, "organization", "private-key", "address",
	)
	if err == nil || !strings.Contains(err.Error(), "matching public key") ||
		!strings.Contains(err.Error(), "API-only user") {
		t.Fatalf("Turnkey authentication guidance = %v", err)
	}
	if client.whoamiCalls != 1 || client.privateKeyCalls != 0 || client.privateKeysCalls != 0 ||
		client.walletsCalls != 0 || client.walletAccountCalls != 0 {
		t.Fatalf("failed authentication continued into key lookup: %+v", client)
	}
}

type turnkeyIdentityClientStub struct {
	whoamiCalls        int
	privateKeyCalls    int
	privateKeysCalls   int
	walletsCalls       int
	walletAccountCalls int
	whoami             *turnkey.GetWhoamiResponse
	privateKey         *turnkey.GetPrivateKeyResponse
	privateKeys        *turnkey.GetPrivateKeysResponse
	wallets            *turnkey.GetWalletsResponse
	walletAccount      *turnkey.GetWalletAccountResponse
}

func (s *turnkeyIdentityClientStub) GetWhoami(
	context.Context, turnkey.GetWhoamiRequest,
) (*turnkey.GetWhoamiResponse, error) {
	s.whoamiCalls++
	return s.whoami, nil
}

func (s *turnkeyIdentityClientStub) GetPrivateKey(
	context.Context, turnkey.GetPrivateKeyRequest,
) (*turnkey.GetPrivateKeyResponse, error) {
	s.privateKeyCalls++
	return s.privateKey, nil
}

func (s *turnkeyIdentityClientStub) GetPrivateKeys(
	context.Context, turnkey.GetPrivateKeysRequest,
) (*turnkey.GetPrivateKeysResponse, error) {
	s.privateKeysCalls++
	return s.privateKeys, nil
}

func (s *turnkeyIdentityClientStub) GetWallets(
	context.Context, turnkey.GetWalletsRequest,
) (*turnkey.GetWalletsResponse, error) {
	s.walletsCalls++
	return s.wallets, nil
}

func (s *turnkeyIdentityClientStub) GetWalletAccount(
	context.Context, turnkey.GetWalletAccountRequest,
) (*turnkey.GetWalletAccountResponse, error) {
	s.walletAccountCalls++
	return s.walletAccount, nil
}

func TestTurnkeyAddressIdentityStillAuthenticatesAndVerifiesOwnership(t *testing.T) {
	const organization = "organization"
	const address = "address"
	format := turnkey.AddressFormatSolana
	wrongAddress := "other"
	addressValue := address
	client := &turnkeyIdentityClientStub{
		whoami: &turnkey.GetWhoamiResponse{OrganizationID: organization},
		privateKeys: &turnkey.GetPrivateKeysResponse{PrivateKeys: []turnkey.PrivateKey{{
			Addresses: []turnkey.ExternalDataV1Address{
				{Format: &format, Address: &wrongAddress},
				{Format: &format, Address: &addressValue},
			},
		}}},
	}
	if err := verifyTurnkeySigningAddress(
		t.Context(), client, organization, address, address,
	); err != nil {
		t.Fatal(err)
	}
	if client.whoamiCalls != 1 || client.privateKeysCalls != 1 || client.privateKeyCalls != 0 {
		t.Fatalf("unexpected Turnkey identity calls: %+v", client)
	}
}

func TestTurnkeyWalletAccountAddressVerifiesOwnership(t *testing.T) {
	const organization = "organization"
	const address = "address"
	client := &turnkeyIdentityClientStub{
		whoami:      &turnkey.GetWhoamiResponse{OrganizationID: organization},
		privateKeys: &turnkey.GetPrivateKeysResponse{},
		wallets: &turnkey.GetWalletsResponse{Wallets: []turnkey.Wallet{
			{WalletID: "first-wallet"},
			{WalletID: "matching-wallet"},
		}},
		walletAccount: &turnkey.GetWalletAccountResponse{Account: turnkey.WalletAccount{
			Address: address, AddressFormat: turnkey.AddressFormatSolana, Curve: turnkey.CurveEd25519,
		}},
	}
	if err := verifyTurnkeySigningAddress(
		t.Context(), client, organization, address, address,
	); err != nil {
		t.Fatal(err)
	}
	if client.whoamiCalls != 1 || client.privateKeysCalls != 1 ||
		client.walletsCalls != 1 || client.walletAccountCalls != 1 {
		t.Fatalf("unexpected Turnkey identity calls: %+v", client)
	}
}

func TestTurnkeyPrivateKeyIDResolvesToExpectedSolanaAddress(t *testing.T) {
	const organization = "organization"
	const privateKeyID = "private-key-id"
	const expectedAddress = "address"
	format := turnkey.AddressFormatSolana
	address := expectedAddress
	client := &turnkeyIdentityClientStub{
		whoami: &turnkey.GetWhoamiResponse{OrganizationID: organization},
		privateKey: &turnkey.GetPrivateKeyResponse{PrivateKey: turnkey.PrivateKey{
			PrivateKeyID: privateKeyID,
			Addresses: []turnkey.ExternalDataV1Address{{
				Format: &format, Address: &address,
			}},
		}},
	}
	if err := verifyTurnkeySigningAddress(
		t.Context(), client, organization, privateKeyID, expectedAddress,
	); err != nil {
		t.Fatal(err)
	}
	if client.whoamiCalls != 1 || client.privateKeyCalls != 1 || client.privateKeysCalls != 0 {
		t.Fatalf("unexpected Turnkey identity calls: %+v", client)
	}

	wrongAddress := "other"
	client.privateKey.PrivateKey.Addresses[0].Address = &wrongAddress
	if err := verifyTurnkeySigningAddress(
		t.Context(), client, organization, privateKeyID, expectedAddress,
	); err == nil || !strings.Contains(err.Error(), "different Solana address") {
		t.Fatalf("wrong Turnkey private-key mapping = %v", err)
	}
}

func TestQualificationFixtureIsOneSignerVersionZero(t *testing.T) {
	privateKey := bytes.Repeat([]byte{0x31}, 32)
	publicKey := solana.Encode(privateKey)
	transaction := qualificationTransaction(t, publicKey, qualificationDestination, 1, 1)
	if len(transaction) <= 65 || transaction[0] != 1 || transaction[65] != 0x80 {
		t.Fatal("qualification fixture is not a one-signer version-zero transaction")
	}
	message, err := solana.DecodeV0Message(transaction[65:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Instructions) != 1 || solana.Encode(message.StaticAccountKeys[0][:]) != publicKey {
		t.Fatal("qualification fixture does not preserve its sole fee payer and instruction")
	}
	for name, mutation := range map[string][]byte{
		"amount":      qualificationTransaction(t, publicKey, qualificationDestination, 2, 1),
		"recipient":   qualificationTransaction(t, publicKey, mutationDestination, 1, 1),
		"instruction": qualificationTransaction(t, publicKey, qualificationDestination, 1, 2),
	} {
		if bytes.Equal(transaction, mutation) {
			t.Fatalf("%s mutation did not change the qualification transaction", name)
		}
	}
}

func liveCustodyRequest(transaction []byte, now time.Time) signer.TransactionCustodyRequest {
	digest := sha256.Sum256(transaction)
	return signer.TransactionCustodyRequest{
		RequestSHA256: hex.EncodeToString(digest[:]),
		TimestampMS:   now.UnixMilli(),
		Transaction:   transaction,
	}
}

func qualificationTransaction(
	t *testing.T,
	source, destination string,
	lamports uint64,
	instructionCount int,
) []byte {
	t.Helper()
	if source == "" {
		t.Fatalf("%s is required", liveSolanaAddress)
	}
	transfer := make([]byte, 12)
	binary.LittleEndian.PutUint32(transfer[:4], 2)
	binary.LittleEndian.PutUint64(transfer[4:], lamports)
	instruction := solana.Instruction{
		Program: systemProgram,
		Accounts: []solana.AccountMeta{
			{Address: source, Signer: true, Writable: true},
			{Address: destination, Writable: true},
		},
		Data: transfer,
	}
	instructions := make([]solana.Instruction, instructionCount)
	for index := range instructions {
		instructions[index] = instruction
	}
	blockhash := solana.Encode(bytes.Repeat([]byte{0x51}, 32))
	message, err := solana.BuildV0Message(source, blockhash, instructions, nil)
	if err != nil {
		t.Fatalf("build qualification message: %v", err)
	}
	transaction, err := solana.BuildUnsignedV0Transaction(message, nil)
	if err != nil {
		t.Fatalf("build qualification transaction: %v", err)
	}
	return transaction
}
