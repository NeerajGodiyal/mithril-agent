package policyauthority

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

type Policy struct {
	TransactionPolicy signer.Policy `json:"transaction_policy"`
	GrantLifetimeSecs uint64        `json:"grant_lifetime_seconds"`
}

func (p Policy) Validate() error {
	if err := p.TransactionPolicy.Validate(); err != nil {
		return err
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
	if err := policy.Validate(); err != nil {
		return riskgrant.Grant{}, err
	}
	if request.RiskGrant.SignatureBase64 != "" || request.RiskGrant.Claims.Version != 0 {
		return riskgrant.Grant{}, errors.New("risk request already contains a grant")
	}
	configuredPublicKey, err := riskgrant.DecodePublicKey(
		policy.TransactionPolicy.RiskAuthorityPublicKey,
	)
	if err != nil {
		return riskgrant.Grant{}, err
	}
	actualPublicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(actualPublicKey, configuredPublicKey) {
		return riskgrant.Grant{}, errors.New("risk authority key does not match policy")
	}
	validated, err := signer.ValidateRequest(policy.TransactionPolicy, request)
	if err != nil {
		return riskgrant.Grant{}, err
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

func grantLifetime(seconds uint64) (time.Duration, error) {
	if seconds < 5 || seconds > uint64(riskgrant.MaxLifetime/time.Second) {
		return 0, errors.New("risk grant lifetime must be between 5 and 300 seconds")
	}
	return time.Duration(seconds) * time.Second, nil
}
