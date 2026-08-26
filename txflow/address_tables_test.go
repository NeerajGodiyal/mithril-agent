package txflow

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestVerifyAddressLookupTablesRequiresThreeMatchingViews(t *testing.T) {
	table := [32]byte{1}
	addresses := [][32]byte{{2}, {3}}
	tableAddress := solana.Encode(table[:])
	provider := func(identity string) *fakeProvider {
		return &fakeProvider{
			identity: identity,
			addressTables: map[string]solanarpc.AddressLookupTable{
				tableAddress: {ContextSlot: 120, Addresses: addresses},
			},
		}
	}
	lifecycle, err := New(provider("node"), provider("primary"), provider("secondary"))
	if err != nil {
		t.Fatal(err)
	}

	verified, err := lifecycle.VerifyAddressLookupTables(
		t.Context(), map[[32]byte][][32]byte{table: addresses}, 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || !bytes.Equal(verified[table][1][:], addresses[1][:]) {
		t.Fatalf("unexpected verified tables: %#v", verified)
	}
	addresses[0][0] = 9
	if verified[table][0][0] != 2 {
		t.Fatal("verified table aliases the untrusted proposal")
	}
}

func TestVerifyAddressLookupTablesFailsClosed(t *testing.T) {
	table := [32]byte{1}
	addresses := [][32]byte{{2}, {3}}
	tableAddress := solana.Encode(table[:])
	provider := func(identity string, slot uint64, values [][32]byte) *fakeProvider {
		return &fakeProvider{
			identity: identity,
			addressTables: map[string]solanarpc.AddressLookupTable{
				tableAddress: {ContextSlot: slot, Addresses: values},
			},
		}
	}
	tests := map[string]struct {
		node, primary, secondary *fakeProvider
		claimed                  [][32]byte
	}{
		"builder claim": {
			provider("node", 120, addresses), provider("primary", 120, addresses),
			provider("secondary", 120, addresses), [][32]byte{{8}, {3}},
		},
		"provider disagreement": {
			provider("node", 120, addresses), provider("primary", 120, addresses),
			provider("secondary", 120, [][32]byte{{2}, {8}}), addresses,
		},
		"stale provider": {
			provider("node", 120, addresses), provider("primary", 99, addresses),
			provider("secondary", 120, addresses), addresses,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			lifecycle, err := New(test.node, test.primary, test.secondary)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lifecycle.VerifyAddressLookupTables(
				t.Context(), map[[32]byte][][32]byte{table: test.claimed}, 100,
			); err == nil {
				t.Fatal("expected verification failure")
			}
		})
	}
}

func TestVerifyAddressLookupTablesRejectsProviderFailure(t *testing.T) {
	table := [32]byte{1}
	addresses := [][32]byte{{2}}
	tableAddress := solana.Encode(table[:])
	provider := func(identity string) *fakeProvider {
		return &fakeProvider{
			identity: identity,
			addressTables: map[string]solanarpc.AddressLookupTable{
				tableAddress: {ContextSlot: 120, Addresses: addresses},
			},
		}
	}
	node, primary, secondary := provider("node"), provider("primary"), provider("secondary")
	secondary.addressTableErr = errors.New("unavailable")
	lifecycle, err := New(node, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.VerifyAddressLookupTables(
		t.Context(), map[[32]byte][][32]byte{table: addresses}, 100,
	); err == nil {
		t.Fatal("expected provider failure")
	}
}

func TestVerifyAddressLookupTablesUsesTheEvidenceLimit(t *testing.T) {
	lifecycle, err := New(
		&fakeProvider{identity: "node"},
		&fakeProvider{identity: "primary"},
		&fakeProvider{identity: "secondary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[[32]byte][][32]byte, maxAddressLookupTables+1)
	for index := range maxAddressLookupTables + 1 {
		claimed[[32]byte{byte(index + 1)}] = [][32]byte{{1}}
	}
	if _, err := lifecycle.VerifyAddressLookupTables(t.Context(), claimed, 100); err == nil {
		t.Fatal("more address tables than the evidence format supports were accepted")
	}
}
