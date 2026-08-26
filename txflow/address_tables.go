package txflow

import (
	"context"
	"errors"
	"slices"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const maxAddressLookupTables = 32

type addressLookupTableProvider interface {
	AddressLookupTable(context.Context, string, uint64) (solanarpc.AddressLookupTable, error)
}

// VerifyAddressLookupTables compares an untrusted transaction builder's ALT
// contents with Mithril and both independent evidence providers. Only the
// mutually identical on-chain view is returned for v0 message compilation.
func (l *Lifecycle) VerifyAddressLookupTables(
	ctx context.Context,
	claimed map[[32]byte][][32]byte,
	minContextSlot uint64,
) (map[[32]byte][][32]byte, error) {
	if minContextSlot == 0 {
		return nil, errors.New("minimum address-table context slot is required")
	}
	if len(claimed) > maxAddressLookupTables {
		return nil, errors.New("too many address lookup tables")
	}
	node, nodeOK := l.node.(addressLookupTableProvider)
	primary, primaryOK := l.primary.(addressLookupTableProvider)
	secondary, secondaryOK := l.secondary.(addressLookupTableProvider)
	if !nodeOK || !primaryOK || !secondaryOK {
		return nil, errors.New("Mithril RPC and evidence providers do not support address lookup tables")
	}

	verified := make(map[[32]byte][][32]byte, len(claimed))
	for table, proposed := range claimed {
		if len(proposed) == 0 || len(proposed) > 256 {
			return nil, errors.New("claimed address lookup table is invalid")
		}
		address := solana.Encode(table[:])
		observed, err := queryAddressLookupTables(ctx, node, primary, secondary, address, minContextSlot)
		if err != nil {
			return nil, errors.New("query address lookup table evidence")
		}
		if observed[0].ContextSlot < minContextSlot ||
			observed[1].ContextSlot < minContextSlot ||
			observed[2].ContextSlot < minContextSlot ||
			!slices.Equal(observed[0].Addresses, proposed) ||
			!slices.Equal(observed[1].Addresses, proposed) ||
			!slices.Equal(observed[2].Addresses, proposed) {
			return nil, errors.New("address lookup table evidence disagrees with the transaction proposal")
		}
		verified[table] = slices.Clone(observed[0].Addresses)
	}
	return verified, nil
}

func queryAddressLookupTables(
	ctx context.Context,
	node, primary, secondary addressLookupTableProvider,
	address string,
	minContextSlot uint64,
) ([3]solanarpc.AddressLookupTable, error) {
	type result struct {
		index int
		table solanarpc.AddressLookupTable
		err   error
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, 3)
	providers := []addressLookupTableProvider{node, primary, secondary}
	for index, provider := range providers {
		go func() {
			table, err := provider.AddressLookupTable(child, address, minContextSlot)
			results <- result{index: index, table: table, err: err}
		}()
	}
	var tables [3]solanarpc.AddressLookupTable
	var firstErr error
	for range providers {
		select {
		case <-ctx.Done():
			return tables, ctx.Err()
		case result := <-results:
			if result.err != nil {
				if firstErr == nil {
					firstErr = result.err
					cancel()
				}
				continue
			}
			tables[result.index] = result.table
		}
	}
	return tables, firstErr
}
