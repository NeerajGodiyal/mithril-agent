package squads

import (
	"strings"
	"testing"
)

const (
	testVault  = "11111111111111111111111111111112"
	testAgent  = "11111111111111111111111111111113"
	testOther  = "11111111111111111111111111111114"
	testMember = "11111111111111111111111111111115"
)

func soundLimit() SpendingLimit {
	return SpendingLimit{
		Multisig: testVault, VaultIndex: 0, Mint: NativeMint,
		Amount: 500_000_000, Period: Daily, Remaining: 500_000_000,
		Members: []string{testMember}, Destinations: []string{testAgent},
	}
}

func soundExpectation() Expectation {
	vaultIndex := uint8(0)
	return Expectation{
		Multisig: testVault, VaultIndex: &vaultIndex,
		Destination: testAgent, Mint: NativeMint,
		MaxAmount: 1_000_000_000, AllowedPeriods: []Period{Daily, OneTime},
	}
}

func problems(findings []Finding) string {
	var parts []string
	for _, finding := range findings {
		parts = append(parts, finding.Check+": "+finding.Problem)
	}
	return strings.Join(parts, "; ")
}

// The happy path has to actually pass, or every other assertion here is
// vacuous.
func TestASoundBoundaryPasses(t *testing.T) {
	if findings := Verify(soundLimit(), soundExpectation()); len(findings) != 0 {
		t.Fatalf("a correctly configured boundary was rejected: %s", problems(findings))
	}
	if !Sound(soundLimit(), soundExpectation()) {
		t.Error("Sound disagrees with Verify")
	}
}

// The single most important check: a limit with no destination list lets funds
// leave for anywhere. It decodes perfectly, so only this catches it.
func TestAnUnaimedLimitIsNotABoundary(t *testing.T) {
	limit := soundLimit()
	limit.Destinations = nil

	findings := Verify(limit, soundExpectation())
	if len(findings) == 0 {
		t.Fatal("a limit with no destinations was accepted as a boundary")
	}
	if !strings.Contains(problems(findings), "any address") {
		t.Errorf("the problem is not explained as unrestricted: %s", problems(findings))
	}
}

// A limit that also permits other destinations bounds the amount but not where
// it goes, and the operator has to be told.
func TestExtraDestinationsAreReported(t *testing.T) {
	limit := soundLimit()
	limit.Destinations = []string{testAgent, testOther}

	findings := Verify(limit, soundExpectation())
	if len(findings) == 0 {
		t.Fatal("a limit permitting an extra destination was accepted")
	}
	if !strings.Contains(problems(findings), "other than the expected one") {
		t.Errorf("the extra destination is not explained: %s", problems(findings))
	}
}

// A limit belonging to a different Multisig config protects different funds.
func TestALimitOnAnotherMultisigIsRejected(t *testing.T) {
	limit := soundLimit()
	limit.Multisig = testOther

	if findings := Verify(limit, soundExpectation()); len(findings) == 0 {
		t.Fatal("a limit on a different vault was accepted")
	}
}

// One Multisig can hold several independent vaults. Verifying only the config
// address would accept a limit aimed at the wrong asset-holding PDA.
func TestALimitOnAnotherVaultIndexIsRejected(t *testing.T) {
	limit := soundLimit()
	limit.VaultIndex = 1

	findings := Verify(limit, soundExpectation())
	if !strings.Contains(problems(findings), "different asset-holding vault") {
		t.Fatalf("a limit on another vault index was accepted: %s", problems(findings))
	}
}

// A cap above what the operator accepts is a finding, not a detail.
func TestACapAboveTheAcceptedMaximumIsReported(t *testing.T) {
	limit := soundLimit()
	limit.Amount = 5_000_000_000

	findings := Verify(limit, soundExpectation())
	if !strings.Contains(problems(findings), "above the") {
		t.Fatalf("an oversized cap was not reported: %s", problems(findings))
	}
}

// A limit that refills faster than expected is a larger exposure than it looks.
func TestAnUnacceptedPeriodIsReported(t *testing.T) {
	limit := soundLimit()
	limit.Period = Weekly

	findings := Verify(limit, soundExpectation())
	if !strings.Contains(problems(findings), "not an accepted period") {
		t.Fatalf("an unexpected refill period was not reported: %s", problems(findings))
	}
}

// An incomplete expectation must fail loudly. Skipping an unstated field would
// let a caller get a clean report by asking less.
func TestAnIncompleteExpectationProvesNothing(t *testing.T) {
	vaultIndex := uint8(0)
	for name, expect := range map[string]Expectation{
		"no multisig":    {VaultIndex: &vaultIndex, Destination: testAgent, Mint: NativeMint, MaxAmount: 1, AllowedPeriods: []Period{Daily}},
		"no vault index": {Multisig: testVault, Destination: testAgent, Mint: NativeMint, MaxAmount: 1, AllowedPeriods: []Period{Daily}},
		"no destination": {Multisig: testVault, VaultIndex: &vaultIndex, Mint: NativeMint, MaxAmount: 1, AllowedPeriods: []Period{Daily}},
		"no mint":        {Multisig: testVault, VaultIndex: &vaultIndex, Destination: testAgent, MaxAmount: 1, AllowedPeriods: []Period{Daily}},
		"no maximum":     {Multisig: testVault, VaultIndex: &vaultIndex, Destination: testAgent, Mint: NativeMint, AllowedPeriods: []Period{Daily}},
		"no periods":     {Multisig: testVault, VaultIndex: &vaultIndex, Destination: testAgent, Mint: NativeMint, MaxAmount: 1},
		"empty":          {},
	} {
		findings := Verify(soundLimit(), expect)
		if len(findings) == 0 {
			t.Errorf("%s: an incomplete expectation produced a clean report", name)
		}
		if !strings.Contains(problems(findings), "proves nothing") {
			t.Errorf("%s: the incompleteness is not explained: %s", name, problems(findings))
		}
	}
}

// Every problem must be reported at once: an operator fixing a funding boundary
// needs the whole list before they touch it.
func TestEveryProblemIsReportedTogether(t *testing.T) {
	limit := soundLimit()
	limit.Multisig = testOther
	limit.Amount = 9_000_000_000
	limit.Period = Monthly
	limit.Destinations = nil

	findings := Verify(limit, soundExpectation())
	if len(findings) < 4 {
		t.Fatalf("only %d problems reported, want at least 4: %s",
			len(findings), problems(findings))
	}
	for _, want := range []string{"multisig", "destinations", "period", "amount"} {
		found := false
		for _, finding := range findings {
			if finding.Check == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no finding for %q: %s", want, problems(findings))
		}
	}
}

// The exposure note has to describe the worst case, not today's leftover. An
// operator deciding whether to trust a boundary needs the cap.
func TestExposureNoteDescribesTheCapNotTheBalance(t *testing.T) {
	limit := soundLimit()
	limit.Remaining = 1 // nearly spent for this period

	note := ExposureNote(limit)
	if !strings.Contains(note, "500000000") || !strings.Contains(note, "per daily") {
		t.Fatalf("the note does not describe the recurring cap: %q", note)
	}
	if strings.Contains(note, "refilling indefinitely") != true {
		t.Errorf("a recurring limit does not say it refills: %q", note)
	}

	once := soundLimit()
	once.Period = OneTime
	if note = ExposureNote(once); !strings.Contains(note, "never again") {
		t.Errorf("a one-time limit does not say it is one-time: %q", note)
	}
}
