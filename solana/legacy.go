package solana

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
)

// AccountMeta describes one account used by a legacy Solana instruction.
type AccountMeta struct {
	Address  string `json:"address"`
	Signer   bool   `json:"signer"`
	Writable bool   `json:"writable"`
}

// Instruction is the program, account list, and opaque data for one instruction.
type Instruction struct {
	Program  string        `json:"program"`
	Accounts []AccountMeta `json:"accounts"`
	Data     []byte        `json:"data"`
}

type CompiledInstruction struct {
	ProgramIndex uint8
	Accounts     []uint8
	Data         []byte
}

// LegacyMessage is a decoded, address-lookup-free Solana message.
type LegacyMessage struct {
	RequiredSignatures uint8
	ReadonlySigned     uint8
	ReadonlyUnsigned   uint8
	AccountKeys        [][32]byte
	RecentBlockhash    [32]byte
	Instructions       []CompiledInstruction
	Raw                []byte
}

// SignedLegacyTransaction is a verified one-signer legacy transaction.
type SignedLegacyTransaction struct {
	Signature [64]byte
	Message   LegacyMessage
}

func (m LegacyMessage) IsSigner(index int) bool {
	return index >= 0 && index < int(m.RequiredSignatures)
}

func (m LegacyMessage) IsWritable(index int) bool {
	if index < 0 || index >= len(m.AccountKeys) {
		return false
	}
	if index < int(m.RequiredSignatures) {
		return index < int(m.RequiredSignatures-m.ReadonlySigned)
	}
	return index < len(m.AccountKeys)-int(m.ReadonlyUnsigned)
}

type keyedMeta struct {
	key      [32]byte
	signer   bool
	writable bool
	order    int
}

// BuildLegacyMessage compiles a bounded one-signer legacy message. The fee payer
// is always the sole signer; callers must validate instruction semantics before
// asking a signer to authorize the result.
func BuildLegacyMessage(
	feePayer string,
	recentBlockhash string,
	instructions []Instruction,
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

	metas := map[[32]byte]*keyedMeta{}
	ordered := make([]*keyedMeta, 0, 32)
	add := func(key [32]byte, signer, writable bool) error {
		if existing := metas[key]; existing != nil {
			existing.signer = existing.signer || signer
			existing.writable = existing.writable || writable
			return nil
		}
		if len(ordered) >= 256 {
			return errors.New("legacy message has too many accounts")
		}
		meta := &keyedMeta{key: key, signer: signer, writable: writable, order: len(ordered)}
		metas[key] = meta
		ordered = append(ordered, meta)
		return nil
	}
	if err := add(feePayerKey, true, true); err != nil {
		return nil, err
	}
	for index, instruction := range instructions {
		if len(instruction.Accounts) > 255 || len(instruction.Data) > maxTransactionBytes {
			return nil, fmt.Errorf("instruction %d exceeds legacy message limits", index)
		}
		for _, account := range instruction.Accounts {
			key, err := Decode32(account.Address)
			if err != nil {
				return nil, fmt.Errorf("instruction %d account: %w", index, err)
			}
			if account.Signer && key != feePayerKey {
				return nil, errors.New("legacy message requires an unsupported additional signer")
			}
			if err := add(key, account.Signer, account.Writable); err != nil {
				return nil, err
			}
		}
		program, err := Decode32(instruction.Program)
		if err != nil {
			return nil, fmt.Errorf("instruction %d program: %w", index, err)
		}
		if program == feePayerKey {
			return nil, errors.New("fee payer cannot also be an instruction program")
		}
		if err := add(program, false, false); err != nil {
			return nil, err
		}
	}

	// Solana legacy messages group accounts by signer and writability. Stable
	// ordering within each group keeps the output deterministic.
	grouped := make([]*keyedMeta, 0, len(ordered))
	for _, want := range []struct{ signer, writable bool }{
		{true, true}, {true, false}, {false, true}, {false, false},
	} {
		for _, meta := range ordered {
			if meta.signer == want.signer && meta.writable == want.writable {
				grouped = append(grouped, meta)
			}
		}
	}
	if len(grouped) == 0 || grouped[0].key != feePayerKey {
		return nil, errors.New("fee payer ordering failed")
	}
	indexByKey := make(map[[32]byte]uint8, len(grouped))
	readonlyUnsigned := 0
	for index, meta := range grouped {
		indexByKey[meta.key] = uint8(index)
		if !meta.signer && !meta.writable {
			readonlyUnsigned++
		}
	}
	if readonlyUnsigned > 255 {
		return nil, errors.New("legacy message readonly account count exceeds one byte")
	}

	message := make([]byte, 0, maxTransactionBytes)
	message = append(message, 1, 0, byte(readonlyUnsigned))
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
	if _, err := DecodeLegacyMessage(message); err != nil {
		return nil, fmt.Errorf("self-check legacy message: %w", err)
	}
	return message, nil
}

func DecodeLegacyMessage(message []byte) (LegacyMessage, error) {
	var decoded LegacyMessage
	if len(message) == 0 || len(message)+1+ed25519.SignatureSize > maxTransactionBytes {
		return decoded, errors.New("legacy message size is invalid")
	}
	reader := byteReader{data: message}
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
	if required != 1 || readonlySigned != 0 {
		return decoded, errors.New("legacy message must have exactly one writable signer")
	}
	accountCount, err := reader.shortVec()
	if err != nil {
		return decoded, fmt.Errorf("account count: %w", err)
	}
	if accountCount == 0 || accountCount > 256 ||
		int(readonlyUnsigned) > accountCount-int(required) {
		return decoded, errors.New("legacy message account header is invalid")
	}
	decoded.AccountKeys = make([][32]byte, accountCount)
	seen := make(map[[32]byte]struct{}, accountCount)
	for index := range decoded.AccountKeys {
		if err := reader.fixed(decoded.AccountKeys[index][:]); err != nil {
			return LegacyMessage{}, err
		}
		if _, exists := seen[decoded.AccountKeys[index]]; exists {
			return LegacyMessage{}, errors.New("legacy message contains a duplicate account key")
		}
		seen[decoded.AccountKeys[index]] = struct{}{}
	}
	if err := reader.fixed(decoded.RecentBlockhash[:]); err != nil {
		return LegacyMessage{}, err
	}
	instructionCount, err := reader.shortVec()
	if err != nil {
		return LegacyMessage{}, fmt.Errorf("instruction count: %w", err)
	}
	if instructionCount == 0 || instructionCount > 64 {
		return LegacyMessage{}, errors.New("legacy message instruction count is invalid")
	}
	decoded.Instructions = make([]CompiledInstruction, instructionCount)
	for index := range decoded.Instructions {
		programIndex, err := reader.byte()
		if err != nil {
			return LegacyMessage{}, err
		}
		if int(programIndex) >= accountCount || int(programIndex) < int(required) {
			return LegacyMessage{}, errors.New("legacy instruction program index is invalid")
		}
		accountIndexes, err := reader.shortVec()
		if err != nil {
			return LegacyMessage{}, fmt.Errorf("instruction account count: %w", err)
		}
		if accountIndexes > 255 {
			return LegacyMessage{}, errors.New("legacy instruction has too many accounts")
		}
		instruction := &decoded.Instructions[index]
		instruction.ProgramIndex = programIndex
		instruction.Accounts = make([]uint8, accountIndexes)
		for accountIndex := range instruction.Accounts {
			value, err := reader.byte()
			if err != nil {
				return LegacyMessage{}, err
			}
			if int(value) >= accountCount {
				return LegacyMessage{}, errors.New("legacy instruction account index is invalid")
			}
			instruction.Accounts[accountIndex] = value
		}
		dataLength, err := reader.shortVec()
		if err != nil {
			return LegacyMessage{}, fmt.Errorf("instruction data length: %w", err)
		}
		instruction.Data = make([]byte, dataLength)
		if err := reader.fixed(instruction.Data); err != nil {
			return LegacyMessage{}, err
		}
	}
	if reader.remaining() != 0 {
		return LegacyMessage{}, errors.New("legacy message contains trailing bytes")
	}
	decoded.RequiredSignatures = required
	decoded.ReadonlySigned = readonlySigned
	decoded.ReadonlyUnsigned = readonlyUnsigned
	decoded.Raw = bytes.Clone(message)
	return decoded, nil
}

func SignLegacyMessage(privateKey ed25519.PrivateKey, message []byte) ([]byte, [64]byte, error) {
	var signature [64]byte
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, signature, errors.New("private key must be 64 bytes")
	}
	decoded, err := DecodeLegacyMessage(message)
	if err != nil {
		return nil, signature, err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, decoded.AccountKeys[0][:]) {
		return nil, signature, errors.New("private key does not match message fee payer")
	}
	copy(signature[:], ed25519.Sign(privateKey, message))
	transaction := make([]byte, 0, 1+len(signature)+len(message))
	transaction = appendShortVec(transaction, 1)
	transaction = append(transaction, signature[:]...)
	transaction = append(transaction, message...)
	if len(transaction) > maxTransactionBytes {
		return nil, [64]byte{}, errors.New("transaction exceeds Solana packet limit")
	}
	if _, err := DecodeSignedLegacyTransaction(transaction); err != nil {
		return nil, [64]byte{}, fmt.Errorf("self-check signed legacy transaction: %w", err)
	}
	return transaction, signature, nil
}

func DecodeSignedLegacyTransaction(transaction []byte) (SignedLegacyTransaction, error) {
	var signed SignedLegacyTransaction
	if len(transaction) == 0 || len(transaction) > maxTransactionBytes {
		return signed, errors.New("transaction size is invalid")
	}
	reader := byteReader{data: transaction}
	signatureCount, err := reader.shortVec()
	if err != nil {
		return signed, fmt.Errorf("signature count: %w", err)
	}
	if signatureCount != 1 {
		return signed, errors.New("transaction must contain exactly one signature")
	}
	if err := reader.fixed(signed.Signature[:]); err != nil {
		return signed, err
	}
	message, err := DecodeLegacyMessage(transaction[reader.offset:])
	if err != nil {
		return signed, err
	}
	if !ed25519.Verify(ed25519.PublicKey(message.AccountKeys[0][:]), message.Raw, signed.Signature[:]) {
		return SignedLegacyTransaction{}, errors.New("transaction signature is invalid")
	}
	signed.Message = message
	return signed, nil
}

// BuildLegacySimulationTransaction supplies the required empty signature for
// simulateTransaction with signature verification disabled.
func BuildLegacySimulationTransaction(message []byte) ([]byte, error) {
	if _, err := DecodeLegacyMessage(message); err != nil {
		return nil, err
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
