package solana

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestDecodeV0MessageMatchesExternalVector(t *testing.T) {
	const transactionBase64 = "Alkhq/BfGdBeok4oBP21xAwT4oO/R5PvkKqbCTq4sHHRsto+uDQCFcdp8hXh1g5D3mTh8GAJW8xE+EDD27f9IweTkH2Afiu4h5aM+Xbo0mklc0/Vi1xawd7SZVbstXDLtWdoJaf4Zt+20F/SasURzw/P4dkD+Q6BjgUNHT+vg5gOgAIBAQgaJV0Ch/DG6XwNcizWbI7STLgSbIOrg0Dl67Oo30WU1uA/NIbYLPRmuLarIJ4J0CcN3IWEm4Gf8675KhnXef2LaDXzjFgWVSbAO2yyTF6dK1oO3gTExie957LXDwu6oJMAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVKU1qZKSEGTSTocWDaOHx8NbXdvJK7geQfqEBBBUSN1LfoiB9oYLDSHJL9rjAlchZhn+fd/23ACfq0oIGla54pt5JT0MdBTJhQI+z7dnVsisw2xWwW+vFSTs97l0tJPxmv9kxpXbHYZFenDpT2s6CT75/9QNFVTkHFLMK+UG6VlyFnQmYh1aMkGtq3c6TIOsk32S6XMUnN9DQgFGQq4lwEAwIAAgwCAAAAgJaYAAAAAAADAgAFDAIAAACAlpgAAAAAAAMCAAYMAgAAAICWmAAAAAAABAAMSGVsbG8gRmFiaW8hAX5s37FH6IeB4QeMYxD4LtpXf1DaupH/ro7W+kEQnofaAgECAQA="
	raw, err := base64.StdEncoding.DecodeString(transactionBase64)
	if err != nil {
		t.Fatal(err)
	}
	// This public fixture has two signatures, encoded before the v0 message.
	message := raw[1+2*64:]
	tableID := testV0Key(t, "9WWfC3y4uCNofr2qEFHSVUXkCxW99JiYkMWmSZvVt8j3")
	tables := map[[32]byte][][32]byte{tableID: {
		testV0Key(t, "2jGpE3ADYRoJPMjyGC4tvqqDfobvdvwGr3vhd66zA1rc"),
		testV0Key(t, "FKN5imdi7yadX4axe4hxaqBET4n6DBDRF5LKo5aBF53j"),
		testV0Key(t, "3or4uF7ZyuQW5GGmcmdXDJasNiSZUURF2az1UrRPYQTg"),
		testV0Key(t, "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"),
	}}

	decoded, err := DecodeV0Message(message, tables)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequiredSignatures != 2 || decoded.ReadonlySigned != 1 ||
		decoded.ReadonlyUnsigned != 1 || len(decoded.StaticAccountKeys) != 8 ||
		len(decoded.AccountKeys) != 11 || decoded.WritableLookupKeys != 2 {
		t.Fatalf("unexpected decoded header: %+v", decoded)
	}
	wantLoaded := [][32]byte{tables[tableID][1], tables[tableID][2], tables[tableID][0]}
	if !equalV0Keys(decoded.AccountKeys[8:], wantLoaded) {
		t.Fatalf("loaded account order does not match Solana v0 semantics")
	}
	if !decoded.IsSigner(0) || !decoded.IsSigner(1) || decoded.IsSigner(8) ||
		!decoded.IsWritable(8) || !decoded.IsWritable(9) || decoded.IsWritable(10) {
		t.Fatal("resolved signer or writable privileges are wrong")
	}
}

func TestDecodeV0MessageGroupsWritableLookupsBeforeReadonlyLookups(t *testing.T) {
	firstID := v0FilledKey(10)
	secondID := v0FilledKey(20)
	first := [][32]byte{v0FilledKey(11), v0FilledKey(12)}
	second := [][32]byte{v0FilledKey(21), v0FilledKey(22)}
	message := testV0Message([]testV0Lookup{
		{firstID, []byte{1}, []byte{0}},
		{secondID, []byte{0}, []byte{1}},
	}, 2, []byte{0, 3, 4, 5, 6})

	decoded, err := DecodeV0Message(message, map[[32]byte][][32]byte{
		firstID: first, secondID: second,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][32]byte{first[1], second[0], first[0], second[1]}
	if !equalV0Keys(decoded.AccountKeys[3:], want) {
		t.Fatal("multiple lookup tables were resolved in per-table rather than global privilege order")
	}
}

func TestValidateV0MessageForSignerRejectsPrivilegeAmbiguity(t *testing.T) {
	tableID := v0FilledKey(10)
	table := [][32]byte{v0FilledKey(11), v0FilledKey(12)}
	message := testV0Message([]testV0Lookup{{tableID, []byte{1}, []byte{0}}}, 2, []byte{0, 3, 4})
	decoded, err := DecodeV0Message(message, map[[32]byte][][32]byte{tableID: table})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV0MessageForSigner(decoded, Encode(decoded.StaticAccountKeys[0][:])); err != nil {
		t.Fatalf("safe one-signer shape rejected: %v", err)
	}

	wrongSigner := decoded
	wrongSigner.StaticAccountKeys = append([][32]byte(nil), decoded.StaticAccountKeys...)
	wrongSigner.StaticAccountKeys[0] = v0FilledKey(99)
	if err := ValidateV0MessageForSigner(wrongSigner, Encode(decoded.AccountKeys[0][:])); err == nil {
		t.Fatal("wrong signer was accepted")
	}

	duplicate := decoded
	duplicate.AccountKeys = append([][32]byte(nil), decoded.AccountKeys...)
	duplicate.AccountKeys[len(duplicate.AccountKeys)-1] = duplicate.AccountKeys[0]
	if err := ValidateV0MessageForSigner(duplicate, Encode(decoded.AccountKeys[0][:])); err == nil {
		t.Fatal("duplicate resolved account was accepted")
	}

	writableProgramMessage := testV0Message(
		[]testV0Lookup{{tableID, []byte{1}, []byte{0}}}, 1, []byte{0},
	)
	writableProgram, err := DecodeV0Message(
		writableProgramMessage, map[[32]byte][][32]byte{tableID: table},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV0MessageForSigner(
		writableProgram, Encode(writableProgram.AccountKeys[0][:]),
	); err == nil {
		t.Fatal("writable program was accepted")
	}
}

func TestDecodeV0MessageRejectsUnsafeOrMalformedMessages(t *testing.T) {
	tableID := v0FilledKey(10)
	table := [][32]byte{v0FilledKey(11), v0FilledKey(12)}
	valid := testV0Message([]testV0Lookup{{tableID, []byte{1}, []byte{0}}}, 2, []byte{0, 3, 4})

	tests := map[string]struct {
		message []byte
		tables  map[[32]byte][][32]byte
	}{
		"legacy version":    {append([]byte{0}, valid[1:]...), map[[32]byte][][32]byte{tableID: table}},
		"version one":       {append([]byte{0x81}, valid[1:]...), map[[32]byte][][32]byte{tableID: table}},
		"missing table":     {valid, nil},
		"short table":       {valid, map[[32]byte][][32]byte{tableID: table[:1]}},
		"bad account index": {testV0Message([]testV0Lookup{{tableID, []byte{1}, []byte{0}}}, 2, []byte{5}), map[[32]byte][][32]byte{tableID: table}},
		"empty lookup":      {testV0Message([]testV0Lookup{{tableID, nil, nil}}, 2, []byte{0}), map[[32]byte][][32]byte{tableID: table}},
		"trailing bytes":    {append(bytes.Clone(valid), 1), map[[32]byte][][32]byte{tableID: table}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeV0Message(test.message, test.tables); err == nil {
				t.Fatal("unsafe or malformed v0 message was accepted")
			}
		})
	}
}

type testV0Lookup struct {
	table    [32]byte
	writable []byte
	readonly []byte
}

func testV0Message(lookups []testV0Lookup, program byte, accounts []byte) []byte {
	message := []byte{0x80, 1, 0, 1, 3}
	for _, key := range [][32]byte{v0FilledKey(1), v0FilledKey(2), v0FilledKey(3)} {
		message = append(message, key[:]...)
	}
	blockhash := v0FilledKey(4)
	message = append(message, blockhash[:]...)
	message = append(message, 1, program)
	message = appendShortVec(message, len(accounts))
	message = append(message, accounts...)
	message = append(message, 1, 7)
	message = appendShortVec(message, len(lookups))
	for _, lookup := range lookups {
		message = append(message, lookup.table[:]...)
		message = appendShortVec(message, len(lookup.writable))
		message = append(message, lookup.writable...)
		message = appendShortVec(message, len(lookup.readonly))
		message = append(message, lookup.readonly...)
	}
	return message
}

func testV0Key(t *testing.T, address string) [32]byte {
	t.Helper()
	key, err := Decode32(address)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func v0FilledKey(value byte) [32]byte {
	var key [32]byte
	for index := range key {
		key[index] = value
	}
	return key
}

func equalV0Keys(left, right [][32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
