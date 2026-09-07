package txflow

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type walletProvider struct {
	*fakeProvider
	finalizedReads int
}

func (p *walletProvider) AccountSlice(context.Context, string, uint64, uint64, uint64) (solanarpc.AccountDataSlice, error) {
	return solanarpc.AccountDataSlice{}, errors.New("confirmed reads are not inventory evidence")
}

func (p *walletProvider) FinalizedAccountSlice(ctx context.Context, address string, slot, offset, length uint64) (solanarpc.AccountDataSlice, error) {
	p.finalizedReads++
	return p.fakeProvider.AccountSlice(ctx, address, slot, offset, length)
}

func TestObserveWalletRequiresFinalizedIndependentBalances(t *testing.T) {
	owner := solana.Encode(bytes.Repeat([]byte{4}, 32))
	mint := solana.Encode(bytes.Repeat([]byte{5}, 32))
	address, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"valid", "zero token", "genesis", "native disagreement", "token disagreement",
		"token owner", "token delegate", "token frozen", "missing token", "native stale", "token stale",
		"native ahead", "token ahead", "slot divergence", "zero slot", "overflow", "unsupported reader"} {
		t.Run(name, func(t *testing.T) {
			makeProvider := func(identity string) *walletProvider {
				data := make([]byte, 165)
				copy(data[:32], bytes.Repeat([]byte{5}, 32))
				copy(data[32:64], bytes.Repeat([]byte{4}, 32))
				binary.LittleEndian.PutUint64(data[64:72], 10)
				data[108] = 1
				return &walletProvider{fakeProvider: &fakeProvider{identity: identity,
					genesis: solana.MainnetBetaGenesisHash, finalized: 100, balanceSlot: 101, balance: 1000,
					accountSlices: map[string]solanarpc.AccountDataSlice{address: {
						ContextSlot: 102, Owner: orcaswap.TokenProgram, DataLength: 165, Data: data,
					}}}}
			}
			a, b := makeProvider("primary"), makeProvider("secondary")
			floor := uint64(100)
			switch name {
			case "zero token":
				binary.LittleEndian.PutUint64(a.accountSlices[address].Data[64:72], 0)
				binary.LittleEndian.PutUint64(b.accountSlices[address].Data[64:72], 0)
			case "genesis":
				b.genesis = solana.DevnetGenesisHash
			case "native disagreement":
				b.balance++
			case "token disagreement":
				b.accountSlices[address].Data[64]++
			case "token owner":
				a.accountSlices[address].Data[32]++
				b.accountSlices[address].Data[32]++
			case "token delegate":
				a.accountSlices[address].Data[72] = 1
				b.accountSlices[address].Data[72] = 1
			case "token frozen":
				a.accountSlices[address].Data[108] = 2
				b.accountSlices[address].Data[108] = 2
			case "missing token":
				b.accountSliceErr = errors.New("account missing")
			case "native stale":
				b.balanceSlot = 99
			case "native ahead":
				b.balanceSlot = 111
			case "token stale", "token ahead":
				value := b.accountSlices[address]
				value.ContextSlot = 99
				if name == "token ahead" {
					value.ContextSlot = 111
				}
				b.accountSlices[address] = value
			case "slot divergence":
				b.finalized = 111
			case "zero slot":
				b.finalized = 0
			case "overflow":
				floor = ^uint64(0)
			}
			var secondary EvidenceProvider = b
			if name == "unsupported reader" {
				secondary = b.fakeProvider
			}
			lifecycle, err := NewEvidenceLifecycle(a, secondary)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lifecycle.ObserveWallet(t.Context(), owner, mint, solana.MainnetBetaGenesisHash, floor, 10)
			if name == "valid" || name == "zero token" {
				want := uint64(10)
				if name == "zero token" {
					want = 0
				}
				if err != nil || got.NativeLamports != 1000 || got.TokenUnits != want ||
					got.Owner != owner || got.TokenAccount != address || got.MinimumContextSlot != 100 ||
					got.MaximumContextSlot != 110 || got.NativePrimarySlot != 101 || got.TokenSecondarySlot != 102 ||
					a.finalizedReads != 1 || b.finalizedReads != 1 {
					t.Fatalf("wallet observation = %+v, %v", got, err)
				}
			} else if err == nil || got != (WalletObservation{}) {
				t.Fatalf("invalid wallet observation accepted: %+v, %v", got, err)
			}
			if a.sendCalls != 0 || b.sendCalls != 0 {
				t.Fatal("observation submitted a transaction")
			}
		})
	}
}
