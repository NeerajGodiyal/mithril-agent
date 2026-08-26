package submitter

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type submitterTestNode struct {
	returned    string
	err         error
	genesis     string
	genesisErr  error
	height      uint64
	heightErr   error
	transaction []byte
	minSlot     uint64
	duringSend  func()
	duringRead  func()
}

type submitterTestGate struct {
	allowed bool
	err     error
	seen    *string
	inside  *bool
}

func (gate submitterTestGate) WithRecoverySendBarrier(
	actionID string,
	operation func() error,
) (bool, error) {
	if gate.seen != nil {
		*gate.seen = actionID
	}
	if gate.err != nil || !gate.allowed {
		return !gate.allowed, gate.err
	}
	if gate.inside != nil {
		*gate.inside = true
		defer func() { *gate.inside = false }()
	}
	return false, operation()
}

func (n *submitterTestNode) SendTransaction(
	_ context.Context,
	transaction []byte,
	minContextSlot uint64,
) (string, error) {
	if n.duringSend != nil {
		n.duringSend()
	}
	n.transaction = bytes.Clone(transaction)
	n.minSlot = minContextSlot
	return n.returned, n.err
}

func (n *submitterTestNode) GenesisHash(context.Context) (string, error) {
	if n.duringRead != nil {
		n.duringRead()
	}
	if n.genesis == "" {
		return solana.DevnetGenesisHash, n.genesisErr
	}
	return n.genesis, n.genesisErr
}

func (n *submitterTestNode) BlockHeight(context.Context) (uint64, error) {
	if n.duringRead != nil {
		n.duringRead()
	}
	if n.height == 0 {
		return 100, n.heightErr
	}
	return n.height, n.heightErr
}

func TestSubmitAuthenticatesAndSendsSealedTransaction(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	inside := false
	sentInside := false
	readInside := false
	node := &submitterTestNode{
		returned: response.Signature,
		duringRead: func() {
			readInside = inside
		},
		duringSend: func() {
			sentInside = inside
		},
	}
	gate := submitterTestGate{allowed: true, inside: &inside}
	submission, err := submitWithGate(t.Context(), policy, privateKey, node, gate, response, 90)
	if err != nil {
		t.Fatal(err)
	}
	if submission.State != txflow.StateAccepted ||
		submission.Signature != response.Signature ||
		node.minSlot != 90 || !bytes.Equal(node.transaction, transaction) || !sentInside || !readInside {
		t.Fatalf("submission=%+v min_slot=%d", submission, node.minSlot)
	}

	node.err = errors.New("response lost")
	submission, err = submitWithGate(t.Context(), policy, privateKey, node, gate, response, 90)
	if err != nil || submission.State != txflow.StateAmbiguous {
		t.Fatalf("ambiguous submission = %+v, %v", submission, err)
	}
	node.err = nil
	node.returned = solana.Encode(bytes.Repeat([]byte{8}, 64))
	submission, err = submitWithGate(t.Context(), policy, privateKey, node, gate, response, 90)
	if err != nil || submission.State != txflow.StateAmbiguous {
		t.Fatalf("mismatched signature submission = %+v, %v", submission, err)
	}
}

func TestSubmitRejectsAuthorityFromAnotherControlFile(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	correctDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrongDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{correctDir, wrongDir} {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	policy.ControlStatePath = filepath.Join(correctDir, "control.json")
	wrongPath := filepath.Join(wrongDir, "control.json")
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		wrongPath, policy.ProfileFingerprint,
		now.Add(-time.Second), now.Add(time.Minute), 1, "unrelated test authority",
	); err != nil {
		t.Fatal(err)
	}
	wrongGate, err := control.NewStateFile(wrongPath, policy.ProfileFingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := wrongGate.WithSendBarrier(response.ActionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("prepare unrelated recovery gate = blocked=%t, err=%v", blocked, err)
	}
	node := &submitterTestNode{returned: response.Signature}
	if _, err := Submit(
		t.Context(), policy, privateKey, node, response,
		response.BlockhashContextSlot,
	); !errors.Is(err, ErrControlBlocked) || len(node.transaction) != 0 {
		t.Fatalf("unrelated control gate authorized submission: sent=%t, err=%v", len(node.transaction) != 0, err)
	}
}

func TestSubmitUsesAuthorityFromItsPolicyControlFile(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	now := time.Now().UTC()
	if err := control.WriteDevnetActivation(
		policy.ControlStatePath, policy.ProfileFingerprint,
		now.Add(-time.Second), now.Add(time.Minute), 1, "test authority",
	); err != nil {
		t.Fatal(err)
	}
	gate, err := control.NewStateFile(
		policy.ControlStatePath, policy.ProfileFingerprint, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := gate.WithSendBarrier(response.ActionID, func() error { return nil }); err != nil || blocked {
		t.Fatalf("prepare policy recovery gate = blocked=%t, err=%v", blocked, err)
	}
	node := &submitterTestNode{returned: response.Signature}
	submission, err := Submit(
		t.Context(), policy, privateKey, node, response,
		response.BlockhashContextSlot,
	)
	if err != nil || submission.State != txflow.StateAccepted ||
		!bytes.Equal(node.transaction, transaction) {
		t.Fatalf("policy-bound submission = %+v, sent=%t, err=%v", submission, len(node.transaction) != 0, err)
	}
}

func TestSubmitRejectsBoundaryDrift(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	tests := map[string]func(*Policy, *string, *signer.Response, *submitterTestNode){
		"wrong key": func(_ *Policy, key *string, _ *signer.Response, _ *submitterTestNode) {
			wrong := sha256.Sum256([]byte("wrong"))
			*key = hex.EncodeToString(wrong[:])
		},
		"source policy": func(value *Policy, _ *string, _ *signer.Response, _ *submitterTestNode) {
			value.Source = solana.Encode(bytes.Repeat([]byte{9}, 32))
		},
		"amount policy": func(value *Policy, _ *string, _ *signer.Response, _ *submitterTestNode) {
			value.MaxLamports--
		},
		"metadata": func(_ *Policy, _ *string, value *signer.Response, _ *submitterTestNode) {
			value.SealedTransaction.Metadata.FeeLamports++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changedPolicy := policy
			changedKey := privateKey
			changedResponse := response
			node := &submitterTestNode{returned: response.Signature}
			mutate(&changedPolicy, &changedKey, &changedResponse, node)
			if _, err := submitWithGate(
				t.Context(),
				changedPolicy,
				changedKey,
				node,
				submitterTestGate{allowed: true},
				changedResponse,
				90,
			); err == nil {
				t.Fatal("drifted submitter request was accepted")
			}
		})
	}
}

func TestSubmitRechecksClusterAndLifetimeInsideSendBarrier(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	tests := map[string]func(*submitterTestNode){
		"wrong cluster": func(node *submitterTestNode) {
			node.genesis = solana.Encode(bytes.Repeat([]byte{8}, 32))
		},
		"cluster unavailable": func(node *submitterTestNode) {
			node.genesisErr = errors.New("unavailable")
		},
		"height unavailable": func(node *submitterTestNode) {
			node.heightErr = errors.New("unavailable")
		},
		"insufficient block-height headroom": func(node *submitterTestNode) {
			node.height = response.LastValidBlockHeight
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			inside := false
			readInside := false
			node := &submitterTestNode{
				returned:   response.Signature,
				duringRead: func() { readInside = inside },
			}
			mutate(node)
			if _, err := submitWithGate(
				t.Context(), policy, privateKey, node,
				submitterTestGate{allowed: true, inside: &inside}, response, 90,
			); err == nil {
				t.Fatal("invalid live submit boundary was accepted")
			}
			if !readInside || node.transaction != nil {
				t.Fatal("submit validation did not stay inside the send barrier")
			}
			if _, err := os.Lstat(recoveryPath(policy)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-send rejection left recovery evidence: %v", err)
			}
		})
	}
}

func TestSubmitRejectsOuterResponseDriftBeforeSend(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	tests := map[string]func(*signer.Response){
		"blockhash context": func(value *signer.Response) {
			value.BlockhashContextSlot++
		},
		"fee": func(value *signer.Response) {
			value.FeeLamports--
		},
		"last valid block height": func(value *signer.Response) {
			value.LastValidBlockHeight++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := response
			mutate(&changed)
			gateSeen := ""
			node := &submitterTestNode{returned: response.Signature}
			gate := submitterTestGate{allowed: true, seen: &gateSeen}
			if _, err := submitWithGate(t.Context(), policy, privateKey, node, gate, changed, 90); err == nil {
				t.Fatal("drifted outer response was accepted")
			}
			if gateSeen != "" || node.transaction != nil {
				t.Fatal("drifted response reached the send boundary")
			}
		})
	}
}

func TestSubmitRejectsResealedContextDriftBeforeSend(t *testing.T) {
	policy, privateKey, response, transaction := submitterFixture(t)
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	metadata := response.SealedTransaction.Metadata
	metadata.BlockhashContextSlot++
	response.BlockhashContextSlot = metadata.BlockhashContextSlot
	response.SealedTransaction, err = sealedtx.Seal(publicKey, metadata, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateSeen := ""
	node := &submitterTestNode{returned: response.Signature}
	if _, err := submitWithGate(
		t.Context(), policy, privateKey, node,
		submitterTestGate{allowed: true, seen: &gateSeen}, response, metadata.BlockhashContextSlot,
	); err == nil {
		t.Fatal("re-sealed context drift was accepted")
	}
	if gateSeen != "" || node.transaction != nil {
		t.Fatal("re-sealed context drift reached the send boundary")
	}
}

func TestSubmitRequiresMatchingLiveControlAuthority(t *testing.T) {
	policy, privateKey, response, _ := submitterFixture(t)
	for _, test := range []struct {
		name string
		gate submitterTestGate
	}{
		{name: "blocked", gate: submitterTestGate{}},
		{name: "inspection error", gate: submitterTestGate{err: errors.New("unreadable state")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := &submitterTestNode{returned: response.Signature}
			seen := ""
			test.gate.seen = &seen
			if _, err := submitWithGate(
				t.Context(), policy, privateKey, node, test.gate, response, 90,
			); err == nil {
				t.Fatal("submission without live control authority was accepted")
			}
			if seen != response.ActionID {
				t.Fatalf("control gate action ID = %q", seen)
			}
			if node.transaction != nil {
				t.Fatal("blocked submission reached the node")
			}
		})
	}
}

func submitterFixture(t *testing.T) (Policy, string, signer.Response, []byte) {
	t.Helper()
	controlDir := t.TempDir()
	sourceSeed := sha256.Sum256([]byte("source"))
	sourceKey := ed25519.NewKeyFromSeed(sourceSeed[:])
	source := solana.Encode(sourceKey.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destination := solana.Encode(
		ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey),
	)
	message, err := solana.BuildTransferMessage(
		source,
		destination,
		solana.Encode(bytes.Repeat([]byte{7}, 32)),
		42,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignTransferMessage(sourceKey, message)
	if err != nil {
		t.Fatal(err)
	}
	submitterSeed := sha256.Sum256([]byte("submitter"))
	privateKey := hex.EncodeToString(submitterSeed[:])
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	requestHash := sha256.Sum256([]byte("submitter request"))
	response := signer.Response{
		ActionID:             hex.EncodeToString(sha256.New().Sum(make([]byte, 0, 32))),
		RequestSHA256:        hex.EncodeToString(requestHash[:]),
		Signature:            solana.Encode(signature[:]),
		MessageSHA256:        hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: 90,
		FeeLamports:          5_000,
		LastValidBlockHeight: 200,
	}
	response.SealedTransaction, err = sealedtx.Seal(
		publicKey,
		sealedtx.Metadata{
			Version:              sealedtx.Version,
			Domain:               sealedtx.Domain,
			ActionID:             response.ActionID,
			MessageSHA256:        response.MessageSHA256,
			TransactionSHA256:    response.TransactionSHA256,
			Signature:            response.Signature,
			BlockhashContextSlot: response.BlockhashContextSlot,
			FeeLamports:          response.FeeLamports,
			LastValidBlockHeight: response.LastValidBlockHeight,
		},
		transaction,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response.SignerAttestation, err = signer.AttestResponse(sourceKey, publicKey, response)
	if err != nil {
		t.Fatal(err)
	}
	return Policy{
		Cluster:            "devnet",
		ProfileFingerprint: strings.Repeat("a", 64),
		ControlStatePath:   filepath.Join(controlDir, "control.json"),
		Source:             source,
		Destination:        destination,
		MaxLamports:        42,
		MaxFeeLamports:     5_000,
		SubmitterPublicKey: publicKey,
		Evidence: proposalcheck.ProviderBindings{
			PrimaryTrustDomain: "provider-one", PrimaryOriginSHA256: strings.Repeat("b", 64),
			SecondaryTrustDomain: "provider-two", SecondaryOriginSHA256: strings.Repeat("c", 64),
		},
	}, privateKey, response, transaction
}
