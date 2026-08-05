package solana

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	maxTransactionBytes  = 1232
	systemTransferOpcode = uint32(2)
)

var systemProgram = [32]byte{}

type Transfer struct {
	Source          [32]byte
	Destination     [32]byte
	RecentBlockhash [32]byte
	Lamports        uint64
}

type SignedTransfer struct {
	Transfer
	Signature [64]byte
	Message   []byte
}

func BuildTransferMessage(source, destination, recentBlockhash string, lamports uint64) ([]byte, error) {
	if lamports == 0 {
		return nil, errors.New("transfer amount must be positive")
	}
	sourceKey, err := Decode32(source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	destinationKey, err := Decode32(destination)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	if sourceKey == destinationKey {
		return nil, errors.New("source and destination must differ")
	}
	blockhash, err := Decode32(recentBlockhash)
	if err != nil {
		return nil, fmt.Errorf("recent blockhash: %w", err)
	}

	message := make([]byte, 0, 150)
	message = append(message, 1, 0, 1)
	message = appendShortVec(message, 3)
	message = append(message, sourceKey[:]...)
	message = append(message, destinationKey[:]...)
	message = append(message, systemProgram[:]...)
	message = append(message, blockhash[:]...)
	message = appendShortVec(message, 1)
	message = append(message, 2)
	message = appendShortVec(message, 2)
	message = append(message, 0, 1)
	message = appendShortVec(message, 12)
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[:4], systemTransferOpcode)
	binary.LittleEndian.PutUint64(data[4:], lamports)
	message = append(message, data...)

	if _, err := DecodeTransferMessage(message); err != nil {
		return nil, fmt.Errorf("self-check transfer message: %w", err)
	}
	return message, nil
}

func DecodeTransferMessage(message []byte) (Transfer, error) {
	var transfer Transfer
	reader := byteReader{data: message}
	required, err := reader.byte()
	if err != nil {
		return transfer, err
	}
	readonlySigned, err := reader.byte()
	if err != nil {
		return transfer, err
	}
	readonlyUnsigned, err := reader.byte()
	if err != nil {
		return transfer, err
	}
	if required != 1 || readonlySigned != 0 || readonlyUnsigned != 1 {
		return transfer, errors.New("message header is not a one-signer System transfer")
	}
	accountCount, err := reader.shortVec()
	if err != nil {
		return transfer, fmt.Errorf("account count: %w", err)
	}
	if accountCount != 3 {
		return transfer, errors.New("transfer must contain exactly three accounts")
	}
	if err := reader.fixed(transfer.Source[:]); err != nil {
		return transfer, err
	}
	if err := reader.fixed(transfer.Destination[:]); err != nil {
		return transfer, err
	}
	var program [32]byte
	if err := reader.fixed(program[:]); err != nil {
		return transfer, err
	}
	if program != systemProgram {
		return transfer, errors.New("instruction program is not the System Program")
	}
	if transfer.Source == transfer.Destination {
		return transfer, errors.New("source and destination must differ")
	}
	if err := reader.fixed(transfer.RecentBlockhash[:]); err != nil {
		return transfer, err
	}
	instructionCount, err := reader.shortVec()
	if err != nil {
		return transfer, fmt.Errorf("instruction count: %w", err)
	}
	if instructionCount != 1 {
		return transfer, errors.New("transfer must contain exactly one instruction")
	}
	programIndex, err := reader.byte()
	if err != nil {
		return transfer, err
	}
	if programIndex != 2 {
		return transfer, errors.New("instruction has the wrong program index")
	}
	instructionAccounts, err := reader.shortVec()
	if err != nil {
		return transfer, fmt.Errorf("instruction account count: %w", err)
	}
	if instructionAccounts != 2 {
		return transfer, errors.New("instruction must reference exactly two accounts")
	}
	sourceIndex, err := reader.byte()
	if err != nil {
		return transfer, err
	}
	destinationIndex, err := reader.byte()
	if err != nil {
		return transfer, err
	}
	if sourceIndex != 0 || destinationIndex != 1 {
		return transfer, errors.New("instruction account order is invalid")
	}
	dataLength, err := reader.shortVec()
	if err != nil {
		return transfer, fmt.Errorf("instruction data length: %w", err)
	}
	if dataLength != 12 {
		return transfer, errors.New("system transfer data must be 12 bytes")
	}
	data := make([]byte, dataLength)
	if err := reader.fixed(data); err != nil {
		return transfer, err
	}
	if binary.LittleEndian.Uint32(data[:4]) != systemTransferOpcode {
		return transfer, errors.New("instruction is not a System transfer")
	}
	transfer.Lamports = binary.LittleEndian.Uint64(data[4:])
	if transfer.Lamports == 0 {
		return transfer, errors.New("transfer amount must be positive")
	}
	if reader.remaining() != 0 {
		return transfer, errors.New("message contains trailing bytes")
	}
	return transfer, nil
}

func SignTransferMessage(privateKey ed25519.PrivateKey, message []byte) ([]byte, [64]byte, error) {
	var signature [64]byte
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, signature, errors.New("private key must be 64 bytes")
	}
	decoded, err := DecodeTransferMessage(message)
	if err != nil {
		return nil, signature, err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, decoded.Source[:]) {
		return nil, signature, errors.New("private key does not match transfer source")
	}
	copy(signature[:], ed25519.Sign(privateKey, message))
	transaction := make([]byte, 0, 1+len(signature)+len(message))
	transaction = appendShortVec(transaction, 1)
	transaction = append(transaction, signature[:]...)
	transaction = append(transaction, message...)
	if len(transaction) > maxTransactionBytes {
		return nil, [64]byte{}, errors.New("transaction exceeds Solana packet limit")
	}
	if _, err := DecodeSignedTransfer(transaction); err != nil {
		return nil, [64]byte{}, fmt.Errorf("self-check signed transfer: %w", err)
	}
	return transaction, signature, nil
}

func BuildSimulationTransaction(message []byte) ([]byte, error) {
	return BuildLegacySimulationTransaction(message)
}

func DecodeSignedTransfer(transaction []byte) (SignedTransfer, error) {
	var signed SignedTransfer
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
	signed.Message = bytes.Clone(transaction[reader.offset:])
	transfer, err := DecodeTransferMessage(signed.Message)
	if err != nil {
		return signed, err
	}
	signed.Transfer = transfer
	if !ed25519.Verify(ed25519.PublicKey(transfer.Source[:]), signed.Message, signed.Signature[:]) {
		return SignedTransfer{}, errors.New("transaction signature is invalid")
	}
	return signed, nil
}

func appendShortVec(dst []byte, value int) []byte {
	for {
		elem := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			return append(dst, elem)
		}
		dst = append(dst, elem|0x80)
	}
}

type byteReader struct {
	data   []byte
	offset int
}

func (r *byteReader) byte() (byte, error) {
	if r.offset >= len(r.data) {
		return 0, ioErrUnexpectedEOF()
	}
	value := r.data[r.offset]
	r.offset++
	return value, nil
}

func (r *byteReader) fixed(dst []byte) error {
	if len(dst) > len(r.data)-r.offset {
		return ioErrUnexpectedEOF()
	}
	copy(dst, r.data[r.offset:r.offset+len(dst)])
	r.offset += len(dst)
	return nil
}

func (r *byteReader) shortVec() (int, error) {
	start := r.offset
	value := 0
	shift := 0
	for i := 0; i < 3; i++ {
		elem, err := r.byte()
		if err != nil {
			return 0, err
		}
		value |= int(elem&0x7f) << shift
		if elem&0x80 == 0 {
			if value > maxTransactionBytes {
				return 0, errors.New("short vector exceeds transaction bound")
			}
			if !bytes.Equal(r.data[start:r.offset], appendShortVec(nil, value)) {
				return 0, errors.New("short vector is not canonical")
			}
			return value, nil
		}
		shift += 7
	}
	return 0, errors.New("short vector is too long")
}

func (r *byteReader) remaining() int {
	return len(r.data) - r.offset
}

func ioErrUnexpectedEOF() error {
	return errors.New("unexpected end of transaction")
}
