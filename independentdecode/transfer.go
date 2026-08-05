package independentdecode

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
)

const (
	messageBytes     = 150
	transactionBytes = 1 + ed25519.SignatureSize + messageBytes
)

type Transfer struct {
	Source          [32]byte
	Destination     [32]byte
	RecentBlockhash [32]byte
	Lamports        uint64
}

type SignedTransfer struct {
	Transfer
	Signature [64]byte
	Message   [messageBytes]byte
}

// DecodeMessage accepts only the fixed legacy-message shape used by
// treasury_sweep_v1. It deliberately does not share the general Solana parser.
func DecodeMessage(message []byte) (Transfer, error) {
	var transfer Transfer
	if len(message) != messageBytes {
		return transfer, errors.New("message length is not the fixed transfer shape")
	}
	if !bytes.Equal(message[:4], []byte{1, 0, 1, 3}) {
		return transfer, errors.New("message header or account count is invalid")
	}
	copy(transfer.Source[:], message[4:36])
	copy(transfer.Destination[:], message[36:68])
	if transfer.Source == transfer.Destination {
		return Transfer{}, errors.New("source and destination must differ")
	}
	if !allZero(message[68:100]) {
		return Transfer{}, errors.New("instruction program is not the System Program")
	}
	copy(transfer.RecentBlockhash[:], message[100:132])
	if !bytes.Equal(message[132:142], []byte{
		1, // one instruction
		2, // System Program account index
		2, // two instruction accounts
		0, 1,
		12,         // instruction data bytes
		2, 0, 0, 0, // SystemInstruction::Transfer
	}) {
		return Transfer{}, errors.New("instruction is not the fixed System transfer")
	}
	transfer.Lamports = binary.LittleEndian.Uint64(message[142:150])
	if transfer.Lamports == 0 {
		return Transfer{}, errors.New("transfer amount must be positive")
	}
	return transfer, nil
}

func DecodeSigned(transaction []byte) (SignedTransfer, error) {
	var signed SignedTransfer
	if len(transaction) != transactionBytes || transaction[0] != 1 {
		return signed, errors.New("transaction is not the fixed one-signature shape")
	}
	copy(signed.Signature[:], transaction[1:65])
	copy(signed.Message[:], transaction[65:])
	transfer, err := DecodeMessage(signed.Message[:])
	if err != nil {
		return SignedTransfer{}, err
	}
	if !ed25519.Verify(
		ed25519.PublicKey(transfer.Source[:]),
		signed.Message[:],
		signed.Signature[:],
	) {
		return SignedTransfer{}, errors.New("transaction signature is invalid")
	}
	signed.Transfer = transfer
	return signed, nil
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}
