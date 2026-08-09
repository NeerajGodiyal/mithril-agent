package signer

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

// The signer exists to be the second, independent opinion: the proposer builds
// a transaction, the signer re-decodes it and refuses anything the policy did
// not authorise. Of everything it checks, the destination matters most — it is
// the difference between paying the operator and paying somebody else.
//
// Nothing tested it. Deleting the destination comparison from signer.go left
// every test in every package that imports this one passing, so a signer that
// would sign a transfer to ANY address looked exactly like a working build.
//
// The reason it was missed is worth keeping: the risk grant hashes every
// request field, so a test that edits the request and does NOT re-issue the
// grant is refused at grant verification and never reaches the policy check.
// Such a test asserts err != nil and proves nothing about the destination. This
// one re-issues the grant deliberately, so the ONLY thing left to refuse the
// transfer is the binding under test.
func TestSignerRefusesATransferToAnyOtherDestination(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()

	attacker := solana.Encode(signerTestKey("attacker").Public().(ed25519.PublicKey))
	if attacker == policy.Destination {
		t.Fatal("fixture error: the substitute destination equals the approved one")
	}
	// Identical in every respect except who gets paid.
	message, err := solana.BuildTransferMessage(policy.Source, attacker, request.RecentBlockhash, 42)
	if err != nil {
		t.Fatal(err)
	}
	request.MessageBase64 = base64.StdEncoding.EncodeToString(message)
	grantSignerRequest(t, policy, &request, now)

	_, err = AuthorizeAndSign(policy, privateKey, request, now)
	if err == nil {
		t.Fatal("the signer signed a transfer to a destination the policy never approved")
	}
	// The message is asserted, not merely the existence of an error: an error
	// from an earlier, unrelated guard would otherwise look like proof. That
	// substitution is exactly how this invariant went untested.
	if !strings.Contains(err.Error(), "does not match policy and request") {
		t.Fatalf("refused, but not for the destination: %v", err)
	}
}

// The approved destination must still be signable, or the test above could be
// satisfied by a signer that refuses everything.
func TestSignerStillSignsTheApprovedDestination(t *testing.T) {
	policy, privateKey, request := signerFixture(t)
	now := time.Unix(request.ScheduleWindowStartUnix+1, 0).UTC()
	if _, err := AuthorizeAndSign(policy, privateKey, request, now); err != nil {
		t.Fatalf("the approved destination was refused: %v", err)
	}
}
