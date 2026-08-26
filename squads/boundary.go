package squads

import (
	"fmt"
	"slices"
)

// A boundary is only a boundary if it is aimed. A spending limit with no
// destination list lets funds leave for anywhere, and one whose Multisig config
// or vault index is not what the operator intended protects different funds.
// These all decode perfectly well, so they must be checked explicitly.

// Expectation is what the operator believes they configured. Every field is
// required: a check that silently skips an unspecified field would report a
// boundary as sound because it was asked less.
type Expectation struct {
	// Multisig is the Squads configuration account the limit must belong to.
	Multisig string
	// VaultIndex selects the asset-holding Vault PDA. It is a pointer because
	// index zero is valid and must not be confused with an omitted expectation.
	VaultIndex *uint8
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

	if expect.Multisig == "" || expect.VaultIndex == nil || expect.Destination == "" || expect.Mint == "" ||
		expect.MaxAmount == 0 || len(expect.AllowedPeriods) == 0 {
		add("expectation", "incomplete: every field must be stated, or this proves nothing")
		return findings
	}

	if limit.Multisig != expect.Multisig {
		add("multisig", "this limit belongs to a different Multisig config than the one expected")
	}
	if expect.VaultIndex != nil && limit.VaultIndex != *expect.VaultIndex {
		add("vault_index", "this limit selects a different asset-holding vault than the one expected")
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
	// MultisigAddress is the config account actually read, so a limit can be
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
	// RequiresMultisigProcess means the multisig is autonomous. Removal needs a
	// complete initiate/vote/execute process; one member's presence alone is
	// not proof that the process is currently live.
	RequiresMultisigProcess Revocability = "requires_multisig_process"
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
			"this limit belongs to a different Multisig config than the account that was read")
	}

	revocability := NotRevocableByOperator
	switch {
	case multisig.ConfigAuthority == expect.Owner:
		revocability = RevocableByOwner
	case multisig.Autonomous():
		revocability = RequiresMultisigProcess
		owner := slices.IndexFunc(multisig.Members, func(m Member) bool { return m.Key == expect.Owner })
		if owner < 0 {
			add("revocability",
				"the Multisig is autonomous and the operator is not a member, so they cannot vote to remove this limit")
			revocability = NotRevocableByOperator
		} else if multisig.Members[owner].Permissions&PermissionVote == 0 {
			add("revocability",
				"the Multisig is autonomous but the operator member has no vote permission")
			revocability = NotRevocableByOperator
		}
	default:
		add("config_authority",
			"a key other than the operator controls this vault's configuration, "+
				"so it can change the members and threshold and the operator cannot revoke")
	}

	findings = append(findings, VerifySpender(limit, expect.Spender)...)
	return revocability, findings
}

// VerifySpender checks the spending-limit membership without requiring the
// Multisig config. Membership is independent of Multisig membership in Squads,
// so an operator can prove the exact automation key with only the limit.
func VerifySpender(limit SpendingLimit, spender string) []Finding {
	var findings []Finding
	add := func(problem string) {
		findings = append(findings, Finding{Check: "members", Problem: problem})
	}
	if spender == "" {
		add("the expected spender was not stated")
		return findings
	}
	switch {
	case !slices.Contains(limit.Members, spender):
		add("the expected spender is not allowed to spend through this limit")
	case len(limit.Members) > 1:
		add(fmt.Sprintf(
			"%d keys may spend through this limit, not only the expected one",
			len(limit.Members)))
	}
	return findings
}

// ExposureNote states, in plain terms, the cap for each refill period. It
// deliberately describes the CAP rather than the remaining balance: an operator
// deciding whether to trust a boundary needs to know the recurring allowance,
// not today's leftover.
func ExposureNote(limit SpendingLimit) string {
	asset := "of mint " + limit.Mint
	if limit.IsNativeSOL() {
		asset = "lamports"
	}
	amount := fmt.Sprintf("at most %d %s", limit.Amount, asset)
	if limit.Period == OneTime {
		return amount + ", once, and never again"
	}
	return fmt.Sprintf("%s per %s, refilling indefinitely", amount, limit.Period)
}
