package programinterface

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"os"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type pmpReader struct {
	account      solanarpc.AccountDataSlice
	rangeAccount solanarpc.AccountDataSlice
	address      string
	rangeAddress string
	rangeMinSlot uint64
	rangeOffset  uint64
	rangeLength  uint64
}

var pmpTestBankhash = solana.Encode(bytes.Repeat([]byte{9}, 32))

func (r *pmpReader) AccountData(
	_ context.Context, address string, _, _ uint64,
) (solanarpc.AccountDataSlice, error) {
	r.address = address
	return r.account, nil
}

func (r *pmpReader) AccountDataRange(
	_ context.Context, address string, minSlot, offset, length uint64,
) (solanarpc.AccountDataSlice, error) {
	r.rangeAddress = address
	r.rangeMinSlot = minSlot
	r.rangeOffset = offset
	r.rangeLength = length
	return r.rangeAccount, nil
}

func TestFetchAndPinCanonicalPMP(t *testing.T) {
	program := "11111111111111111111111111111111"
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	account := pmpAccount(t, program, idl)
	reader := &pmpReader{account: solanarpc.AccountDataSlice{
		ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: ProgramMetadataProgram, DataLength: uint64(len(account)), Data: account,
	}}
	result, err := FetchAndPinPMP(t.Context(), reader, t.TempDir(), program, 80)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "canonical_pmp_direct" || result.ContextSlot != 90 ||
		result.Bankhash != pmpTestBankhash ||
		result.Program != program || result.MetadataAccount != reader.address || !result.Created {
		t.Fatalf("result = %+v", result)
	}
	programKey, _ := solana.Decode32(program)
	seed := make([]byte, 16)
	copy(seed, "idl")
	want, _, err := solana.FindProgramAddress([][]byte{programKey[:], seed}, ProgramMetadataProgram)
	if err != nil {
		t.Fatal(err)
	}
	if reader.address != want {
		t.Fatalf("metadata address = %s, want %s", reader.address, want)
	}
	registry := t.TempDir()
	reader.account.Bankhash = ""
	if _, err := FetchAndPinPMP(t.Context(), reader, registry, program, 80); err == nil {
		t.Fatal("PMP metadata without a processed bank identity was accepted")
	}
	entries, err := os.ReadDir(registry)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing-bank PMP registry = %d entries, %v", len(entries), err)
	}
}

func TestFetchPMPRejectsIndirectionAndWrongOwner(t *testing.T) {
	program := "11111111111111111111111111111111"
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	account := pmpAccount(t, program, idl)
	account[86] = 1
	reader := &pmpReader{account: solanarpc.AccountDataSlice{
		ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: ProgramMetadataProgram, DataLength: uint64(len(account)), Data: account,
	}}
	if _, err := FetchAndPinPMP(t.Context(), reader, t.TempDir(), program, 80); err == nil {
		t.Fatal("PMP URL indirection was accepted")
	}
	reader.account.Owner = program
	if _, err := FetchAndPinPMP(t.Context(), reader, t.TempDir(), program, 80); err == nil {
		t.Fatal("wrong PMP owner was accepted")
	}
}

func TestFetchAndPinCanonicalExternalPMP(t *testing.T) {
	program := "11111111111111111111111111111111"
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(idl); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	externalKey := bytes.Repeat([]byte{7}, 32)
	externalAddress := solana.Encode(externalKey)
	const offset = 12
	metadata := pmpExternalAccount(
		t, program, externalKey, offset, uint32(compressed.Len()), 2,
	)
	reader := &pmpReader{
		account: solanarpc.AccountDataSlice{
			ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: ProgramMetadataProgram,
			DataLength: uint64(len(metadata)), Data: metadata,
		},
		rangeAccount: solanarpc.AccountDataSlice{
			ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: program,
			DataLength: offset + uint64(compressed.Len()) + 5, Data: compressed.Bytes(),
		},
	}
	result, err := FetchAndPinPMP(t.Context(), reader, t.TempDir(), program, 80)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "canonical_pmp_external" || result.ContextSlot != 90 ||
		result.Bankhash != pmpTestBankhash ||
		result.ContentAccount != externalAddress || result.ContentContextSlot != 90 ||
		reader.rangeAddress != externalAddress || reader.rangeMinSlot != 90 ||
		reader.rangeOffset != offset || reader.rangeLength != uint64(compressed.Len()) {
		t.Fatalf("result = %+v, reader = %+v", result, reader)
	}

	registry := t.TempDir()
	reader.rangeAccount.ContextSlot = 91
	if _, err := FetchAndPinPMP(t.Context(), reader, registry, program, 80); err == nil {
		t.Fatal("external PMP assembled from two processed banks")
	}
	entries, err := os.ReadDir(registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("mismatched-bank PMP wrote %d registry entries", len(entries))
	}
	reader.rangeAccount.ContextSlot = 90
	reader.rangeAccount.Bankhash = solana.Encode(bytes.Repeat([]byte{8}, 32))
	if _, err := FetchAndPinPMP(t.Context(), reader, registry, program, 80); err == nil {
		t.Fatal("external PMP assembled from sibling banks at one slot")
	}
}

func TestFetchExternalPMPUsesRemainingAccountWhenLengthIsUnset(t *testing.T) {
	program := "11111111111111111111111111111111"
	idl := []byte(`{"address":"11111111111111111111111111111111","metadata":{"name":"system","version":"1","spec":"0.1.0"},"instructions":[]}`)
	externalKey := bytes.Repeat([]byte{6}, 32)
	const offset = 7
	metadata := pmpExternalAccount(t, program, externalKey, offset, 0, 0)
	reader := &pmpReader{
		account: solanarpc.AccountDataSlice{
			ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: ProgramMetadataProgram,
			DataLength: uint64(len(metadata)), Data: metadata,
		},
		rangeAccount: solanarpc.AccountDataSlice{
			ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: program,
			DataLength: offset + uint64(len(idl)), Data: idl,
		},
	}
	result, err := FetchAndPinPMP(t.Context(), reader, t.TempDir(), program, 80)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "canonical_pmp_external" || reader.rangeOffset != offset ||
		reader.rangeLength != MaxIDLBytes {
		t.Fatalf("result = %+v, reader = %+v", result, reader)
	}
}

func TestFetchExternalPMPRejectsUnprovableRange(t *testing.T) {
	program := "11111111111111111111111111111111"
	externalKey := bytes.Repeat([]byte{7}, 32)
	metadata := pmpExternalAccount(t, program, externalKey, 12, 4, 0)
	reader := &pmpReader{
		account: solanarpc.AccountDataSlice{
			ContextSlot: 90, Bankhash: pmpTestBankhash, Owner: ProgramMetadataProgram,
			DataLength: uint64(len(metadata)), Data: metadata,
		},
		rangeAccount: solanarpc.AccountDataSlice{
			ContextSlot: 91, Bankhash: pmpTestBankhash, Owner: program, DataLength: 15, Data: []byte("test"),
		},
	}
	if _, err := FetchAndPinPMP(t.Context(), reader, t.TempDir(), program, 80); err == nil {
		t.Fatal("external PMP range beyond the account was accepted")
	}
}

func pmpAccount(t *testing.T, program string, idl []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(idl); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, pmpHeaderBytes+compressed.Len())
	data[0] = 2
	programKey, err := solana.Decode32(program)
	if err != nil {
		t.Fatal(err)
	}
	copy(data[1:33], programKey[:])
	data[65] = 1
	data[66] = 1
	copy(data[67:83], []byte("idl"))
	data[83] = 1
	data[84] = 2
	data[85] = 1
	data[86] = 0
	binary.LittleEndian.PutUint32(data[87:91], uint32(compressed.Len()))
	copy(data[pmpHeaderBytes:], compressed.Bytes())
	return data
}

func pmpExternalAccount(
	t *testing.T,
	program string,
	externalKey []byte,
	offset,
	length uint32,
	compression byte,
) []byte {
	t.Helper()
	if len(externalKey) != 32 {
		t.Fatal("external key must be 32 bytes")
	}
	data := make([]byte, pmpHeaderBytes+pmpExternalDataBytes)
	data[0] = 2
	programKey, err := solana.Decode32(program)
	if err != nil {
		t.Fatal(err)
	}
	copy(data[1:33], programKey[:])
	data[65], data[66] = 1, 1
	copy(data[67:83], []byte("idl"))
	data[83], data[84], data[85], data[86] = 1, compression, 1, 2
	binary.LittleEndian.PutUint32(data[87:91], pmpExternalDataBytes)
	copy(data[pmpHeaderBytes:pmpHeaderBytes+32], externalKey)
	binary.LittleEndian.PutUint32(data[pmpHeaderBytes+32:pmpHeaderBytes+36], offset)
	binary.LittleEndian.PutUint32(data[pmpHeaderBytes+36:pmpHeaderBytes+40], length)
	return data
}
