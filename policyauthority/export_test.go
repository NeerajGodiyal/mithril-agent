package policyauthority

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

// WalletAdmissionFixture exists only in the test binary, avoiding a production
// test hook or an import cycle between authority's internal tests and execution.
type WalletAdmissionFixture struct {
	Policy                            Policy
	Paper                             shadow.Policy
	Ticks                             []shadow.Tick
	Bounds                            proposalcheck.PaperIntentBounds
	Candidate                         proposalcheck.Candidate
	Evidence                          proposalcheck.NativeReserveEvidence
	Primary, Secondary                proposalcheck.FinalizedSlotReader
	Start                             int64
	Now                               time.Time
	MaxDecisionAge, MaxAcquisitionAge time.Duration
	AcquisitionPath                   string
}

// WalletAdmissionBuildForTest extracts the already-validated fixture's upstream
// instructions, unwrapping the host guard and excluding compute-unit limits.
func WalletAdmissionBuildForTest(t *testing.T, candidate proposalcheck.Candidate) jupiterquote.BuildResult {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(candidate.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	message, err := solana.DecodeV0Message(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := jupiterquote.BuildResult{Quote: candidate.Quote, RecentBlockhash: message.RecentBlockhash, LastValidBlockHeight: candidate.LastValidBlockHeight}
	for _, compiled := range message.Instructions {
		instruction := solana.Instruction{Program: solana.Encode(message.AccountKeys[compiled.ProgramIndex][:]), Data: compiled.Data}
		for _, index := range compiled.Accounts {
			instruction.Accounts = append(instruction.Accounts, solana.AccountMeta{Address: solana.Encode(message.AccountKeys[index][:]), Signer: message.IsSigner(int(index)), Writable: message.IsWritable(int(index))})
		}
		// Compilation unions account privileges. Restore this fixture's original
		// per-instruction privileges before upstream semantic validation.
		switch instruction.Program {
		case orcaswap.AssociatedTokenProgram:
			for index := 2; index < len(instruction.Accounts); index++ {
				instruction.Accounts[index].Signer = false
				instruction.Accounts[index].Writable = false
			}
		case orcaswap.TokenProgram:
			if len(instruction.Data) == 1 && instruction.Data[0] == 9 && len(instruction.Accounts) == 3 {
				instruction.Accounts[1].Signer = false
				instruction.Accounts[2].Writable = false
			}
		case candidate.Policy.RouteGuard.Program:
			if len(instruction.Accounts) < 11 {
				t.Fatal("fixture has no guarded route_v2")
			}
			instruction.Accounts[1].Writable = false
		}
		if instruction.Program == solana.ComputeBudgetProgram {
			if len(instruction.Data) != 0 && instruction.Data[0] == 3 {
				result.ComputeBudget = append(result.ComputeBudget, instruction)
			}
		} else {
			result.Instructions = append(result.Instructions, instruction)
		}
	}
	result.Instructions, err = jupiterswap.UnwrapRoutePlan(candidate.Policy.RouteGuard, result.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jupiterswap.ValidateExactInSOL(candidate.Request, result.Quote, result.Instructions); err != nil {
		t.Fatalf("invalid reconstructed upstream fixture: %v", err)
	}
	return result
}

func NewWalletAdmissionFixture(t *testing.T, primary, secondary string, native uint64) WalletAdmissionFixture {
	t.Helper()
	f := newPaperRequestFixture(t)
	f.policy.JupiterProviders.PrimaryOriginSHA256 = primary
	f.policy.JupiterProviders.SecondaryOriginSHA256 = secondary
	f.primary.identity, f.secondary.identity = primary, secondary
	evidence := f.evidence.(*paperReserveEvidence)
	proposal := evidence.Evidence.(*jupiterEvidence)
	proposal.primary, proposal.secondary = primary, secondary
	evidence.mutate = func(value *txflow.AccountEvidence) {
		value.PrimaryLamports, value.SecondaryLamports = native, native
	}
	return WalletAdmissionFixture{Policy: f.policy, Paper: f.paper, Ticks: f.ticks, Bounds: f.bounds,
		Candidate: f.candidate, Evidence: f.evidence, Primary: f.primary, Secondary: f.secondary,
		Start: f.start, Now: f.now, MaxDecisionAge: f.maxDecisionAge,
		MaxAcquisitionAge: f.maxAcquisitionAge, AcquisitionPath: f.acquisitionPath}
}
