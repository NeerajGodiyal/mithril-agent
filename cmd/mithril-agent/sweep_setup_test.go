package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/offchainmsg"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestSweepNonceRequiresCompleteRandomInput(t *testing.T) {
	original := swapSetupRandom
	t.Cleanup(func() { swapSetupRandom = original })

	swapSetupRandom = bytes.NewReader(bytes.Repeat([]byte{1}, 15))
	if _, err := newSweepNonce(); err == nil {
		t.Fatal("short random input produced a destination nonce")
	}

	swapSetupRandom = iotest.OneByteReader(bytes.NewReader(bytes.Repeat([]byte{2}, 16)))
	nonce, err := newSweepNonce()
	if err != nil || nonce != strings.Repeat("02", 16) {
		t.Fatalf("partial random reads produced nonce %q, %v", nonce, err)
	}
}

func sweepTestKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return solana.Encode(public), private
}

// The proof must accept both signing forms over the identical challenge and
// refuse every substitution: this is the whole defense against sweeping to a
// lookalike or mistyped address.
func TestSweepDestinationProof(t *testing.T) {
	agentAccount, _ := sweepTestKey(t)
	destination, destinationKey := sweepTestKey(t)
	other, otherKey := sweepTestKey(t)
	nonce := strings.Repeat("ab", 16)
	issued := "2026-08-05T12:00:00Z"
	challenge := sweepChallenge(agentAccount, destination, nonce, issued)

	sealed, err := offchainmsg.Envelope(challenge)
	if err != nil {
		t.Fatalf("the challenge must be plain printable ASCII: %v", err)
	}
	cliSig := solana.Encode(ed25519.Sign(destinationKey, sealed))
	rawSig := solana.Encode(ed25519.Sign(destinationKey, []byte(challenge)))

	for name, signature := range map[string]string{"cli": cliSig, "raw": rawSig} {
		if err := verifySweepDestinationProof(agentAccount, destination, nonce, issued, signature); err != nil {
			t.Fatalf("%s form rejected: %v", name, err)
		}
	}
	if err := verifySweepDestinationProof(agentAccount, destination, nonce, issued, ""); err == nil {
		t.Fatal("an empty signature must be refused; the ceremony has no bypass")
	}
	wrong := solana.Encode(ed25519.Sign(otherKey, sealed))
	if err := verifySweepDestinationProof(agentAccount, destination, nonce, issued, wrong); err == nil {
		t.Fatal("a signature from another key must be refused")
	}
	if err := verifySweepDestinationProof(agentAccount, other, nonce, issued, cliSig); err == nil {
		t.Fatal("a proof for another destination must be refused")
	}
	if err := verifySweepDestinationProof(agentAccount, destination, strings.Repeat("cd", 16), issued, cliSig); err == nil {
		t.Fatal("a replayed signature under a fresh nonce must be refused")
	}
	if err := verifySweepDestinationProof(agentAccount, destination, nonce, "2027-01-01T00:00:00Z", cliSig); err == nil {
		t.Fatal("a changed issue time must be refused: it feeds the activation delay")
	}
}

func TestFirstMidnightAfterAlwaysDelaysAndAligns(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 5, 0, 0, 1, 0, time.UTC),
		time.Date(2026, 8, 5, 12, 30, 0, 0, time.UTC),
		time.Date(2026, 8, 5, 23, 59, 59, 0, time.UTC),
	}
	for _, at := range cases {
		midnight := firstMidnightAfter(at)
		if midnight.Before(at) {
			t.Fatalf("%s: anchor %s is before the earliest allowed time", at, midnight)
		}
		if midnight.Hour() != 0 || midnight.Minute() != 0 || midnight.Second() != 0 {
			t.Fatalf("%s: anchor %s is not midnight-aligned; the signer would refuse it", at, midnight)
		}
	}
}

func TestParseDecimalUnits9(t *testing.T) {
	good := map[string]uint64{
		"1":           1_000_000_000,
		"0.1":         100_000_000,
		"2.000000001": 2_000_000_001,
		"1000":        1_000_000_000_000,
	}
	for text, want := range good {
		got, err := parseDecimalUnits9(text, "amount")
		if err != nil || got != want {
			t.Fatalf("%q: got %d err %v, want %d", text, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-1", "1e9", "1.0000000001", "abc", " 1"} {
		if _, err := parseDecimalUnits9(bad, "amount"); err == nil {
			t.Fatalf("%q parsed but must not", bad)
		}
	}
	if _, err := parseDecimalUnitsLamports("1001", "amount"); err == nil {
		t.Fatal("amounts beyond the pilot range must be refused")
	}
}

// The installed file set must be coherent: the profile validates, the signer
// policy pins the proven destination, every fingerprint matches, and the
// state paths are keyed by the fingerprint so a destination change can never
// inherit the previous ledger.
func TestInstallSweepSetupProducesACoherentBoundSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	agentAccount, _ := sweepTestKey(t)
	destination, _ := sweepTestKey(t)
	anchor := firstMidnightAfter(time.Now().UTC().Add(25 * time.Hour))
	profile := agent.Profile{
		Name: agent.ProfileTreasurySweepV1, Version: 1, Cluster: "devnet",
		Source: agentAccount, Destination: destination,
		ReserveLamports:     2_000_000,
		MinTransferLamports: 100_000_000, MaxTransferLamports: 1_000_000_000,
		DailyCapLamports: 2_000_000_000, MaxFeeLamports: defaultSweepFee,
		ScheduleWindowSeconds: defaultSweepWindow, ScheduleAnchorUnix: anchor.Unix(),
		MaxClockUncertaintyMillis: 500, MaxObservationAgeSeconds: 60,
		MinHealthyObservationSeconds: 30, MinHealthySlotAdvance: 8,
		MaxNodeLagSlots: 150, MaxReconciliationSeconds: 120,
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("fixture profile: %v", err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "sweep")
	proof := destinationProof{
		Version: 1, AgentAccount: agentAccount, Destination: destination,
		Nonce: strings.Repeat("ab", 16), IssuedAt: "2026-08-05T12:00:00Z",
		SignatureBase58: "unverified-in-this-fixture", ActiveAfterUnix: anchor.Unix(),
	}
	evidence := proposalcheck.ProviderBindings{
		PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("1", 64),
		SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("2", 64),
	}
	if err := installSweepSetup(root, fingerprint, profile, filepath.Join(home, "wallet.json"), "", "", evidence, proof); err != nil {
		t.Fatalf("install: %v", err)
	}

	var cfg config
	if err := readStrictJSONFile(filepath.Join(root, "config.json"), &cfg); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if cfg.Profile != profile {
		t.Fatal("the installed profile does not match what was configured")
	}
	var signerPolicy signer.Policy
	if err := readStrictJSONFile(filepath.Join(root, "signer-policy.json"), &signerPolicy); err != nil {
		t.Fatalf("read signer policy: %v", err)
	}
	if signerPolicy.Destination != destination {
		t.Fatal("the signer policy does not pin the proven destination")
	}
	if signerPolicy.ProfileFingerprint != fingerprint {
		t.Fatal("the signer policy is not bound to the profile fingerprint")
	}
	if signerPolicy.ScheduleAnchorUnix != anchor.Unix() {
		t.Fatal("the signer does not enforce the same activation anchor")
	}
	stateDir := "state-" + fingerprint[:8]
	if !strings.Contains(signerPolicy.AuthorizationLedgerPath, stateDir) {
		t.Fatalf("the ledger path %q is not keyed by the fingerprint", signerPolicy.AuthorizationLedgerPath)
	}
	if info, err := os.Stat(filepath.Join(root, stateDir)); err != nil || !info.IsDir() {
		t.Fatal("the fingerprint-keyed state directory was not created")
	}
	// A second install to the same root must refuse rather than overwrite:
	// an existing setup may hold a live ledger.
	if err := installSweepSetup(root, fingerprint, profile, filepath.Join(home, "wallet.json"), "", "", evidence, proof); err == nil {
		t.Fatal("reinstalling over an existing setup must fail")
	}
}

func readStrictJSONFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return strictjson.Decode(raw, out)
}

// A zero delay must still produce a legal anchor — midnight-aligned and
// positive — while leaving the current window already open, so a Devnet test
// does not have to wait for tomorrow to see the mechanism work.
func TestZeroActivationDelayAnchorsInTheOpenWindow(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 50, 52, 0, time.UTC)
	anchor := mostRecentMidnight(now)
	if anchor.After(now) {
		t.Fatalf("anchor %s must not be in the future", anchor)
	}
	if anchor.Unix()%86_400 != 0 {
		t.Fatalf("anchor %s is not midnight-aligned; both validators would refuse it", anchor)
	}
	if anchor.Unix() <= 0 {
		t.Fatal("anchor must be positive")
	}
	// And the delayed form must still be strictly later than the open one.
	delayed := firstMidnightAfter(now.Add(24 * time.Hour))
	if !delayed.After(anchor) {
		t.Fatal("a delayed anchor must be later than an immediate one")
	}
}

// The issue time is inside the signed bytes; if it is never checked it is
// decorative. These are the bounds that make it mean something.
func TestProofFreshnessBounds(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		issued  string
		wantErr bool
	}{
		{"just signed", now.Format(time.RFC3339), false},
		{"a week old", now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), false},
		{"just inside the age bound", now.Add(-29 * 24 * time.Hour).Format(time.RFC3339), false},
		{"beyond the age bound", now.Add(-31 * 24 * time.Hour).Format(time.RFC3339), true},
		{"slightly ahead, tolerated as clock skew", now.Add(2 * time.Minute).Format(time.RFC3339), false},
		{"far in the future", now.Add(48 * time.Hour).Format(time.RFC3339), true},
		{"not a timestamp", "yesterday", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkProofFreshness(tc.issued, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("got err=%v, want error=%v", err, tc.wantErr)
			}
		})
	}
}

// Re-verification of a stored proof must NOT apply the age bound: the proof
// was valid when made, and reporting it as invalid later would look like
// tampering when nothing has changed.
func TestStoredProofVerificationIgnoresAge(t *testing.T) {
	agentAccount, _ := sweepTestKey(t)
	destination, destinationKey := sweepTestKey(t)
	nonce := strings.Repeat("ab", 16)
	ancient := "2020-01-01T00:00:00Z"
	challenge := sweepChallenge(agentAccount, destination, nonce, ancient)
	sealed, err := offchainmsg.Envelope(challenge)
	if err != nil {
		t.Fatal(err)
	}
	signature := solana.Encode(ed25519.Sign(destinationKey, sealed))
	if err := verifySweepDestinationProof(agentAccount, destination, nonce, ancient, signature); err != nil {
		t.Fatalf("a stored proof must still verify regardless of age: %v", err)
	}
	if err := checkProofFreshness(ancient, time.Now().UTC()); err == nil {
		t.Fatal("but accepting one that old as NEW must be refused")
	}
}
