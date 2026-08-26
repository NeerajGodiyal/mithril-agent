package operatorapproval

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/offchainmsg"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestApprovalBindsExactReadableRequest(t *testing.T) {
	seed := sha256.Sum256([]byte("operator approval test"))
	key := ed25519.NewKeyFromSeed(seed[:])
	approver := solana.Encode(key.Public().(ed25519.PublicKey))
	ownerKey := sha256.Sum256([]byte("agent owner"))
	owner := solana.Encode(ownerKey[:])
	request := signer.Request{
		Cluster: "mainnet-beta", ActionID: digest("action"),
		ProfileFingerprint: digest("profile"), FeeLamports: 5_000,
		ScheduleWindowStartUnix: 1_800_000_000,
		ScheduleWindowEndUnix:   1_800_003_600,
		LastValidBlockHeight:    123_456,
		JupiterCandidate: &proposalcheck.Candidate{
			Policy: jupiterswap.Policy{Owner: owner},
		},
	}
	validated := signer.ValidatedRequest{
		MessageSHA256: digest("message"), InputMint: digestAddress("input"),
		OutputMint: digestAddress("output"), InputAmount: 1_000_000,
		MinimumOutput: 42_000_000, DebitLamports: 3_045_000,
	}
	review, err := BuildReview(approver, request, validated)
	if err != nil {
		t.Fatal(err)
	}
	if review.Approver != approver || review.RequestSHA256 == "" ||
		review.MaximumNativeDebitLamports != validated.DebitLamports {
		t.Fatalf("review = %+v", review)
	}
	sealed, err := offchainmsg.Envelope(review.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := Create(
		approver, request, validated, solana.Encode(ed25519.Sign(key, sealed)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(approver, request, validated, approval); err != nil {
		t.Fatal(err)
	}

	changed := request
	changed.FeeLamports++
	if err := Verify(approver, changed, validated, approval); err == nil {
		t.Fatal("approval accepted a changed request")
	}
	changed = request
	changed.LastValidBlockHeight++
	if err := Verify(approver, changed, validated, approval); err == nil {
		t.Fatal("approval accepted a changed lifetime")
	}
	wrong := approval
	wrong.SignatureBase58 = solana.Encode(ed25519.Sign(key, []byte(review.Challenge)))
	if err := Verify(approver, request, validated, wrong); err != nil {
		t.Fatalf("browser-wallet raw signature was rejected: %v", err)
	}
}

func digest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func digestAddress(label string) string {
	sum := sha256.Sum256([]byte(label))
	return solana.Encode(sum[:])
}
