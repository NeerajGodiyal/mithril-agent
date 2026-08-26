package solana

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestDecodeAddressLookupTableHonorsWarmup(t *testing.T) {
	data := testLookupTableData(100, 1, v0FilledKey(1), v0FilledKey(2))
	current, err := DecodeAddressLookupTable(data, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0] != v0FilledKey(1) {
		t.Fatal("same-slot extension was treated as active")
	}
	next, err := DecodeAddressLookupTable(data, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || next[1] != v0FilledKey(2) {
		t.Fatal("warmed-up lookup table address is missing")
	}
}

func TestDecodeAddressLookupTableRejectsInvalidState(t *testing.T) {
	valid := testLookupTableData(100, 1, v0FilledKey(1), v0FilledKey(2))
	deactivated := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint64(deactivated[4:12], 200)
	future := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint64(future[12:20], 102)
	badType := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(badType[:4], 0)
	badStart := append([]byte(nil), valid...)
	badStart[20] = 3
	for name, data := range map[string][]byte{
		"short":       valid[:55],
		"unaligned":   append(valid, 1),
		"deactivated": deactivated,
		"future":      future,
		"wrong state": badType,
		"start index": badStart,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAddressLookupTable(data, 101); err == nil {
				t.Fatal("invalid lookup table was accepted")
			}
		})
	}
}

func testLookupTableData(lastExtendedSlot uint64, startIndex byte, addresses ...[32]byte) []byte {
	data := make([]byte, addressLookupTableMetaSize, addressLookupTableMetaSize+len(addresses)*32)
	binary.LittleEndian.PutUint32(data[:4], 1)
	binary.LittleEndian.PutUint64(data[4:12], math.MaxUint64)
	binary.LittleEndian.PutUint64(data[12:20], lastExtendedSlot)
	data[20] = startIndex
	for _, address := range addresses {
		data = append(data, address[:]...)
	}
	return data
}
