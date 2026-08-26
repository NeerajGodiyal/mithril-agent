package policyauthority

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

type Policy struct {
	TransactionPolicy signer.Policy                   `json:"transaction_policy"`
	JupiterProviders  *proposalcheck.ProviderBindings `json:"jupiter_providers,omitempty"`
	OperatorApprover  string                          `json:"operator_approver,omitempty"`
	GrantLifetimeSecs uint64                          `json:"grant_lifetime_seconds"`
}

func (p Policy) Validate() error {
	if p.TransactionPolicy.Jupiter == nil {
		if p.JupiterProviders != nil || p.OperatorApprover != "" {
			return errors.New("Devnet risk policy contains Mainnet provider bindings")
		}
		if err := p.TransactionPolicy.Validate(); err != nil {
			return err
		}
	} else {
		if p.JupiterProviders == nil || p.OperatorApprover == "" ||
			p.OperatorApprover == p.TransactionPolicy.Source {
			return errors.New("Jupiter risk policy requires protected providers and a separate operator approver")
		}
		if _, err := solana.Decode32(p.OperatorApprover); err != nil {
			return errors.New("Jupiter operator approver is invalid")
		}
		if err := signer.ValidateJupiterPolicy(p.TransactionPolicy); err != nil {
			return err
		}
		if err := p.JupiterProviders.ValidateArchiveProbe(); err != nil {
			return err
		}
	}
	_, err := grantLifetime(p.GrantLifetimeSecs)
	return err
}

func Authorize(
	policy Policy,
	privateKey ed25519.PrivateKey,
	request signer.Request,
	now time.Time,
) (riskgrant.Grant, error) {
	return authorize(policy, privateKey, request, nil, now)
}

// AuthorizeApproved is the Mainnet entry point. The wallet approval is
// detached from the signer request so it cannot become transaction authority;
// the risk grant proves that this separate boundary verified it.
func AuthorizeApproved(
	policy Policy,
	privateKey ed25519.PrivateKey,
	request signer.Request,
	approval operatorapproval.Approval,
	now time.Time,
) (riskgrant.Grant, error) {
	return authorize(policy, privateKey, request, &approval, now)
}

func authorize(
	policy Policy,
	privateKey ed25519.PrivateKey,
	request signer.Request,
	approval *operatorapproval.Approval,
	now time.Time,
) (riskgrant.Grant, error) {
	if err := policy.Validate(); err != nil {
		return riskgrant.Grant{}, err
	}
	if request.RiskGrant.SignatureBase64 != "" || request.RiskGrant.Claims.Version != 0 {
		return riskgrant.Grant{}, errors.New("risk request already contains a grant")
	}
	if err := validatePrivateKey(policy.TransactionPolicy, privateKey); err != nil {
		return riskgrant.Grant{}, err
	}
	var validated signer.ValidatedRequest
	var err error
	if policy.TransactionPolicy.Jupiter != nil {
		if request.JupiterProviders == nil ||
			*request.JupiterProviders != *policy.JupiterProviders {
			return riskgrant.Grant{}, errors.New("Jupiter request provider bindings do not match protected policy")
		}
		validated, err = signer.ValidateJupiterRequest(policy.TransactionPolicy, request)
	} else {
		if approval != nil {
			return riskgrant.Grant{}, errors.New("Devnet risk request contains an operator approval")
		}
		validated, err = signer.ValidateRequest(policy.TransactionPolicy, request)
	}
	if err != nil {
		return riskgrant.Grant{}, err
	}
	if policy.TransactionPolicy.Jupiter != nil {
		if approval == nil {
			return riskgrant.Grant{}, errors.New("Jupiter risk request requires exact operator approval")
		}
		if err := operatorapproval.Verify(
			policy.OperatorApprover, request, validated, *approval,
		); err != nil {
			return riskgrant.Grant{}, err
		}
	}
	lifetime, err := grantLifetime(policy.GrantLifetimeSecs)
	if err != nil {
		return riskgrant.Grant{}, err
	}
	binding, err := signer.RiskBinding(request, validated.MessageSHA256)
	if err != nil {
		return riskgrant.Grant{}, err
	}
	return riskgrant.Sign(
		privateKey,
		policy.TransactionPolicy.RiskAuthorityKeyID,
		binding,
		now.UTC(),
		lifetime,
	)
}

// PrepareJupiterRequest rechecks one immutable Mainnet candidate against
// protected providers and returns the exact unsigned, ungranted signer request.
// It does not grant authority, sign, or submit.
func PrepareJupiterRequest(
	ctx context.Context,
	policy Policy,
	candidate proposalcheck.Candidate,
	scheduleWindowStartUnix int64,
	now time.Time,
	evidence proposalcheck.Evidence,
	primary, secondary proposalcheck.FinalizedSlotReader,
) (signer.Request, error) {
	if err := policy.Validate(); err != nil {
		return signer.Request{}, err
	}
	if policy.TransactionPolicy.Jupiter == nil {
		return signer.Request{}, errors.New("Jupiter risk policy is invalid")
	}
	checked, err := proposalcheck.Recheck(
		ctx, evidence, primary, secondary,
		*policy.TransactionPolicy.Jupiter, *policy.JupiterProviders, candidate,
	)
	if err != nil {
		return signer.Request{}, err
	}
	request, err := signer.RequestFromJupiterRecheck(
		policy.TransactionPolicy, *policy.JupiterProviders, candidate, checked,
		scheduleWindowStartUnix,
	)
	if err != nil {
		return signer.Request{}, err
	}
	now = now.UTC()
	if nowUnix := now.Unix(); nowUnix < request.ScheduleWindowStartUnix ||
		nowUnix >= request.ScheduleWindowEndUnix {
		return signer.Request{}, errors.New("Jupiter risk request is outside its schedule window")
	}
	return request, nil
}

func validatePrivateKey(policy signer.Policy, privateKey ed25519.PrivateKey) error {
	publicKey, err := riskgrant.PublicKeyHex(privateKey)
	if err != nil {
		return err
	}
	if publicKey != policy.RiskAuthorityPublicKey {
		return errors.New("risk authority key does not match policy")
	}
	return nil
}

func grantLifetime(seconds uint64) (time.Duration, error) {
	if seconds < 5 || seconds > uint64(riskgrant.MaxLifetime/time.Second) {
		return 0, errors.New("risk grant lifetime must be between 5 and 300 seconds")
	}
	return time.Duration(seconds) * time.Second, nil
}
