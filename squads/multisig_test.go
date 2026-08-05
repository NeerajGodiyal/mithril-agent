package squads

import (
	"encoding/hex"
	"testing"
	"time"
)

// Real Squads v4 multisig accounts captured on 2026-08-05. The two together are
// the whole point: the devnet one has rent_collector None and the mainnet one
// has it Some, and that single Option shifts every field after it by 32 bytes.
// A decoder verified against only one of them reads the other's control
// authority out of the wrong bytes.
const devnetMultisigHex = "e07479ba44a14fec5473be7958b9ff5b0db9e96e642ead4fff5b327b2299e5972f71477bb03edea9c4475aeb27d4ef8700c3b00012ab10f1dc047e44c2d0922b541bd91848aecf1f0200803a09000000000000000000000000000000000000ff02000000c4475aeb27d4ef8700c3b00012ab10f1dc047e44c2d0922b541bd91848aecf1f07ff0baea608347a3ad2353c205c4a46b9254af6ee6e401e6c256b040eb8c0bf51070000000000000000000000000000000000000000000000000000000000000000"

const mainnetMultisigHex = "e07479ba44a14fec01aa096e8fe3d898368ccc6af05cedc7e199641b5d9508316534351dbec8b44c00000000000000000000000000000000000000000000000000000000000000000200000000000100000000000000010000000000000001176fc895d8df9ac0d684ab1e63b06156a1a605673344b9f2b4ba50bd00a6892fff04000000176fc895d8df9ac0d684ab1e63b06156a1a605673344b9f2b4ba50bd00a6892f0017e85f890eced77a7f9a2f0d0b33ed25300066e830186619fb155e78667a60a9072a74fd5772cb296c5d4abd95bbc4b9188ccfd98433085353c44034cd9069ddb00789f344cdf15d3b174c4af58d9919919bca88b555a5543ddb10fe0751f1c6a30402000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

func decodeMultisigFixture(t *testing.T, encoded string) Multisig {
	t.Helper()
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	multisig, err := DecodeMultisig(raw)
	if err != nil {
		t.Fatalf("decoding a real account failed: %v", err)
	}
	return multisig
}

// rent_collector None: bump and the member vector sit 32 bytes earlier.
func TestDecodesARealDevnetMultisigWithoutRentCollector(t *testing.T) {
	multisig := decodeMultisigFixture(t, devnetMultisigHex)
	if multisig.ConfigAuthority != "EDC1uYJJrp5VNcz4UsHBgvwsr2AGbKjpRTxDPDs3AyLi" {
		t.Fatalf("config authority: %s", multisig.ConfigAuthority)
	}
	if multisig.Autonomous() {
		t.Fatal("a multisig with a real config authority is not autonomous")
	}
	if multisig.RentCollector != "" {
		t.Fatalf("rent collector should be unset, got %q", multisig.RentCollector)
	}
	if multisig.Threshold != 2 || multisig.Bump != 255 {
		t.Fatalf("threshold %d bump %d", multisig.Threshold, multisig.Bump)
	}
	if len(multisig.Members) != 2 {
		t.Fatalf("members: %d", len(multisig.Members))
	}
	if multisig.Members[0].Key != "EDC1uYJJrp5VNcz4UsHBgvwsr2AGbKjpRTxDPDs3AyLi" {
		t.Fatalf("member 0: %s", multisig.Members[0].Key)
	}
}

// rent_collector Some, plus realloc padding after the members.
func TestDecodesARealMainnetMultisigWithRentCollector(t *testing.T) {
	multisig := decodeMultisigFixture(t, mainnetMultisigHex)
	if !multisig.Autonomous() {
		t.Fatalf("expected autonomous, config authority %s", multisig.ConfigAuthority)
	}
	if multisig.RentCollector == "" {
		t.Fatal("rent collector should be set")
	}
	if len(multisig.Members) != 4 {
		t.Fatalf("members: %d", len(multisig.Members))
	}
}

func TestTruncatedMultisigIsRefusedNotGuessed(t *testing.T) {
	raw, err := hex.DecodeString(devnetMultisigHex)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{8, 60, 94, 99, 120} {
		if _, err := DecodeMultisig(raw[:cut]); err == nil {
			t.Fatalf("a %d-byte account decoded instead of failing", cut)
		}
	}
}

func TestForeignAccountIsNotAMultisig(t *testing.T) {
	raw, err := hex.DecodeString(devnetMultisigHex)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xff
	if _, err := DecodeMultisig(raw); err == nil {
		t.Fatal("an account with a different discriminator decoded as a multisig")
	}
}

// A tag other than 0 or 1 means the bytes are not what we think they are, so
// every later offset would be guesswork.
func TestInvalidRentCollectorTagIsRefused(t *testing.T) {
	raw, err := hex.DecodeString(devnetMultisigHex)
	if err != nil {
		t.Fatal(err)
	}
	raw[offsetRentCollectorFlag] = 2
	if _, err := DecodeMultisig(raw); err == nil {
		t.Fatal("an invalid option tag decoded")
	}
}

// Trailing bytes are expected (Anchor over-allocates) but must be zero padding.
// Content there would mean the member list was read at the wrong offset.
func TestNonZeroTrailingBytesAreRefused(t *testing.T) {
	raw, err := hex.DecodeString(mainnetMultisigHex)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] = 1
	if _, err := DecodeMultisig(raw); err == nil {
		t.Fatal("non-zero trailing bytes decoded")
	}
}

// The program resets lazily inside the spend instruction, so Remaining alone
// says an exhausted limit is exhausted forever.
func TestAvailableAtModelsTheProgramsReset(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	limit := SpendingLimit{Amount: 1_000, Remaining: 0, LastResetAt: base.Unix(), Period: Daily}

	if got := limit.AvailableAt(base); got != 0 {
		t.Fatalf("at the reset instant nothing has refilled yet: got %d", got)
	}
	// spending_limit_use.rs:152 uses strictly greater-than. Exactly one period
	// later has NOT passed the period, so the program would not reset.
	exactly := base.Add(24 * time.Hour)
	if got := limit.AvailableAt(exactly); got != 0 {
		t.Fatalf("exactly one period is not past it: got %d, want 0", got)
	}
	if got := limit.AvailableAt(exactly.Add(time.Second)); got != 1_000 {
		t.Fatalf("one second past the period must refill: got %d", got)
	}

	// OneTime never refills, however long you wait.
	once := SpendingLimit{Amount: 1_000, Remaining: 0, LastResetAt: base.Unix(), Period: OneTime}
	if got := once.AvailableAt(base.Add(365 * 24 * time.Hour)); got != 0 {
		t.Fatalf("a one-time limit refilled: got %d", got)
	}
}

func TestVerifyControlProvesRevocability(t *testing.T) {
	const (
		vault   = "VaultAddress11111111111111111111111111111111"
		owner   = "OwnerWallet1111111111111111111111111111111111"
		agent   = "AgentAccount111111111111111111111111111111111"
		outside = "SomeoneElse1111111111111111111111111111111111"
	)
	limit := SpendingLimit{Multisig: vault, Members: []string{agent}}
	expect := ControlExpectation{MultisigAddress: vault, Owner: owner, Spender: agent}

	t.Run("owner is the config authority", func(t *testing.T) {
		got, findings := VerifyControl(Multisig{ConfigAuthority: owner}, limit, expect)
		if got != RevocableByOwner || len(findings) != 0 {
			t.Fatalf("got %s findings %v", got, findings)
		}
	})

	t.Run("autonomous and the owner is a member", func(t *testing.T) {
		multisig := Multisig{
			ConfigAuthority: AutonomousConfigAuthority,
			Members:         []Member{{Key: owner, Permissions: 7}},
		}
		got, findings := VerifyControl(multisig, limit, expect)
		if got != RevocableByVote || len(findings) != 0 {
			t.Fatalf("got %s findings %v", got, findings)
		}
	})

	t.Run("autonomous but the owner cannot vote", func(t *testing.T) {
		multisig := Multisig{
			ConfigAuthority: AutonomousConfigAuthority,
			Members:         []Member{{Key: outside, Permissions: 7}},
		}
		got, findings := VerifyControl(multisig, limit, expect)
		if got != NotRevocableByOperator || len(findings) == 0 {
			t.Fatalf("an operator who cannot vote must not read as revocable: %s %v", got, findings)
		}
	})

	t.Run("a third party controls the config", func(t *testing.T) {
		got, findings := VerifyControl(Multisig{ConfigAuthority: outside}, limit, expect)
		if got != NotRevocableByOperator || len(findings) == 0 {
			t.Fatalf("got %s findings %v", got, findings)
		}
	})

	t.Run("an extra spender is a finding", func(t *testing.T) {
		wide := SpendingLimit{Multisig: vault, Members: []string{agent, outside}}
		_, findings := VerifyControl(Multisig{ConfigAuthority: owner}, wide, expect)
		if len(findings) == 0 {
			t.Fatal("a second key able to spend must be reported")
		}
	})

	t.Run("a limit belonging to another vault is a finding", func(t *testing.T) {
		other := SpendingLimit{Multisig: "OtherVault11111111111111111111111111111111111", Members: []string{agent}}
		_, findings := VerifyControl(Multisig{ConfigAuthority: owner}, other, expect)
		if len(findings) == 0 {
			t.Fatal("a limit bound to a different multisig must be reported")
		}
	})

	t.Run("an incomplete expectation proves nothing", func(t *testing.T) {
		got, findings := VerifyControl(Multisig{ConfigAuthority: owner}, limit, ControlExpectation{})
		if got != NotRevocableByOperator || len(findings) == 0 {
			t.Fatal("an unstated expectation must not report revocable")
		}
	})
}
