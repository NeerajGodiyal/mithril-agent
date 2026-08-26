package txflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestV0FeeAndSimulationEvidence(t *testing.T) {
	signer := solana.Encode(bytes.Repeat([]byte{1}, 32))
	program := solana.Encode(bytes.Repeat([]byte{2}, 32))
	blockhash := solana.Encode(bytes.Repeat([]byte{3}, 32))
	message, err := solana.BuildV0Message(signer, blockhash, []solana.Instruction{{
		Program:  program,
		Accounts: []solana.AccountMeta{{Address: signer, Signer: true, Writable: true}},
		Data:     []byte{1},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := sha256.Sum256([]byte("simulation logs"))
	node := &fakeProvider{identity: "node", v0Simulation: solanarpc.LegacySimulation{
		ContextSlot: 120, UnitsConsumed: 50_000, LogsSHA256: hex.EncodeToString(logs[:]),
	}}
	primary := &fakeProvider{identity: "primary", v0Fee: solanarpc.FeeQuote{
		ContextSlot: 120, Lamports: 5_000,
	}}
	secondary := &fakeProvider{identity: "secondary", v0Fee: solanarpc.FeeQuote{
		ContextSlot: 121, Lamports: 5_000,
	}}
	lifecycle, err := New(node, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := lifecycle.FeeForV0Message(t.Context(), message, nil, signer, 100)
	if err != nil {
		t.Fatal(err)
	}
	if fee.Lamports != 5_000 || fee.PrimaryContextSlot != 120 || fee.SecondaryContextSlot != 121 {
		t.Fatalf("unexpected fee evidence: %+v", fee)
	}
	simulation, err := lifecycle.SimulateV0(t.Context(), message, nil, signer, 100)
	if err != nil {
		t.Fatal(err)
	}
	if simulation.ProviderIdentity != "node" || simulation.ContextSlot != 120 ||
		simulation.UnitsConsumed != 50_000 {
		t.Fatalf("unexpected simulation evidence: %+v", simulation)
	}
}

func TestV0EvidenceFailsClosed(t *testing.T) {
	signer := solana.Encode(bytes.Repeat([]byte{1}, 32))
	program := solana.Encode(bytes.Repeat([]byte{2}, 32))
	message, err := solana.BuildV0Message(
		signer, solana.Encode(bytes.Repeat([]byte{3}, 32)),
		[]solana.Instruction{{Program: program, Data: []byte{1}}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	logs := sha256.Sum256([]byte("logs"))
	node := &fakeProvider{identity: "node", v0Simulation: solanarpc.LegacySimulation{
		ContextSlot: 99, LogsSHA256: hex.EncodeToString(logs[:]),
	}}
	primary := &fakeProvider{identity: "primary", v0Fee: solanarpc.FeeQuote{
		ContextSlot: 120, Lamports: 5_000,
	}}
	secondary := &fakeProvider{identity: "secondary", v0Fee: solanarpc.FeeQuote{
		ContextSlot: 120, Lamports: 6_000,
	}}
	lifecycle, err := New(node, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.FeeForV0Message(t.Context(), message, nil, signer, 100); err == nil {
		t.Fatal("provider fee disagreement was accepted")
	}
	if _, err := lifecycle.SimulateV0(t.Context(), message, nil, signer, 100); err == nil {
		t.Fatal("stale Mithril simulation was accepted")
	}
	wrongSigner := solana.Encode(bytes.Repeat([]byte{9}, 32))
	if _, err := lifecycle.FeeForV0Message(t.Context(), message, nil, wrongSigner, 100); err == nil {
		t.Fatal("wrong signer was accepted")
	}
}
