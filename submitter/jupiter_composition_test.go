package submitter_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/operatorapproval"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

func TestJupiterAuthorityCustodyAndSubmitterBoundariesCompose(t *testing.T) {
	submitterPolicy, submitterKey, request, _ := submitter.JupiterSubmitterFixture(t)
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	riskSeed := sha256.Sum256([]byte("Jupiter composed risk authority"))
	riskKey := ed25519.NewKeyFromSeed(riskSeed[:])
	riskPublic, err := riskgrant.PublicKeyHex(riskKey)
	if err != nil {
		t.Fatal(err)
	}
	signerPolicy := signer.Policy{
		Cluster: submitterPolicy.Cluster, Profile: submitterPolicy.Profile,
		ProfileVersion:          jupiterswap.ProfileVersion,
		ProfileFingerprint:      submitterPolicy.ProfileFingerprint,
		Source:                  submitterPolicy.Source,
		MaxLamports:             submitterPolicy.MaxLamports,
		MaxFeeLamports:          submitterPolicy.MaxFeeLamports,
		DailyDebitCapLamports:   10_000_000,
		AuthorizationLedgerPath: filepath.Join(ledgerDir, "authorization.jsonl"),
		ScheduleWindowSeconds:   submitterPolicy.ScheduleWindowSeconds,
		ScheduleAnchorUnix:      submitterPolicy.ScheduleAnchorUnix,
		MaxBlockHeightWindow:    submitterPolicy.MaxBlockHeightWindow,
		RiskAuthorityKeyID:      "Jupiter composed risk authority",
		RiskAuthorityPublicKey:  riskPublic,
		SubmitterPublicKey:      submitterPolicy.SubmitterPublicKey,
		AttestationPublicKey:    submitterPolicy.AttestationPublicKey,
		Jupiter:                 submitterPolicy.Jupiter,
	}
	approvalSeed := sha256.Sum256([]byte("Jupiter composed operator approval"))
	approvalKey := ed25519.NewKeyFromSeed(approvalSeed[:])
	authorityPolicy := policyauthority.Policy{
		TransactionPolicy: signerPolicy,
		JupiterProviders:  request.JupiterProviders,
		OperatorApprover:  solana.Encode(approvalKey.Public().(ed25519.PublicKey)),
		GrantLifetimeSecs: 30,
	}
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	validated, err := signer.ValidateJupiterRequest(signerPolicy, request)
	if err != nil {
		t.Fatal(err)
	}
	review, err := operatorapproval.BuildReview(
		authorityPolicy.OperatorApprover, request, validated,
	)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := operatorapproval.Create(
		authorityPolicy.OperatorApprover, request, validated,
		solana.Encode(ed25519.Sign(approvalKey, []byte(review.Challenge))),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.RiskGrant, err = policyauthority.AuthorizeApproved(
		authorityPolicy, riskKey, request, approval, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	walletSeed := sha256.Sum256([]byte("Jupiter submitter wallet"))
	walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
	attestationSeed := sha256.Sum256([]byte("Jupiter response attestor"))
	attestationKey := ed25519.NewKeyFromSeed(attestationSeed[:])
	response, err := signer.AuthorizeAndSignJupiterFileKey(
		t.Context(), signerPolicy, walletKey, attestationKey, request, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := submitter.ValidateJupiterResponse(
		submitterPolicy, submitterKey, request, response,
	); err != nil {
		t.Fatal(err)
	}

	ledgerBefore, err := os.ReadFile(signerPolicy.AuthorizationLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	submitter.CheckJupiterComposedRecovery(t, submitterPolicy, submitterKey, request, response)
	ledgerAfter, err := os.ReadFile(signerPolicy.AuthorizationLedgerPath)
	if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("send/retry/reconciliation changed signer spending ledger: %v", err)
	}
}
