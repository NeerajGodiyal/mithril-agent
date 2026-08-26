package jupiterswap

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestValidateProposalAppliesOperatorLimits(t *testing.T) {
	request, quote, instructions := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 10, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	price := make([]byte, 9)
	price[0] = 3
	binary.LittleEndian.PutUint64(price[1:], 10)
	compute := []solana.Instruction{{Program: solana.ComputeBudgetProgram, Data: price}}
	if _, err := ValidateProposal(policy, request, quote, compute, instructions); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*Policy, *jupiterquote.Request, *jupiterquote.Result, []solana.Instruction){
		"input cap": func(_ *Policy, request *jupiterquote.Request, _ *jupiterquote.Result, _ []solana.Instruction) {
			request.InputAmount++
		},
		"output floor": func(policy *Policy, _ *jupiterquote.Request, _ *jupiterquote.Result, _ []solana.Instruction) {
			policy.MinOutputAmount++
		},
		"slippage": func(policy *Policy, request *jupiterquote.Request, _ *jupiterquote.Result, _ []solana.Instruction) {
			policy.MaxSlippageBPS--
			request.SlippageBPS++
		},
		"missing pre-created output": func(_ *Policy, request *jupiterquote.Request, _ *jupiterquote.Result, _ []solana.Instruction) {
			request.DestinationTokenAccount = ""
		},
		"priority fee": func(_ *Policy, _ *jupiterquote.Request, _ *jupiterquote.Result, compute []solana.Instruction) {
			binary.LittleEndian.PutUint64(compute[0].Data[1:], 11)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policyCopy, requestCopy, quoteCopy := policy, request, quote
			computeCopy := []solana.Instruction{{
				Program: compute[0].Program, Data: append([]byte(nil), compute[0].Data...),
			}}
			mutate(&policyCopy, &requestCopy, &quoteCopy, computeCopy)
			if _, err := ValidateProposal(
				policyCopy, requestCopy, quoteCopy, computeCopy, instructions,
			); err == nil {
				t.Fatal("proposal outside operator limits was accepted")
			}
		})
	}
}

func TestValidateProposalAppliesOperatorLimitsToTokenInput(t *testing.T) {
	request, quote, instructions := exactInTokenToSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 10, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	price := make([]byte, 9)
	price[0] = 3
	binary.LittleEndian.PutUint64(price[1:], 10)
	compute := []solana.Instruction{{Program: solana.ComputeBudgetProgram, Data: price}}
	if _, err := ValidateProposal(policy, request, quote, compute, instructions); err != nil {
		t.Fatal(err)
	}

	redirected := request
	redirected.DestinationTokenAccount = request.Taker
	if _, err := ValidateProposal(policy, redirected, quote, compute, instructions); err == nil {
		t.Fatal("token-input proposal redirected native output")
	}
	overCap := request
	overCap.InputAmount++
	if _, err := ValidateProposal(policy, overCap, quote, compute, instructions); err == nil {
		t.Fatal("token-input proposal exceeded operator cap")
	}
}

func TestPolicyRejectsInvalidLimits(t *testing.T) {
	request, quote, _ := exactInSOLFixture(t)
	base := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 10, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000,
	}
	for name, mutate := range map[string]func(*Policy){
		"zero input":  func(value *Policy) { value.MaxInputAmount = 0 },
		"zero output": func(value *Policy) { value.MinOutputAmount = 0 },
		"token-to-token route": func(value *Policy) {
			value.InputMint = value.OutputMint
			value.OutputMint = solana.Encode(make([]byte, 32))
		},
		"high slippage": func(value *Policy) { value.MaxSlippageBPS = 501 },
		"high compute":  func(value *Policy) { value.MaxComputeUnits = solana.MaxComputeUnitLimit + 1 },
		"zero price":    func(value *Policy) { value.MaxComputeUnitPriceMicroLamport = 0 },
		"zero fee":      func(value *Policy) { value.MaxFeeLamports = 0 },
		"zero rent":     func(value *Policy) { value.MaxTokenAccountRentLamports = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := base
			mutate(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("invalid Jupiter policy was accepted")
			}
		})
	}
}

func TestPolicyFingerprintAndActionIDBindThePolicy(t *testing.T) {
	request, quote, _ := exactInSOLFixture(t)
	policy := Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: 100_000,
		MaxComputeUnitPriceMicroLamport: 10, MaxFeeLamports: 10_000,
		MaxTokenAccountRentLamports: 3_000_000, RouteGuard: routeGuardFixture(),
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	actionID, err := ComputeActionID(fingerprint, 1_728_000_000)
	if err != nil {
		t.Fatal(err)
	}
	changed := policy
	changed.MaxInputAmount++
	changedFingerprint, err := changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == changedFingerprint || actionID == "" {
		t.Fatal("Jupiter policy identity did not bind its limits")
	}
	changedDeployment := deploymentIdentity{
		Program:          Program,
		ProgramData:      ProgramData,
		UpgradeAuthority: UpgradeAuthority,
		DeploymentSlot:   DeploymentSlot,
	}
	changedDeployment.DeploymentSlot++
	changedDeploymentFingerprint, err := policy.fingerprint(changedDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == changedDeploymentFingerprint {
		t.Fatal("Jupiter policy identity did not bind its deployment")
	}
	changedGuard := policy
	changedGuard.RouteGuard.CodeSHA256 = strings.Repeat("0", 64)
	changedGuardFingerprint, err := changedGuard.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == changedGuardFingerprint {
		t.Fatal("Jupiter policy identity did not bind the exact guard code")
	}
}
