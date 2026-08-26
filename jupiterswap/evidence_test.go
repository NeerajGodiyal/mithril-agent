package jupiterswap

import (
	"bytes"
	"testing"
)

func TestAddressTableEvidenceRoundTripsCanonically(t *testing.T) {
	var first, second [32]byte
	copy(first[:], bytes.Repeat([]byte{1}, 32))
	copy(second[:], bytes.Repeat([]byte{2}, 32))
	tables := map[[32]byte][][32]byte{second: {first, second}, first: {second}}
	evidence, err := EncodeAddressTables(tables)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 || evidence[0].Address >= evidence[1].Address {
		t.Fatalf("evidence order = %+v", evidence)
	}
	decoded, err := DecodeAddressTables(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded[first]) != 1 || decoded[first][0] != second || len(decoded[second]) != 2 {
		t.Fatalf("decoded = %+v", decoded)
	}

	reversed := []AddressTableEvidence{evidence[1], evidence[0]}
	if _, err := DecodeAddressTables(reversed); err == nil {
		t.Fatal("accepted non-canonical address-table evidence")
	}
}
