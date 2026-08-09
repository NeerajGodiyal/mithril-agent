package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic addresses, never real ones. reserveOwner used to be the live Devnet
// trading wallet: a public key, so not a secret, but it identifies a real
// account in a public repository and invites anyone reading the tests to point
// their own setup at it. It is now sha256("mithril-agent documentation test
// wallet v1") base58-encoded — a valid 32-byte address that belongs to nobody.
const (
	reserveOwner = "3ZpvjJ52z5UxmoGWWRWL9YTs4j5Z8xig2ADCWwM6wGW9"
	otherOwner   = "1nc1nerator11111111111111111111111111111111"
)

// A round trip is two swap setups on one wallet. The floor previously resolved a
// single `current` pointer, so it reserved for at most ONE leg — and under the
// plain `swap setup` path, which never writes that pointer, for NEITHER. The
// sweep then drained the wallet below what the trader needed and every later
// trade was refused for insufficient balance.
//
// It sums rather than taking the largest: reserving for one leg would only be
// right if exactly one could ever be armed, and nothing enforces that.
func TestFloorReservesForEveryLegOnThisWallet(t *testing.T) {
	reserve, note, reserveErr := siblingSwapReserve(reserveOwner, []swapNeed{
		{path: "/a/config.json", owner: reserveOwner, lamports: 11_100_000},
		{path: "/b/config.json", owner: reserveOwner, lamports: 51_100_000},
	})
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if want := uint64(62_200_000); reserve != want {
		t.Fatalf("reserve = %d, want %d (both legs)", reserve, want)
	}
	// The operator has to be able to check the arithmetic, not trust it.
	for _, expected := range []string{"total of 2 setup(s)", "/a/config.json", "/b/config.json"} {
		if !strings.Contains(note, expected) {
			t.Errorf("note omitted %q: %s", expected, note)
		}
	}
}

// A setup on a different wallet cannot compete for this balance, and reserving
// for it would starve the sweep for no reason.
func TestFloorIgnoresSetupsOnAnotherWallet(t *testing.T) {
	reserve, note, reserveErr := siblingSwapReserve(reserveOwner, []swapNeed{
		{path: "/mine", owner: reserveOwner, lamports: 11_100_000},
		{path: "/theirs", owner: otherOwner, lamports: 90_000_000},
	})
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if want := uint64(11_100_000); reserve != want {
		t.Fatalf("reserve = %d, want %d — a foreign wallet's setup was counted", reserve, want)
	}
	if strings.Contains(note, "/theirs") {
		t.Errorf("note named a foreign wallet's setup: %s", note)
	}
}

// With nothing to protect the floor must not invent a reserve, or a wallet with
// no swap setup could never sweep.
func TestFloorReservesNothingWithoutASwapSetup(t *testing.T) {
	reserve, note, reserveErr := siblingSwapReserve(reserveOwner, nil)
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if reserve != 0 || note != "" {
		t.Fatalf("reserve = %d, note = %q; want zero and empty", reserve, note)
	}
	// Only foreign setups is the same situation.
	reserve, note, reserveErr = siblingSwapReserve(reserveOwner, []swapNeed{
		{path: "/theirs", owner: otherOwner, lamports: 90_000_000},
	})
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if reserve != 0 || note != "" {
		t.Fatalf("foreign-only reserve = %d, note = %q", reserve, note)
	}
}

// A DISCOVERED `current` pointer is usually empty and may name the same setup
// twice; none of that must reach the arithmetic or be fatal.
func TestLoadingSkipsEmptyAbsentAndDuplicateDiscoveredPaths(t *testing.T) {
	needs, err := loadSwapNeeds(nil, []string{
		"", "/nonexistent/config.json", "", "/nonexistent/config.json",
	})
	if err != nil {
		t.Fatalf("a stale discovered pointer was fatal: %v", err)
	}
	if len(needs) != 0 {
		t.Fatalf("unusable paths produced %d needs: %+v", len(needs), needs)
	}
}

// An EXPLICIT --swap-config is an operator naming a leg to protect. Skipping a
// mistyped one lowered the floor by exactly that leg's requirement and reported
// success — the starvation the reserve exists to prevent, silently.
func TestAMistypedSwapConfigIsFatal(t *testing.T) {
	notASwap := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(notASwap, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"absent":     "/nonexistent/config.json",
		"empty":      "",
		"not a swap": notASwap,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadSwapNeeds([]string{path}, nil); err == nil {
				t.Fatal("an unusable explicit --swap-config was silently skipped")
			}
		})
	}
}

// The need must come from the profile's own requirement. Rebuilding that
// arithmetic here is how the buy leg came to be under-reserved by its rent.
func TestLoadedNeedCarriesTheProfilesOwnRequirement(t *testing.T) {
	profile := testSwapProfile(reserveOwner)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config{Swap: &profile}
	cfg.Evidence.PrimaryTrustDomain = "primary.test"
	cfg.Evidence.SecondaryTrustDomain = "secondary.test"
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	needs, err := loadSwapNeeds([]string{path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(needs) != 1 {
		t.Fatalf("got %d needs, want 1", len(needs))
	}
	if needs[0].owner != reserveOwner || needs[0].path != path {
		t.Errorf("need = %+v, want owner %s at %s", needs[0], reserveOwner, path)
	}
	if want := profile.WalletRequirementLamports(); needs[0].lamports != want {
		t.Errorf("lamports = %d, want the profile's own %d", needs[0].lamports, want)
	}
}

// Saturating at MaxUint64 was WORSE than wrapping: runSweepSetup adds rent and
// fee headroom to this result, so the saturated value wrapped one line later into
// a floor BELOW the no-swap baseline of 1_300_000 — the starvation this whole
// reserve exists to prevent, printed to the operator as a reassuring total.
func TestAnUnpayableReserveIsRefusedRatherThanWrapped(t *testing.T) {
	_, _, err := siblingSwapReserve(reserveOwner, []swapNeed{
		{path: "/a", owner: reserveOwner, lamports: math.MaxUint64 - 1},
		{path: "/b", owner: reserveOwner, lamports: 1_000},
	})
	if err == nil {
		t.Fatal("a reserve larger than the lamport supply was accepted")
	}
	// And the floor arithmetic the caller performs must refuse the same way, since
	// that addition is where the wrap actually happened.
	if _, err := addLamports(rentExemptFloorLamports+3*defaultSweepFee, math.MaxUint64); err == nil {
		t.Fatal("the floor sum wrapped instead of refusing")
	}
}

// A setup the operator NAMED must never be discarded for belonging to another
// wallet. Skipping it lowers the floor by exactly that leg's requirement and says
// nothing — the same silent starvation as a mistyped path, one step later.
func TestANamedSetupOnAnotherWalletIsFatal(t *testing.T) {
	_, _, err := siblingSwapReserve(reserveOwner, []swapNeed{
		{path: "/theirs", owner: otherOwner, lamports: 90_000_000, required: true},
	})
	if err == nil {
		t.Fatal("a named setup on a foreign wallet was silently dropped")
	}
	if !strings.Contains(err.Error(), "/theirs") {
		t.Errorf("the error did not name the setup: %v", err)
	}
}

// Every leg must expose its state under the SAME path shape, or deployment
// tooling has to know which kind of leg it is looking at. The swap setups make
// a real "state" directory; the sweep keys its own by profile fingerprint so a
// destination change cannot inherit the previous ledger — a property worth
// keeping — and now links "state" to it.
//
// Without the link the sweep's state lived at a path nothing outside the setup
// could predict. On 2026-08-06 the operator-status bridge unit still named
// state-6fa913b1 while the live directory was state-425cd4ee; it failed with
// 243/CREDENTIALS and every sweep notification was lost, silently, for hours.
func TestSweepStateIsReachableByAStablePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sweep")
	const fingerprint = "0123456789abcdef0123456789abcdef"
	if err := installSweepDocuments(root, "state-"+fingerprint[:8], map[string]any{
		"config.json": map[string]any{"probe": true},
	}); err != nil {
		t.Fatal(err)
	}

	// The fingerprinted directory is still the real one.
	real := filepath.Join(root, "state-"+fingerprint[:8])
	if info, err := os.Lstat(real); err != nil || !info.IsDir() {
		t.Fatalf("fingerprinted state directory missing: %v", err)
	}
	// And "state" reaches it, exactly as it does for a swap leg.
	stable := filepath.Join(root, stableStateDirName)
	info, err := os.Stat(stable)
	if err != nil || !info.IsDir() {
		t.Fatalf("stable state path does not resolve to a directory: %v", err)
	}
	// It must point AT the fingerprinted directory, not be a second real one —
	// two directories would mean two ledgers and the isolation would be a lie.
	target, err := os.Readlink(stable)
	if err != nil {
		t.Fatalf("stable state path is not a link: %v", err)
	}
	if target != "state-"+fingerprint[:8] {
		t.Errorf("stable path points at %q, want the fingerprinted directory", target)
	}
	// A file written into the real directory is visible through the stable one,
	// which is the whole point for anything reading operator status.
	probe := filepath.Join(real, "events.jsonl.status.json")
	if err := os.WriteFile(probe, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(stable, "events.jsonl.status.json")); err != nil {
		t.Errorf("status file not readable through the stable path: %v", err)
	}
}
