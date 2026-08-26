package solana

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	AddressLookupTableProgram  = "AddressLookupTab1e1111111111111111111111111"
	addressLookupTableMetaSize = 56
	maxLookupTableAddresses    = 256
)

// DecodeAddressLookupTable returns the addresses usable at contextSlot from a
// live Address Lookup Table program account. Addresses appended in the same
// slot are intentionally withheld because Solana does not make them active
// until a later slot.
func DecodeAddressLookupTable(data []byte, contextSlot uint64) ([][32]byte, error) {
	if contextSlot == 0 || len(data) < addressLookupTableMetaSize ||
		(len(data)-addressLookupTableMetaSize)%32 != 0 {
		return nil, errors.New("address lookup table data is invalid")
	}
	count := (len(data) - addressLookupTableMetaSize) / 32
	if count == 0 || count > maxLookupTableAddresses || binary.LittleEndian.Uint32(data[:4]) != 1 {
		return nil, errors.New("address lookup table data is invalid")
	}
	if binary.LittleEndian.Uint64(data[4:12]) != math.MaxUint64 {
		return nil, errors.New("address lookup table is deactivated")
	}
	lastExtendedSlot := binary.LittleEndian.Uint64(data[12:20])
	startIndex := int(data[20])
	if lastExtendedSlot > contextSlot || startIndex > count {
		return nil, errors.New("address lookup table metadata is inconsistent")
	}
	activeCount := count
	if lastExtendedSlot == contextSlot {
		activeCount = startIndex
	}
	if activeCount == 0 {
		return nil, errors.New("address lookup table has no active addresses")
	}
	addresses := make([][32]byte, activeCount)
	for index := range addresses {
		copy(addresses[index][:], data[addressLookupTableMetaSize+index*32:])
	}
	return addresses, nil
}
