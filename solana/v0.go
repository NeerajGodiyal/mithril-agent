package solana

import (
	"bytes"
	"errors"
	"fmt"
)

// MessageAddressTableLookup identifies the writable and read-only addresses a
// v0 message loads from one address lookup table.
type MessageAddressTableLookup struct {
	AccountKey      [32]byte
	WritableIndexes []uint8
	ReadonlyIndexes []uint8
}

// V0Message is a decoded Solana version-0 message with all lookup-table
// addresses resolved. AccountKeys uses Solana's runtime order: static keys,
// every writable lookup key, then every read-only lookup key.
type V0Message struct {
	RequiredSignatures  uint8
	ReadonlySigned      uint8
	ReadonlyUnsigned    uint8
	StaticAccountKeys   [][32]byte
	AccountKeys         [][32]byte
	RecentBlockhash     [32]byte
	Instructions        []CompiledInstruction
	AddressTableLookups []MessageAddressTableLookup
	WritableLookupKeys  int
	Raw                 []byte
}

func (m V0Message) IsSigner(index int) bool {
	return index >= 0 && index < int(m.RequiredSignatures)
}

func (m V0Message) IsWritable(index int) bool {
	if index < 0 || index >= len(m.AccountKeys) {
		return false
	}
	staticCount := len(m.StaticAccountKeys)
	if index >= staticCount {
		return index-staticCount < m.WritableLookupKeys
	}
	if index < int(m.RequiredSignatures) {
		return index < int(m.RequiredSignatures-m.ReadonlySigned)
	}
	return index < staticCount-int(m.ReadonlyUnsigned)
}

// DecodeV0Message decodes a bounded Solana v0 message using address mappings
// supplied by the caller. A signing path must obtain or verify those mappings
// independently; a transaction builder's claimed table contents are not
// evidence of what the runtime will load.
func DecodeV0Message(message []byte, tables map[[32]byte][][32]byte) (V0Message, error) {
	var decoded V0Message
	if len(message) < 2 || message[0] != 0x80 {
		return decoded, errors.New("message is not Solana version 0")
	}
	reader := byteReader{data: message, offset: 1}
	required, err := reader.byte()
	if err != nil {
		return decoded, err
	}
	readonlySigned, err := reader.byte()
	if err != nil {
		return decoded, err
	}
	readonlyUnsigned, err := reader.byte()
	if err != nil {
		return decoded, err
	}
	if required == 0 || readonlySigned >= required {
		return decoded, errors.New("v0 message must have a writable fee payer")
	}
	if len(message)+len(appendShortVec(nil, int(required)))+int(required)*64 > maxTransactionBytes {
		return decoded, errors.New("v0 transaction exceeds Solana packet limit")
	}

	staticCount, err := reader.shortVec()
	if err != nil {
		return decoded, fmt.Errorf("static account count: %w", err)
	}
	if staticCount == 0 || staticCount > 256 || int(required) > staticCount ||
		int(readonlyUnsigned) > staticCount-int(required) {
		return decoded, errors.New("v0 message account header is invalid")
	}
	decoded.StaticAccountKeys = make([][32]byte, staticCount)
	for index := range decoded.StaticAccountKeys {
		if err := reader.fixed(decoded.StaticAccountKeys[index][:]); err != nil {
			return V0Message{}, err
		}
	}
	if err := reader.fixed(decoded.RecentBlockhash[:]); err != nil {
		return V0Message{}, err
	}

	instructionCount, err := reader.shortVec()
	if err != nil {
		return V0Message{}, fmt.Errorf("instruction count: %w", err)
	}
	if instructionCount == 0 || instructionCount > 64 {
		return V0Message{}, errors.New("v0 message instruction count is invalid")
	}
	decoded.Instructions = make([]CompiledInstruction, instructionCount)
	for index := range decoded.Instructions {
		instruction := &decoded.Instructions[index]
		instruction.ProgramIndex, err = reader.byte()
		if err != nil {
			return V0Message{}, err
		}
		accountCount, err := reader.shortVec()
		if err != nil {
			return V0Message{}, fmt.Errorf("instruction account count: %w", err)
		}
		if accountCount > 255 {
			return V0Message{}, errors.New("v0 instruction has too many accounts")
		}
		instruction.Accounts = make([]uint8, accountCount)
		if err := reader.fixed(instruction.Accounts); err != nil {
			return V0Message{}, err
		}
		dataLength, err := reader.shortVec()
		if err != nil {
			return V0Message{}, fmt.Errorf("instruction data length: %w", err)
		}
		instruction.Data = make([]byte, dataLength)
		if err := reader.fixed(instruction.Data); err != nil {
			return V0Message{}, err
		}
	}

	lookupCount, err := reader.shortVec()
	if err != nil {
		return V0Message{}, fmt.Errorf("address lookup count: %w", err)
	}
	if lookupCount > 64 {
		return V0Message{}, errors.New("v0 message has too many address lookups")
	}
	decoded.AddressTableLookups = make([]MessageAddressTableLookup, lookupCount)
	writableKeys := make([][32]byte, 0, 32)
	readonlyKeys := make([][32]byte, 0, 32)
	for index := range decoded.AddressTableLookups {
		lookup := &decoded.AddressTableLookups[index]
		if err := reader.fixed(lookup.AccountKey[:]); err != nil {
			return V0Message{}, err
		}
		table, ok := tables[lookup.AccountKey]
		if !ok {
			return V0Message{}, errors.New("v0 address lookup table is missing")
		}
		lookup.WritableIndexes, writableKeys, err = decodeLookupIndexes(&reader, table, writableKeys)
		if err != nil {
			return V0Message{}, fmt.Errorf("writable address lookup: %w", err)
		}
		lookup.ReadonlyIndexes, readonlyKeys, err = decodeLookupIndexes(&reader, table, readonlyKeys)
		if err != nil {
			return V0Message{}, fmt.Errorf("readonly address lookup: %w", err)
		}
		if len(lookup.WritableIndexes)+len(lookup.ReadonlyIndexes) == 0 {
			return V0Message{}, errors.New("v0 address lookup is empty")
		}
	}
	if reader.remaining() != 0 {
		return V0Message{}, errors.New("v0 message contains trailing bytes")
	}

	totalKeys := staticCount + len(writableKeys) + len(readonlyKeys)
	if totalKeys > 256 {
		return V0Message{}, errors.New("v0 message has too many resolved accounts")
	}
	decoded.AccountKeys = make([][32]byte, 0, totalKeys)
	decoded.AccountKeys = append(decoded.AccountKeys, decoded.StaticAccountKeys...)
	decoded.AccountKeys = append(decoded.AccountKeys, writableKeys...)
	decoded.AccountKeys = append(decoded.AccountKeys, readonlyKeys...)
	decoded.WritableLookupKeys = len(writableKeys)
	decoded.RequiredSignatures = required
	decoded.ReadonlySigned = readonlySigned
	decoded.ReadonlyUnsigned = readonlyUnsigned
	for _, instruction := range decoded.Instructions {
		program := int(instruction.ProgramIndex)
		if program >= totalKeys {
			return V0Message{}, errors.New("v0 instruction program index is invalid")
		}
		for _, account := range instruction.Accounts {
			if int(account) >= totalKeys {
				return V0Message{}, errors.New("v0 instruction account index is invalid")
			}
		}
	}
	decoded.Raw = bytes.Clone(message)
	return decoded, nil
}

// ValidateV0MessageForSigner applies the signer-independent structural rules
// required before semantic instruction validation. It deliberately does not
// decide which programs or accounts an application should permit.
func ValidateV0MessageForSigner(message V0Message, expectedSigner string) error {
	expected, err := Decode32(expectedSigner)
	if err != nil {
		return errors.New("expected v0 signer is invalid")
	}
	if message.RequiredSignatures != 1 || message.ReadonlySigned != 0 ||
		len(message.StaticAccountKeys) == 0 || message.StaticAccountKeys[0] != expected ||
		len(message.AccountKeys) < len(message.StaticAccountKeys) {
		return errors.New("v0 message does not have the expected sole writable signer")
	}
	for index := range message.StaticAccountKeys {
		if message.AccountKeys[index] != message.StaticAccountKeys[index] {
			return errors.New("v0 message resolved accounts do not match its static prefix")
		}
	}
	seen := make(map[[32]byte]struct{}, len(message.AccountKeys))
	for _, key := range message.AccountKeys {
		if _, exists := seen[key]; exists {
			return errors.New("v0 message contains a duplicate resolved account")
		}
		seen[key] = struct{}{}
	}
	for _, instruction := range message.Instructions {
		program := int(instruction.ProgramIndex)
		if program >= len(message.AccountKeys) || message.IsSigner(program) || message.IsWritable(program) {
			return errors.New("v0 instruction program must be a readonly non-signer")
		}
		for _, account := range instruction.Accounts {
			if int(account) >= len(message.AccountKeys) {
				return errors.New("v0 instruction account index is invalid")
			}
		}
	}
	return nil
}

func decodeLookupIndexes(reader *byteReader, table [][32]byte, keys [][32]byte) ([]uint8, [][32]byte, error) {
	count, err := reader.shortVec()
	if err != nil {
		return nil, keys, err
	}
	if count > 256 {
		return nil, keys, errors.New("address lookup has too many indexes")
	}
	indexes := make([]uint8, count)
	if err := reader.fixed(indexes); err != nil {
		return nil, keys, err
	}
	for _, index := range indexes {
		if int(index) >= len(table) {
			return nil, keys, errors.New("address lookup index is out of range")
		}
		keys = append(keys, table[index])
	}
	return indexes, keys, nil
}
