package solana

import (
	"encoding/binary"
	"errors"
)

const (
	// ComputeBudgetProgram is Solana's native compute-budget program address.
	ComputeBudgetProgram = "ComputeBudget111111111111111111111111111111"
	// MaxComputeUnitLimit is Solana's per-transaction compute-unit ceiling.
	MaxComputeUnitLimit = uint32(1_400_000)
)

// SetComputeUnitLimitInstruction returns Solana's canonical compute-unit limit
// instruction. Jupiter /build callers must supply this after estimating usage.
func SetComputeUnitLimitInstruction(units uint32) (Instruction, error) {
	if units == 0 || units > MaxComputeUnitLimit {
		return Instruction{}, errors.New("compute unit limit is invalid")
	}
	data := make([]byte, 5)
	data[0] = 2
	binary.LittleEndian.PutUint32(data[1:], units)
	return Instruction{Program: ComputeBudgetProgram, Data: data}, nil
}
