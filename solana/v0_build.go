package solana

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
)

type v0TableSelection struct {
	table    [32]byte
	writable []uint8
	readonly []uint8
}

// BuildV0Message compiles a bounded, one-signer Solana v0 message. Address
// tables are compilation inputs only; callers must independently verify their
// on-chain contents before signing the resulting message.
func BuildV0Message(
	feePayer string,
	recentBlockhash string,
	instructions []Instruction,
	addressTables map[[32]byte][][32]byte,
) ([]byte, error) {
	return BuildV0MessageWithStaticAccounts(
		feePayer, recentBlockhash, instructions, addressTables, nil,
	)
}

// BuildV0MessageWithStaticAccounts compiles a v0 message while keeping the
// named accounts a policy service must inspect out of address lookup tables.
func BuildV0MessageWithStaticAccounts(
	feePayer string,
	recentBlockhash string,
	instructions []Instruction,
	addressTables map[[32]byte][][32]byte,
	staticAccounts []string,
) ([]byte, error) {
	feePayerKey, err := Decode32(feePayer)
	if err != nil {
		return nil, fmt.Errorf("fee payer: %w", err)
	}
	blockhash, err := Decode32(recentBlockhash)
	if err != nil {
		return nil, fmt.Errorf("recent blockhash: %w", err)
	}
	if len(instructions) == 0 || len(instructions) > 64 {
		return nil, errors.New("instruction count must be between 1 and 64")
	}
	if len(staticAccounts) > 64 {
		return nil, errors.New("v0 static account allowlist is too large")
	}
	forcedStatic := make(map[[32]byte]struct{}, len(staticAccounts))
	for _, address := range staticAccounts {
		key, err := Decode32(address)
		if err != nil {
			return nil, errors.New("v0 static account is invalid")
		}
		forcedStatic[key] = struct{}{}
	}

	metas := map[[32]byte]*keyedMeta{}
	ordered := make([]*keyedMeta, 0, 64)
	add := func(key [32]byte, signer, writable bool) error {
		if existing := metas[key]; existing != nil {
			existing.signer = existing.signer || signer
			existing.writable = existing.writable || writable
			return nil
		}
		if len(ordered) >= 256 {
			return errors.New("v0 message has too many accounts")
		}
		meta := &keyedMeta{key: key, signer: signer, writable: writable, order: len(ordered)}
		metas[key] = meta
		ordered = append(ordered, meta)
		return nil
	}
	if err := add(feePayerKey, true, true); err != nil {
		return nil, err
	}
	programs := make(map[[32]byte]struct{}, len(instructions))
	for instructionIndex, instruction := range instructions {
		if len(instruction.Accounts) > 255 || len(instruction.Data) > maxTransactionBytes {
			return nil, fmt.Errorf("instruction %d exceeds v0 message limits", instructionIndex)
		}
		for _, account := range instruction.Accounts {
			key, err := Decode32(account.Address)
			if err != nil {
				return nil, fmt.Errorf("instruction %d account: %w", instructionIndex, err)
			}
			if account.Signer && key != feePayerKey {
				return nil, errors.New("v0 message requires an unsupported additional signer")
			}
			if err := add(key, account.Signer, account.Writable); err != nil {
				return nil, err
			}
		}
		program, err := Decode32(instruction.Program)
		if err != nil {
			return nil, fmt.Errorf("instruction %d program: %w", instructionIndex, err)
		}
		if program == feePayerKey {
			return nil, errors.New("fee payer cannot also be an instruction program")
		}
		programs[program] = struct{}{}
		if err := add(program, false, false); err != nil {
			return nil, err
		}
	}

	tableIDs := make([][32]byte, 0, len(addressTables))
	for table := range addressTables {
		tableIDs = append(tableIDs, table)
	}
	sort.Slice(tableIDs, func(i, j int) bool {
		return string(tableIDs[i][:]) < string(tableIDs[j][:])
	})
	type location struct {
		table [32]byte
		index uint8
	}
	locations := make(map[[32]byte]location)
	for _, table := range tableIDs {
		accounts := addressTables[table]
		if len(accounts) == 0 || len(accounts) > 256 {
			return nil, errors.New("v0 address table size is invalid")
		}
		for index, account := range accounts {
			if _, exists := locations[account]; !exists {
				locations[account] = location{table: table, index: uint8(index)}
			}
		}
	}

	selected := make(map[[32]byte]location)
	static := make([]*keyedMeta, 0, len(ordered))
	for _, meta := range ordered {
		location, inTable := locations[meta.key]
		_, isProgram := programs[meta.key]
		_, forced := forcedStatic[meta.key]
		if !meta.signer && !isProgram && !forced && inTable {
			selected[meta.key] = location
			continue
		}
		static = append(static, meta)
	}
	for key := range forcedStatic {
		if metas[key] == nil {
			return nil, errors.New("v0 static account is not used by the message")
		}
	}
	grouped := make([]*keyedMeta, 0, len(static))
	for _, want := range []struct{ signer, writable bool }{
		{true, true}, {true, false}, {false, true}, {false, false},
	} {
		for _, meta := range static {
			if meta.signer == want.signer && meta.writable == want.writable {
				grouped = append(grouped, meta)
			}
		}
	}
	if len(grouped) == 0 || grouped[0].key != feePayerKey {
		return nil, errors.New("fee payer ordering failed")
	}
	readonlyUnsigned := 0
	indexByKey := make(map[[32]byte]uint8, len(ordered))
	for index, meta := range grouped {
		indexByKey[meta.key] = uint8(index)
		if !meta.signer && !meta.writable {
			readonlyUnsigned++
		}
	}

	selections := make([]v0TableSelection, 0, len(tableIDs))
	for _, table := range tableIDs {
		selection := v0TableSelection{table: table}
		for index, account := range addressTables[table] {
			location, ok := selected[account]
			if !ok || location.table != table || int(location.index) != index {
				continue
			}
			if metas[account].writable {
				selection.writable = append(selection.writable, uint8(index))
			} else {
				selection.readonly = append(selection.readonly, uint8(index))
			}
		}
		if len(selection.writable)+len(selection.readonly) > 0 {
			selections = append(selections, selection)
		}
	}
	accountIndex := len(grouped)
	for _, selection := range selections {
		for _, index := range selection.writable {
			indexByKey[addressTables[selection.table][index]] = uint8(accountIndex)
			accountIndex++
		}
	}
	for _, selection := range selections {
		for _, index := range selection.readonly {
			indexByKey[addressTables[selection.table][index]] = uint8(accountIndex)
			accountIndex++
		}
	}
	if accountIndex > 256 {
		return nil, errors.New("v0 message has too many resolved accounts")
	}

	message := []byte{0x80, 1, 0, byte(readonlyUnsigned)}
	message = appendShortVec(message, len(grouped))
	for _, meta := range grouped {
		message = append(message, meta.key[:]...)
	}
	message = append(message, blockhash[:]...)
	message = appendShortVec(message, len(instructions))
	for instructionIndex, instruction := range instructions {
		program, _ := Decode32(instruction.Program)
		message = append(message, indexByKey[program])
		message = appendShortVec(message, len(instruction.Accounts))
		for _, account := range instruction.Accounts {
			key, _ := Decode32(account.Address)
			message = append(message, indexByKey[key])
		}
		message = appendShortVec(message, len(instruction.Data))
		message = append(message, instruction.Data...)
		if len(message)+1+ed25519.SignatureSize > maxTransactionBytes {
			return nil, fmt.Errorf("instruction %d makes transaction exceed Solana packet limit", instructionIndex)
		}
	}
	message = appendShortVec(message, len(selections))
	for _, selection := range selections {
		message = append(message, selection.table[:]...)
		message = appendShortVec(message, len(selection.writable))
		message = append(message, selection.writable...)
		message = appendShortVec(message, len(selection.readonly))
		message = append(message, selection.readonly...)
	}
	if len(message)+1+ed25519.SignatureSize > maxTransactionBytes {
		return nil, errors.New("v0 transaction exceeds Solana packet limit")
	}
	decoded, err := DecodeV0Message(message, addressTables)
	if err != nil {
		return nil, fmt.Errorf("self-check v0 message: %w", err)
	}
	if err := ValidateV0MessageForSigner(decoded, feePayer); err != nil {
		return nil, fmt.Errorf("self-check v0 signer shape: %w", err)
	}
	return message, nil
}

// BuildUnsignedV0Transaction adds the one empty signature slot in Solana's
// canonical wire transaction. Transaction-aware custody services consume this
// form and replace the empty slot without changing the message.
func BuildUnsignedV0Transaction(message []byte, tables map[[32]byte][][32]byte) ([]byte, error) {
	decoded, err := DecodeV0Message(message, tables)
	if err != nil {
		return nil, err
	}
	if decoded.RequiredSignatures != 1 {
		return nil, errors.New("v0 transaction requires exactly one signature slot")
	}
	transaction := make([]byte, 0, 1+ed25519.SignatureSize+len(message))
	transaction = appendShortVec(transaction, 1)
	transaction = append(transaction, make([]byte, ed25519.SignatureSize)...)
	transaction = append(transaction, message...)
	if len(transaction) > maxTransactionBytes {
		return nil, errors.New("transaction exceeds Solana packet limit")
	}
	return transaction, nil
}

// BuildV0SimulationTransaction returns the same canonical unsigned wire form
// used by simulateTransaction when signature verification is disabled.
func BuildV0SimulationTransaction(message []byte, tables map[[32]byte][][32]byte) ([]byte, error) {
	return BuildUnsignedV0Transaction(message, tables)
}
