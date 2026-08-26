package pricesource

import (
	"context"

	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

// liveAccountReader adapts the production RPC client for the env-gated smoke
// test and records which migration accounts were actually usable.
type liveAccountReader struct {
	client *solanarpc.Client
	seen   map[string]bool
}

func (r liveAccountReader) AccountSlice(
	ctx context.Context, address string, minContextSlot, offset, length uint64,
) (AccountData, error) {
	slice, err := r.client.AccountSlice(ctx, address, minContextSlot, offset, length)
	if err != nil {
		return AccountData{}, err
	}
	r.seen[address] = true
	return AccountData{
		ContextSlot: slice.ContextSlot,
		Owner:       slice.Owner,
		DataLength:  slice.DataLength,
		Data:        slice.Data,
	}, nil
}
