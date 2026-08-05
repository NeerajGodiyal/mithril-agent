package squads

import (
	"encoding/hex"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// A real mainnet spending limit, captured on 2026-08-04. Decoding real bytes is
// the only way to know the layout is right: a published field list can be stale
// or wrong, and a fixture we generated ourselves would only prove we agree with
// ourselves.
const mainnetLimitHex = "0ac91ba0dac3de9812d6ea8f464eaef4839cfc30e6643cc3c23b44c7f0fce7a3cfbb030cf8a54a7d5e7f00fb3ad784ff0a720f00d2246f8668c3e6416bc368d4856a6dfdfafcac910000000000000000000000000000000000000000000000000000000000000000000065cd1d00000000010065cd1d000000007e96bb6600000000ff01000000"

// A real devnet limit, same date: a token mint rather than SOL, and a one-time
// unlimited amount. The two together exercise both branches of every field.
const devnetLimitHex = "0ac91ba0dac3de981e080025086651da25a34ae3682b2e4f4c5a1efbbb55e425f479b1f53e1b4d60ff5bee127fdf2c3e7a8e365ae55eb7a28df843cc63061c4a94d0f29d0741d99b003b442cb3912157f13a933d0134282d032b5ffecd01a2dbf1b7790608df002ea7ffffffffffffffff00ffffffffffffffff85c1e26900000000ff01000000"

func decodeFixture(t *testing.T, encoded string, members, destinations int) SpendingLimit {
	t.Helper()
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	// The captured prefix stops after the member count; append the key bodies
	// and an empty destination list so the account is complete.
	raw = append(raw, make([]byte, members*pubkeyBytes)...)
	countBytes := []byte{byte(destinations), 0, 0, 0}
	raw = append(raw, countBytes...)
	raw = append(raw, make([]byte, destinations*pubkeyBytes)...)
	limit, err := DecodeSpendingLimit(raw)
	if err != nil {
		t.Fatalf("decoding a real account failed: %v", err)
	}
	return limit
}

// Every field must land on a plausible value when decoding bytes taken straight
// off mainnet.
func TestDecodesARealMainnetLimit(t *testing.T) {
	limit := decodeFixture(t, mainnetLimitHex, 1, 1)

	if !limit.IsNativeSOL() {
		t.Errorf("mint = %q, want the native-SOL zero mint", limit.Mint)
	}
	if limit.Amount != 500_000_000 {
		t.Errorf("amount = %d, want 500000000 (0.5 SOL)", limit.Amount)
	}
	if limit.Period != Daily {
		t.Errorf("period = %v, want daily", limit.Period)
	}
	if limit.Remaining != limit.Amount {
		t.Errorf("remaining = %d, want the full %d", limit.Remaining, limit.Amount)
	}
	// A real reset timestamp, not a zero and not a far-future value.
	if limit.LastResetAt < 1_600_000_000 || limit.LastResetAt > 2_000_000_000 {
		t.Errorf("last reset = %d, which is not a plausible timestamp", limit.LastResetAt)
	}
	if limit.VaultIndex != 0 || limit.Bump != 255 {
		t.Errorf("vault index = %d, bump = %d", limit.VaultIndex, limit.Bump)
	}
	if len(limit.Members) != 1 {
		t.Errorf("members = %d, want 1", len(limit.Members))
	}
}

// The devnet fixture covers the other side of every branch: a token mint and a
// one-time unlimited amount.
func TestDecodesARealDevnetLimit(t *testing.T) {
	limit := decodeFixture(t, devnetLimitHex, 1, 0)

	if limit.IsNativeSOL() {
		t.Error("a token-mint limit was reported as native SOL")
	}
	if limit.Amount != ^uint64(0) {
		t.Errorf("amount = %d, want the unlimited sentinel", limit.Amount)
	}
	if limit.Period != OneTime {
		t.Errorf("period = %v, want one-time", limit.Period)
	}
}

// A truncated account must be refused. Decoding it into a small, plausible
// limit is the most dangerous possible failure here: it would understate how
// much can leave the vault.
func TestTruncatedAccountIsRefusedNotGuessed(t *testing.T) {
	raw, err := hex.DecodeString(mainnetLimitHex)
	if err != nil {
		t.Fatal(err)
	}
	for cut := range len(raw) {
		if _, err := DecodeSpendingLimit(raw[:cut]); err == nil {
			t.Fatalf("a %d-byte truncation decoded successfully", cut)
		}
	}
	// Complete but with the key bodies missing is also a truncation.
	if _, err := DecodeSpendingLimit(raw); err == nil {
		t.Error("an account ending at its member count decoded successfully")
	}
}

// Anything that is not a spending limit must be refused outright.
func TestForeignAccountIsRefused(t *testing.T) {
	notALimit := make([]byte, 200)
	if _, err := DecodeSpendingLimit(notALimit); err == nil {
		t.Error("a zeroed account decoded as a spending limit")
	}
	multisig := make([]byte, 200)
	copy(multisig, []byte{0xe0, 0x74, 0x79, 0xba, 0x44, 0xa1, 0x4f, 0xec})
	if _, err := DecodeSpendingLimit(multisig); err == nil {
		t.Error("a Multisig account decoded as a spending limit")
	}
}

// An unrecognised period must never be reported as a bounded one: "unknown"
// silently treated as one-time would understate a recurring exposure.
func TestUnknownPeriodIsRefused(t *testing.T) {
	raw, err := hex.DecodeString(mainnetLimitHex)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, make([]byte, pubkeyBytes)...)
	raw = append(raw, 0, 0, 0, 0)
	raw[offsetPeriod] = 9
	if _, err := DecodeSpendingLimit(raw); err == nil {
		t.Fatal("a limit with an undefined period decoded successfully")
	}
	if Period(9).Valid() || Period(9).String() != "unknown" {
		t.Error("an undefined period does not report itself as unknown")
	}
}

// Trailing bytes mean the account is not what we think it is.
func TestTrailingBytesAreRefused(t *testing.T) {
	raw, err := hex.DecodeString(mainnetLimitHex)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, make([]byte, pubkeyBytes)...)
	raw = append(raw, 0, 0, 0, 0)
	if _, err := DecodeSpendingLimit(raw); err != nil {
		t.Fatalf("a well-formed account was refused: %v", err)
	}
	if _, err := DecodeSpendingLimit(append(raw, 0)); err == nil {
		t.Error("an account with a trailing byte decoded successfully")
	}
}

// The boundary is only trustworthy because this software cannot use it. If this
// package ever gains the ability to build or sign a transaction, the boundary
// becomes only as good as our own code, which is the thing it exists to avoid.
func TestSquadsPackageCannotMoveFunds(t *testing.T) {
	forbidden := []string{
		"mithril-agent/signer", "mithril-agent/submitter", "mithril-agent/sealedtx",
		"mithril-agent/signerclient", "mithril-agent/submitterclient",
		"crypto/ed25519",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			for _, banned := range forbidden {
				if strings.Contains(path, banned) {
					t.Errorf("%s imports %s", entry.Name(), path)
				}
			}
		}
		source, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"Sign(", "Submit(", "BuildLegacyMessage", "Instruction{"} {
			if strings.Contains(string(source), banned) {
				t.Errorf("%s contains %q: this package must only read", entry.Name(), banned)
			}
		}
	}
	if checked == 0 {
		t.Fatal("the guard checked no files, so it proves nothing")
	}
}
