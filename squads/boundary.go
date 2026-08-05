package squads

import (
	"fmt"
	"slices"
)

// A boundary is only a boundary if it is aimed. A spending limit with no
// destination list lets funds leave for anywhere, and one whose multisig is not
// the vault the operator believes they configured is protecting somebody else's
// money. Both decode perfectly well, so neither is caught by decoding — they
// have to be checked against what the operator actually intended.

// Expectation is what the operator believes they configured. Every field is
// required: a check that silently skips an unspecified field would report a
// boundary as sound because it was asked less.
type Expectation struct {
	// Multisig is the vault the limit must belong to.
	Multisig string
	// Destination is the only address funds may leave for — normally the
	// dedicated agent account.
	Destination string
	// Mint is the asset being capped, or NativeMint for SOL.
	Mint string
	// MaxAmount is the largest per-period cap the operator will accept. An
	// on-chain limit above this is a finding, not a detail.
	MaxAmount uint64
	// AllowedPeriods are the refill periods the operator will accept. A limit
	// that refills faster than expected is a larger exposure than it appears.
	AllowedPeriods []Period
}

// Finding is one specific reason the boundary is not what was expected.
type Finding struct {
	Check   string `json:"check"`
	Problem string `json:"problem"`
}

// Verify reports every way the on-chain limit falls short of the expectation.
// It returns all findings rather than the first, because an operator fixing a
// funding boundary needs the whole list before they touch it.
func Verify(limit SpendingLimit, expect Expectation) []Finding {
	var findings []Finding
	add := func(check, problem string) {
		findings = append(findings, Finding{Check: check, Problem: problem})
	}

	if expect.Multisig == "" || expect.Destination == "" || expect.Mint == "" ||
		expect.MaxAmount == 0 || len(expect.AllowedPeriods) == 0 {
		add("expectation", "incomplete: every field must be stated, or this proves nothing")
		return findings
	}

	if limit.Multisig != expect.Multisig {
		add("multisig", "this limit belongs to a different vault than the one expected")
	}
	if limit.Mint != expect.Mint {
		add("mint", "this limit caps a different asset than the one expected")
	}

	switch {
	case len(limit.Destinations) == 0:
		add("destinations", "empty, so funds may leave for any address: this is not a boundary")
	case !slices.Contains(limit.Destinations, expect.Destination):
		add("destinations", "the expected destination is not allowed by this limit")
	case len(limit.Destinations) > 1:
		add("destinations", fmt.Sprintf(
			"%d destinations are allowed, so funds may also leave for addresses "+
				"other than the expected one", len(limit.Destinations)))
	}

	if !limit.Period.Valid() {
		add("period", "the refill period is not one this program defines")
	} else if !slices.Contains(expect.AllowedPeriods, limit.Period) {
		add("period", fmt.Sprintf("refills %s, which is not an accepted period", limit.Period))
	}

	if limit.Amount > expect.MaxAmount {
		add("amount", fmt.Sprintf(
			"caps %d per period, above the %d the operator accepts",
			limit.Amount, expect.MaxAmount))
	}
	if limit.Amount == 0 {
		add("amount", "is zero, which the program does not permit and cannot be a live limit")
	}
	if len(limit.Members) == 0 {
		add("members", "nobody can spend through this limit")
	}
	return findings
}

// Sound reports whether the limit is a boundary the operator can rely on.
func Sound(limit SpendingLimit, expect Expectation) bool {
	return len(Verify(limit, expect)) == 0
}

// Verify answers "how much can leave, and to where". It cannot answer "can I
// take this away", because that is decided by the Multisig account rather than
// the limit. A boundary the operator cannot remove is not a bounded exposure,
// it is a fixed one — so revocability has to be proven, not assumed.

// ControlExpectation is what the operator believes about control of the vault.
type ControlExpectation struct {
	// MultisigAddress is the vault account actually read, so a limit can be
	// tied to the multisig it claims rather than to one supplied separately.
	MultisigAddress string
	// Owner is the operator's own wallet: the key they expect to be able to
	// revoke with, and the key they expect to control config.
	Owner string
	// Spender is the key expected to be allowed to spend through the limit,
	// normally the dedicated agent account.
	Spender string
}

// Revocability is how the operator can take the limit away.
type Revocability string

const (
	// RevocableByOwner means the operator's own key is the config authority
	// and can remove the limit with one signature.
	RevocableByOwner Revocability = "revocable_by_owner"
	// RevocableByVote means the multisig is autonomous, so removal goes
	// through the member voting path rather than a single signature.
	RevocableByVote Revocability = "revocable_by_vote"
	// NotRevocableByOperator means some other key controls config. That key
	// can also change the members and threshold.
	NotRevocableByOperator Revocability = "not_revocable_by_operator"
)

// VerifyControl reports every way control of the vault differs from what the
// operator expects, and how the limit can be revoked.
func VerifyControl(
	multisig Multisig, limit SpendingLimit, expect ControlExpectation,
) (Revocability, []Finding) {
	var findings []Finding
	add := func(check, problem string) {
		findings = append(findings, Finding{Check: check, Problem: problem})
	}

	if expect.MultisigAddress == "" || expect.Owner == "" || expect.Spender == "" {
		add("control_expectation", "incomplete: every field must be stated, or this proves nothing")
		return NotRevocableByOperator, findings
	}
	if limit.Multisig != expect.MultisigAddress {
		add("multisig_binding",
			"this limit belongs to a different vault than the multisig account that was read")
	}

	revocability := NotRevocableByOperator
	switch {
	case multisig.ConfigAuthority == expect.Owner:
		revocability = RevocableByOwner
	case multisig.Autonomous():
		revocability = RevocableByVote
		if !slices.ContainsFunc(multisig.Members, func(m Member) bool { return m.Key == expect.Owner }) {
			add("revocability",
				"the vault is autonomous and the operator is not a member, so they cannot vote to remove this limit")
			revocability = NotRevocableByOperator
		}
	default:
		add("config_authority",
			"a key other than the operator controls this vault's configuration, "+
				"so it can change the members and threshold and the operator cannot revoke")
	}

	// Who may spend is a property of the limit, but it only means anything
	// alongside who controls the vault, so it is checked here rather than in
	// Verify, which deliberately knows nothing about the operator's own keys.
	switch {
	case !slices.Contains(limit.Members, expect.Spender):
		add("members", "the expected spender is not allowed to spend through this limit")
	case len(limit.Members) > 1:
		add("members", fmt.Sprintf(
			"%d keys may spend through this limit, not only the expected one",
			len(limit.Members)))
	}
	return revocability, findings
}

// Unlimited is the sentinel a spending limit uses for "no cap". Printed as a
// number it reads like a specific, very large allowance; it has to be named,
// because the difference between a large cap and no cap is the whole question.
const Unlimited = ^uint64(0)

// ExposureNote states, in plain terms, the most that can leave the vault. It
// deliberately describes the CAP rather than the remaining balance: an operator
// deciding whether to trust a boundary needs to know the worst case, not
// today's leftover.
func ExposureNote(limit SpendingLimit) string {
	asset := "of mint " + limit.Mint
	if limit.IsNativeSOL() {
		asset = "lamports"
	}
	amount := fmt.Sprintf("at most %d %s", limit.Amount, asset)
	if limit.Amount == Unlimited {
		amount = "an UNLIMITED amount " + asset
	}
	if limit.Period == OneTime {
		return amount + ", once, and never again"
	}
	return fmt.Sprintf("%s per %s, refilling indefinitely", amount, limit.Period)
}
