package solana

import (
	"encoding/hex"
	"testing"
)

func TestSetComputeUnitLimitInstructionUsesCanonicalWireFormat(t *testing.T) {
	instruction, err := SetComputeUnitLimitInstruction(1_400_000)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.Program != ComputeBudgetProgram || len(instruction.Accounts) != 0 ||
		hex.EncodeToString(instruction.Data) != "02c05c1500" {
		t.Fatalf("instruction = %+v", instruction)
	}
	for _, invalid := range []uint32{0, MaxComputeUnitLimit + 1} {
		if _, err := SetComputeUnitLimitInstruction(invalid); err == nil {
			t.Fatalf("accepted compute unit limit %d", invalid)
		}
	}
}
