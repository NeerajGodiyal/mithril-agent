package policyauthority

import (
	"errors"

	"github.com/Overclock-Validator/mithril-agent/submitter"
)

// ValidateJupiterRecoveryPolicy checks the protected execution envelope shared
// by an authority policy and its recovery policy. It does not read recovery
// evidence or grant permission to sign or submit.
func ValidateJupiterRecoveryPolicy(authority Policy, recovery submitter.Policy) error {
	if err := authority.Validate(); err != nil {
		return err
	}
	if err := submitter.ValidateJupiterPolicy(recovery); err != nil {
		return err
	}
	if authority.JupiterProviders == nil || *authority.JupiterProviders != recovery.Evidence {
		return errors.New("authority and submitter evidence providers do not match")
	}
	signing := authority.TransactionPolicy
	if signing.Jupiter == nil || recovery.Jupiter == nil ||
		*signing.Jupiter != *recovery.Jupiter ||
		recovery.Cluster != signing.Cluster ||
		recovery.Profile != signing.Profile ||
		recovery.ProfileFingerprint != signing.ProfileFingerprint ||
		recovery.Source != signing.Source ||
		recovery.MaxLamports != signing.MaxLamports ||
		recovery.MaxInputTokenAmount != signing.MaxInputTokenAmount ||
		recovery.MaxFeeLamports != signing.MaxFeeLamports ||
		recovery.ScheduleWindowSeconds != signing.ScheduleWindowSeconds ||
		recovery.ScheduleAnchorUnix != signing.ScheduleAnchorUnix ||
		recovery.MaxBlockHeightWindow != signing.MaxBlockHeightWindow ||
		recovery.SubmitterPublicKey != signing.SubmitterPublicKey ||
		recovery.AttestationPublicKey != signing.AttestationPublicKey {
		return errors.New("signer and submitter policies do not match")
	}
	return nil
}

func pendingStrategyRecoveryPolicy(authority Policy, policies []submitter.Policy) (submitter.Policy, error) {
	var selected submitter.Policy
	digest := ""
	for _, policy := range policies {
		hash, err := strategyRecoveryPolicyHash(policy)
		if err != nil {
			return submitter.Policy{}, err
		}
		if err := ValidateJupiterRecoveryPolicy(authority, policy); err != nil {
			continue
		}
		if digest != "" && digest != hash {
			return submitter.Policy{}, errors.New("pending strategy recovery policy is ambiguous")
		}
		selected, digest = policy, hash
	}
	if digest == "" {
		return submitter.Policy{}, errors.New("pending strategy recovery policy is unavailable")
	}
	return selected, nil
}
