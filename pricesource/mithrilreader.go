package pricesource

import "context"

// mithrilAccountReader adapts a Mithril RPC client to the narrow AccountReader
// this package needs. It is a shim rather than a direct dependency so
// pricesource never imports a package capable of submitting a transaction.
type mithrilAccountReader struct {
	slice func(ctx context.Context, address string, minContextSlot, offset, length uint64) (AccountData, error)
}

func (r mithrilAccountReader) AccountSlice(
	ctx context.Context,
	address string,
	minContextSlot, offset, length uint64,
) (AccountData, error) {
	return r.slice(ctx, address, minContextSlot, offset, length)
}

// NewMithrilAccountReader builds an AccountReader from a caller-supplied read
// function. The caller owns the RPC client, so the origin stays pinned to the
// operator's own node and never reaches this package as a string.
func NewMithrilAccountReader(
	slice func(ctx context.Context, address string, minContextSlot, offset, length uint64) (AccountData, error),
) AccountReader {
	if slice == nil {
		return nil
	}
	return mithrilAccountReader{slice: slice}
}
