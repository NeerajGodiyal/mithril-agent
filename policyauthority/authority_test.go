package policyauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestGrantLifetimeRejectsOverflow(t *testing.T) {
	if _, err := grantLifetime(math.MaxUint64); err == nil {
		t.Fatal("accepted a grant lifetime that overflows time.Duration")
	}
}

func TestPolicyRequiresProtectedProvidersForJupiter(t *testing.T) {
	owner := solana.Encode(bytes.Repeat([]byte{1}, 32))
	route := jupiterswap.Policy{
		Owner: owner, InputMint: orcaswap.WrappedSOLMint,
		OutputMint:     solana.Encode(bytes.Repeat([]byte{2}, 32)),
		MaxInputAmount: 10, MinOutputAmount: 1, MaxSlippageBPS: 50,
		MaxComputeUnits: 300_000, MaxComputeUnitPriceMicroLamport: 10_000,
		MaxFeeLamports: 20_000, MaxTokenAccountRentLamports: 3_000_000,
		RouteGuard: jupiterAuthorityRouteGuard(),
	}
	fingerprint, err := route.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	authority := sha256.Sum256([]byte("authority"))
	authorityPublic, err := riskgrant.PublicKeyHex(ed25519.NewKeyFromSeed(authority[:]))
	if err != nil {
		t.Fatal(err)
	}
	submitter := sha256.Sum256([]byte("submitter"))
	submitterPublic, err := sealedtx.PublicKey(hex.EncodeToString(submitter[:]))
	if err != nil {
		t.Fatal(err)
	}
	attestation := sha256.Sum256([]byte("attestation"))
	attestationPublic := solana.Encode(
		ed25519.NewKeyFromSeed(attestation[:]).Public().(ed25519.PublicKey),
	)
	policy := Policy{
		TransactionPolicy: signer.Policy{
			Cluster: "mainnet-beta", Profile: jupiterswap.ProfileName,
			ProfileVersion: jupiterswap.ProfileVersion, ProfileFingerprint: fingerprint,
			Source: owner, MaxLamports: 10, MaxFeeLamports: 20_000,
			DailyDebitCapLamports:   3_020_010,
			AuthorizationLedgerPath: filepath.Join(t.TempDir(), "authorization.jsonl"),
			ScheduleWindowSeconds:   3_600,
			ScheduleAnchorUnix:      time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC).Unix(),
			MaxBlockHeightWindow:    150, RiskAuthorityKeyID: "authority",
			RiskAuthorityPublicKey: authorityPublic, SubmitterPublicKey: submitterPublic,
			AttestationPublicKey: attestationPublic,
			Jupiter:              &route,
		},
		JupiterProviders: &proposalcheck.ProviderBindings{
			PrimaryTrustDomain: "primary", PrimaryOriginSHA256: strings.Repeat("1", 64),
			SecondaryTrustDomain: "secondary", SecondaryOriginSHA256: strings.Repeat("2", 64),
			ArchiveProbeSignature: solana.Encode(bytes.Repeat([]byte{7}, 64)),
		},
		OperatorApprover:  solana.Encode(bytes.Repeat([]byte{8}, 32)),
		GrantLifetimeSecs: 30,
	}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	withoutProviders := policy
	withoutProviders.JupiterProviders = nil
	if err := withoutProviders.Validate(); err == nil {
		t.Fatal("Jupiter authority policy accepted missing provider bindings")
	}
	withoutApprover := policy
	withoutApprover.OperatorApprover = ""
	if err := withoutApprover.Validate(); err == nil {
		t.Fatal("Jupiter authority policy accepted a missing operator approver")
	}
}
