package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

const upgradeableLoaderProgram = "BPFLoaderUpgradeab1e11111111111111111111111"

type programDeploymentReader interface {
	AccountData(context.Context, string, uint64, uint64) (solanarpc.AccountDataSlice, error)
}

type programDeploymentEvidence struct {
	Program              string `json:"program"`
	Owner                string `json:"owner"`
	ProgramData          string `json:"program_data,omitempty"`
	DeploymentSlot       uint64 `json:"deployment_slot,omitempty"`
	ProgramAccountSHA256 string `json:"program_account_sha256"`
	ProgramDataSHA256    string `json:"program_data_sha256,omitempty"`
	SHA256               string `json:"sha256"`
	ContextSlot          uint64 `json:"context_slot"`
	Bankhash             string `json:"bankhash"`
}

func readProgramDeployment(
	ctx context.Context,
	reader programDeploymentReader,
	program string,
	minContextSlot uint64,
) (programDeploymentEvidence, error) {
	if _, err := solana.Decode32(program); err != nil || minContextSlot == 0 {
		return programDeploymentEvidence{}, errors.New("program deployment request is invalid")
	}
	account, err := reader.AccountData(ctx, program, minContextSlot, programinterface.MaxAccountDataBytes)
	if err != nil {
		return programDeploymentEvidence{}, errors.New("read deployed program account through Mithril")
	}
	if account.ContextSlot < minContextSlot || !validProgramBankhash(account.Bankhash) ||
		!account.Executable || account.DataLength != uint64(len(account.Data)) {
		return programDeploymentEvidence{}, errors.New("deployed program account evidence is incomplete")
	}
	if _, err := solana.Decode32(account.Owner); err != nil {
		return programDeploymentEvidence{}, errors.New("deployed program owner is invalid")
	}
	evidence := programDeploymentEvidence{
		Program: program, Owner: account.Owner,
		ProgramAccountSHA256: hashProgramDeploymentBytes(account.Data),
		ContextSlot:          account.ContextSlot, Bankhash: account.Bankhash,
	}
	if account.Owner == upgradeableLoaderProgram {
		if len(account.Data) != 36 || binary.LittleEndian.Uint32(account.Data[:4]) != 2 {
			return programDeploymentEvidence{}, errors.New("upgradeable program account is invalid")
		}
		evidence.ProgramData = solana.Encode(account.Data[4:])
		programData, err := reader.AccountData(
			ctx, evidence.ProgramData, account.ContextSlot, programinterface.MaxAccountDataBytes,
		)
		if err != nil {
			return programDeploymentEvidence{}, errors.New("read deployed program-data account through Mithril")
		}
		if programData.ContextSlot != account.ContextSlot || programData.Bankhash != account.Bankhash ||
			programData.Owner != upgradeableLoaderProgram || programData.Executable ||
			programData.DataLength != uint64(len(programData.Data)) || len(programData.Data) < 45 ||
			binary.LittleEndian.Uint32(programData.Data[:4]) != 3 {
			return programDeploymentEvidence{}, errors.New("upgradeable program-data evidence is incomplete or from another processed bank")
		}
		evidence.DeploymentSlot = binary.LittleEndian.Uint64(programData.Data[4:12])
		if evidence.DeploymentSlot == 0 {
			return programDeploymentEvidence{}, errors.New("upgradeable program deployment slot is invalid")
		}
		evidence.ProgramDataSHA256 = hashProgramDeploymentBytes(programData.Data)
	}
	encoded, err := json.Marshal(struct {
		Domain               string `json:"domain"`
		Program              string `json:"program"`
		Owner                string `json:"owner"`
		ProgramData          string `json:"program_data,omitempty"`
		DeploymentSlot       uint64 `json:"deployment_slot,omitempty"`
		ProgramAccountSHA256 string `json:"program_account_sha256"`
		ProgramDataSHA256    string `json:"program_data_sha256,omitempty"`
	}{
		Domain: "mithril-agent/program-deployment-v1", Program: evidence.Program,
		Owner: evidence.Owner, ProgramData: evidence.ProgramData,
		DeploymentSlot: evidence.DeploymentSlot, ProgramAccountSHA256: evidence.ProgramAccountSHA256,
		ProgramDataSHA256: evidence.ProgramDataSHA256,
	})
	if err != nil {
		return programDeploymentEvidence{}, errors.New("encode program deployment identity")
	}
	sum := sha256.Sum256(encoded)
	evidence.SHA256 = hex.EncodeToString(sum[:])
	return evidence, nil
}

func hashProgramDeploymentBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func applicableProgramEvidence(
	ctx context.Context,
	workspace programWorkspace,
	interfaceSHA256 string,
	evidence []programinterface.EvidenceResult,
) ([]programinterface.EvidenceResult, error) {
	genesis, err := expectedProgramGenesis(workspace.Cluster, workspace.GenesisHash)
	if err != nil {
		return nil, err
	}
	candidates := make([]programinterface.EvidenceResult, 0, len(evidence))
	var minContextSlot uint64
	for _, item := range evidence {
		attestation := item.Attestation
		if attestation.Version != 3 || attestation.InterfaceSHA256 != interfaceSHA256 ||
			attestation.GenesisHash != genesis {
			continue
		}
		candidates = append(candidates, item)
		if attestation.ContextSlot > minContextSlot {
			minContextSlot = attestation.ContextSlot
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	node, err := openProgramPMPNode(workspace.NodeRPC)
	if err != nil {
		return nil, errors.New("open Mithril node for program evidence applicability")
	}
	if err := verifyProgramNodeCluster(ctx, node, workspace.Cluster, workspace.GenesisHash); err != nil {
		return nil, err
	}
	deployment, err := readProgramDeployment(ctx, node, workspace.Program, minContextSlot)
	if err != nil {
		return nil, errors.New("verify current program deployment applicability")
	}
	applicable := candidates[:0]
	for _, item := range candidates {
		if item.Attestation.DeploymentSHA256 == deployment.SHA256 {
			applicable = append(applicable, item)
		}
	}
	return applicable, nil
}
