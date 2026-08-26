package main

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type fixedProgramDeploymentReader map[string]solanarpc.AccountDataSlice

func (reader fixedProgramDeploymentReader) AccountData(
	_ context.Context, address string, _, _ uint64,
) (solanarpc.AccountDataSlice, error) {
	return reader[address], nil
}

func testLegacyProgramDeployment(
	t *testing.T, slot uint64, bankhash string,
) (solanarpc.AccountDataSlice, programDeploymentEvidence) {
	t.Helper()
	account := testLegacyProgramDeploymentAccount(slot, bankhash)
	evidence, err := readProgramDeployment(t.Context(), fixedProgramDeploymentReader{
		programCommandAddress: account,
	}, programCommandAddress, slot)
	if err != nil {
		t.Fatal(err)
	}
	return account, evidence
}

func testLegacyProgramDeploymentAccount(slot uint64, bankhash string) solanarpc.AccountDataSlice {
	data := []byte("immutable test program")
	return solanarpc.AccountDataSlice{
		ContextSlot: slot, Bankhash: bankhash, Owner: programSimulationState,
		Executable: true, DataLength: uint64(len(data)), Data: data,
	}
}

func TestReadProgramDeploymentBindsUpgradeableCodeAtOneBank(t *testing.T) {
	programDataAddress := "SysvarC1ock11111111111111111111111111111111"
	programDataKey, err := solana.Decode32(programDataAddress)
	if err != nil {
		t.Fatal(err)
	}
	programAccountData := make([]byte, 36)
	binary.LittleEndian.PutUint32(programAccountData, 2)
	copy(programAccountData[4:], programDataKey[:])
	programData := make([]byte, 45)
	binary.LittleEndian.PutUint32(programData, 3)
	binary.LittleEndian.PutUint64(programData[4:], 77)
	reader := fixedProgramDeploymentReader{
		programCommandAddress: {
			ContextSlot: 42, Bankhash: programSimulationState, Owner: upgradeableLoaderProgram,
			Executable: true, DataLength: uint64(len(programAccountData)), Data: programAccountData,
		},
		programDataAddress: {
			ContextSlot: 42, Bankhash: programSimulationState, Owner: upgradeableLoaderProgram,
			DataLength: uint64(len(programData)), Data: programData,
		},
	}
	evidence, err := readProgramDeployment(t.Context(), reader, programCommandAddress, 42)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ProgramData != programDataAddress || evidence.DeploymentSlot != 77 ||
		evidence.ProgramDataSHA256 == "" || evidence.SHA256 == "" || evidence.ContextSlot != 42 ||
		evidence.Bankhash != programSimulationState {
		t.Fatalf("deployment evidence = %+v", evidence)
	}

	mismatched := reader[programDataAddress]
	mismatched.Bankhash = solana.DevnetGenesisHash
	reader[programDataAddress] = mismatched
	if _, err := readProgramDeployment(t.Context(), reader, programCommandAddress, 42); err == nil {
		t.Fatal("program-data from another processed bank was accepted")
	}
}
