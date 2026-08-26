package jupiterswap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

func TestValidateRouteV2PinsTheSecurityRelevantPrefix(t *testing.T) {
	request, quote, instruction := routeV2Fixture()
	intent, err := ValidateRouteV2(request, quote, instruction)
	if err != nil {
		t.Fatal(err)
	}
	if intent.SourceTokenAccount != instruction.Accounts[1].Address ||
		intent.DestinationTokenAccount != instruction.Accounts[2].Address ||
		intent.InputAmount != request.InputAmount || intent.QuotedOutput != quote.EstimatedOutput ||
		intent.MinimumOutput != quote.MinimumOutput || intent.SlippageBPS != request.SlippageBPS {
		t.Fatalf("intent = %+v", intent)
	}
}

func TestValidateRouteV2PinsTheExplicitDestination(t *testing.T) {
	request, quote, instruction := routeV2Fixture()
	request.DestinationTokenAccount = instruction.Accounts[2].Address
	instruction.Accounts[7] = solana.AccountMeta{
		Address: request.DestinationTokenAccount, Writable: true,
	}
	if _, err := ValidateRouteV2(request, quote, instruction); err != nil {
		t.Fatal(err)
	}
	instruction.Accounts[7] = solana.AccountMeta{Address: Program}
	if _, err := ValidateRouteV2(request, quote, instruction); err == nil {
		t.Fatal("explicit Jupiter destination was replaced by the optional-account sentinel")
	}
}

func TestValidateRouteV2AllowsANonSignerTakerInRemainingAccounts(t *testing.T) {
	request, quote, instruction := routeV2Fixture()
	// Solana privileges are transaction-wide and the taker is already the one
	// required signer at index zero. Current Jupiter routes may repeat that same
	// address as a non-signer remaining account for a downstream CPI; the final
	// message validator still enforces exactly one signer.
	instruction.Accounts = append(instruction.Accounts, solana.AccountMeta{Address: request.Taker})
	if _, err := ValidateRouteV2(request, quote, instruction); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRouteV2RejectsAuthorityAccountAndAmountMutations(t *testing.T) {
	mutations := map[string]func(*jupiterquote.Request, *jupiterquote.Result, *solana.Instruction){
		"program": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Program = tokenProgram
		},
		"taker": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Accounts[0].Address = instruction.Accounts[1].Address
		},
		"input mint": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Accounts[3].Address = instruction.Accounts[4].Address
		},
		"token program": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Accounts[5].Address = Program
		},
		"event authority": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Accounts[8].Address = Program
		},
		"remaining signer": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Accounts[10].Signer = true
		},
		"discriminator": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			instruction.Data[0] ^= 1
		},
		"input amount": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			binary.LittleEndian.PutUint64(instruction.Data[8:16], 1)
		},
		"quoted output": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			binary.LittleEndian.PutUint64(instruction.Data[16:24], 1)
		},
		"slippage": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			binary.LittleEndian.PutUint16(instruction.Data[24:26], 500)
		},
		"platform fee": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			binary.LittleEndian.PutUint16(instruction.Data[26:28], 1)
		},
		"positive slippage": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			binary.LittleEndian.PutUint16(instruction.Data[28:30], 1)
		},
		"empty route": func(_ *jupiterquote.Request, _ *jupiterquote.Result, instruction *solana.Instruction) {
			binary.LittleEndian.PutUint32(instruction.Data[30:34], 0)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			request, quote, instruction := routeV2Fixture()
			mutate(&request, &quote, &instruction)
			if _, err := ValidateRouteV2(request, quote, instruction); err == nil {
				t.Fatal("mutated Jupiter route_v2 was accepted")
			}
		})
	}
}

func TestValidateSharedAccountsRouteV2PinsAuthorityAccountsAndAmounts(t *testing.T) {
	request, quote, instruction := sharedAccountsRouteV2Fixture(t)
	intent, err := ValidateRouteV2(request, quote, instruction)
	if err != nil {
		t.Fatal(err)
	}
	if intent.SourceTokenAccount != instruction.Accounts[2].Address ||
		intent.DestinationTokenAccount != instruction.Accounts[5].Address ||
		intent.InputAmount != request.InputAmount || intent.QuotedOutput != quote.EstimatedOutput ||
		intent.MinimumOutput != quote.MinimumOutput || intent.SlippageBPS != request.SlippageBPS {
		t.Fatalf("intent = %+v", intent)
	}

	mutations := map[string]func(*solana.Instruction){
		"authority id": func(value *solana.Instruction) { value.Data[8] = authorityCount },
		"authority": func(value *solana.Instruction) {
			value.Accounts[0].Address = value.Accounts[1].Address
		},
		"taker": func(value *solana.Instruction) {
			value.Accounts[1].Address = value.Accounts[0].Address
		},
		"program source": func(value *solana.Instruction) {
			value.Accounts[3].Address = value.Accounts[2].Address
		},
		"program destination": func(value *solana.Instruction) {
			value.Accounts[4].Address = value.Accounts[5].Address
		},
		"input mint": func(value *solana.Instruction) {
			value.Accounts[6].Address = value.Accounts[7].Address
		},
		"token program": func(value *solana.Instruction) {
			value.Accounts[8].Address = Program
		},
		"event authority": func(value *solana.Instruction) {
			value.Accounts[10].Address = Program
		},
		"remaining signer": func(value *solana.Instruction) { value.Accounts[12].Signer = true },
		"input amount": func(value *solana.Instruction) {
			binary.LittleEndian.PutUint64(value.Data[9:17], 1)
		},
		"quoted output": func(value *solana.Instruction) {
			binary.LittleEndian.PutUint64(value.Data[17:25], 1)
		},
		"slippage": func(value *solana.Instruction) {
			binary.LittleEndian.PutUint16(value.Data[25:27], 500)
		},
		"platform fee": func(value *solana.Instruction) {
			binary.LittleEndian.PutUint16(value.Data[27:29], 1)
		},
		"positive slippage": func(value *solana.Instruction) {
			binary.LittleEndian.PutUint16(value.Data[29:31], 1)
		},
		"empty route": func(value *solana.Instruction) {
			binary.LittleEndian.PutUint32(value.Data[31:35], 0)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			_, _, instruction := sharedAccountsRouteV2Fixture(t)
			mutate(&instruction)
			if _, err := ValidateRouteV2(request, quote, instruction); err == nil {
				t.Fatal("mutated Jupiter shared route_v2 was accepted")
			}
		})
	}
}

// TestLivePinnedJupiterDeployment is an explicit, read-only drift check for
// the deployment identity that proposal validation pins. Ordinary tests stay
// offline; no wallet, API key, signature, or transaction is involved.
func TestLivePinnedJupiterDeployment(t *testing.T) {
	endpoint := os.Getenv("MITHRIL_AGENT_LIVE_MAINNET_RPC_URL")
	if os.Getenv("MITHRIL_AGENT_LIVE_JUPITER_TEST") != "1" || endpoint == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_JUPITER_TEST=1 and MITHRIL_AGENT_LIVE_MAINNET_RPC_URL")
	}
	client, err := solanarpc.New(endpoint, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := client.FinalizedSlot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	program, err := client.AccountSlice(t.Context(), Program, slot, 0, 36)
	if err != nil {
		t.Fatal(err)
	}
	programData, err := client.AccountSlice(t.Context(), ProgramData, slot, 0, 45)
	if err != nil {
		t.Fatal(err)
	}
	if program.Owner != orcaswap.UpgradeableLoader || !program.Executable ||
		program.DataLength != 36 || binary.LittleEndian.Uint32(program.Data[:4]) != 2 ||
		solana.Encode(program.Data[4:]) != ProgramData {
		t.Fatal("live Jupiter program account no longer matches the pinned deployment")
	}
	if programData.Owner != orcaswap.UpgradeableLoader || programData.Executable ||
		programData.DataLength < 45 || binary.LittleEndian.Uint32(programData.Data[:4]) != 3 ||
		binary.LittleEndian.Uint64(programData.Data[4:12]) != DeploymentSlot ||
		programData.Data[12] != 1 || solana.Encode(programData.Data[13:45]) != UpgradeAuthority {
		t.Fatal("live Jupiter program-data account no longer matches the pinned deployment")
	}
}

// TestLiveCurrentJupiterRouteShape catches an API/program upgrade that keeps
// the same program account but changes either route_v2 contract this validator
// understands. It reads one proposal and never signs or submits it.
func TestLiveCurrentJupiterRouteShape(t *testing.T) {
	taker := os.Getenv("MITHRIL_AGENT_LIVE_JUPITER_TAKER")
	if os.Getenv("MITHRIL_AGENT_LIVE_JUPITER_TEST") != "1" || taker == "" {
		t.Skip("set MITHRIL_AGENT_LIVE_JUPITER_TEST=1 and MITHRIL_AGENT_LIVE_JUPITER_TAKER")
	}
	client, err := jupiterquote.New(os.Getenv("MITHRIL_AGENT_JUPITER_API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	outputMint := "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	destinationAccount, err := orcaswap.AssociatedTokenAddress(taker, outputMint)
	if err != nil {
		t.Fatal(err)
	}
	requests := []jupiterquote.Request{
		{
			Taker: taker, InputMint: orcaswap.WrappedSOLMint,
			OutputMint: outputMint, DestinationTokenAccount: destinationAccount,
			InputAmount: 1_000_000, SlippageBPS: 50,
		},
		{
			Taker: taker, InputMint: outputMint, OutputMint: orcaswap.WrappedSOLMint,
			InputAmount: 1_000_000, SlippageBPS: 50,
		},
	}
	for index, request := range requests {
		if index > 0 {
			select {
			case <-t.Context().Done():
				t.Fatal("current Jupiter route check was canceled")
			case <-time.After(2100 * time.Millisecond):
			}
		}
		name := "token-input"
		if request.InputMint == orcaswap.WrappedSOLMint {
			name = "native-input"
		}
		t.Run(name, func(t *testing.T) {
			checkLiveCurrentJupiterRouteShape(t, client, request)
		})
	}
}

func checkLiveCurrentJupiterRouteShape(
	t *testing.T,
	client *jupiterquote.Client,
	request jupiterquote.Request,
) {
	t.Helper()
	for attempt := range 3 {
		result, err := client.Build(t.Context(), request)
		if err != nil {
			if errors.Is(err, jupiterquote.ErrTemporarilyUnavailable) && attempt < 2 {
				select {
				case <-t.Context().Done():
					t.Fatal("current Jupiter route check was canceled")
				case <-time.After(2100 * time.Millisecond):
				}
				continue
			}
			t.Fatal(err)
		}
		if request.OutputMint == orcaswap.WrappedSOLMint {
			policy := Policy{
				Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
				MaxInputAmount: request.InputAmount, MinOutputAmount: result.Quote.MinimumOutput,
				MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: solana.MaxComputeUnitLimit,
				MaxComputeUnitPriceMicroLamport: ^uint64(0), MaxFeeLamports: ^uint64(0),
				MaxTokenAccountRentLamports: 10_000_000,
			}
			if _, err := ValidateProposal(
				policy, request, result.Quote, result.ComputeBudget, result.Instructions,
			); err != nil {
				if attempt == 2 {
					t.Fatal(err)
				}
				select {
				case <-t.Context().Done():
					t.Fatal("current Jupiter route check was canceled")
				case <-time.After(2100 * time.Millisecond):
				}
				continue
			}
		}
		found := 0
		for _, instruction := range result.Instructions {
			if instruction.Program != Program {
				continue
			}
			found++
			if _, err := ValidateRouteV2(request, result.Quote, instruction); err != nil {
				t.Fatal(err)
			}
		}
		if found == 1 {
			limit, err := solana.SetComputeUnitLimitInstruction(solana.MaxComputeUnitLimit)
			if err != nil {
				t.Fatal(err)
			}
			instructions := make(
				[]solana.Instruction, 1, 1+len(result.ComputeBudget)+len(result.Instructions),
			)
			instructions[0] = limit
			instructions = append(instructions, result.ComputeBudget...)
			instructions = append(instructions, result.Instructions...)
			message, err := BuildPolicyV0Message(
				request.Taker, solana.Encode(result.RecentBlockhash[:]), instructions,
				result.ClaimedAddressTables,
			)
			if err != nil {
				t.Fatalf("current Jupiter proposal cannot keep hosted-policy accounts static: %v", err)
			}
			decoded, err := solana.DecodeV0Message(message, result.ClaimedAddressTables)
			if err != nil {
				t.Fatal(err)
			}
			static := make(map[[32]byte]struct{}, len(decoded.StaticAccountKeys))
			for _, key := range decoded.StaticAccountKeys {
				static[key] = struct{}{}
			}
			policyAccounts, _ := policyStaticAccounts(Program, instructions)
			for _, address := range policyAccounts {
				key, _ := solana.Decode32(address)
				if _, ok := static[key]; !ok {
					t.Fatal("current Jupiter proposal hides a hosted-policy account in a lookup table")
				}
			}
			return
		}
		if found > 1 {
			t.Fatalf("current Jupiter proposal contains %d route_v2 instructions", found)
		}
		if attempt < 2 {
			select {
			case <-t.Context().Done():
				t.Fatal("current Jupiter route check was canceled")
			case <-time.After(2100 * time.Millisecond):
			}
		}
	}
	t.Fatal("current Jupiter proposals did not include a supported route_v2 contract")
}

func routeV2Fixture() (jupiterquote.Request, jupiterquote.Result, solana.Instruction) {
	addresses := make([]string, 11)
	for index := range addresses {
		addresses[index] = solana.Encode(bytes.Repeat([]byte{byte(index + 1)}, 32))
	}
	request := jupiterquote.Request{
		Taker: addresses[0], InputMint: addresses[3], OutputMint: addresses[4],
		InputAmount: 1_000_000, SlippageBPS: 50,
	}
	quote := jupiterquote.Result{
		InputAmount: 1_000_000, EstimatedOutput: 75_890, MinimumOutput: 75_511,
	}
	data := append([]byte(nil), routeV2Discriminator[:]...)
	data = binary.LittleEndian.AppendUint64(data, request.InputAmount)
	data = binary.LittleEndian.AppendUint64(data, quote.EstimatedOutput)
	data = binary.LittleEndian.AppendUint16(data, request.SlippageBPS)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint16(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = append(data, 17, 1, 0x10, 0x27, 0, 1)
	return request, quote, solana.Instruction{
		Program: Program,
		Accounts: []solana.AccountMeta{
			{Address: request.Taker, Signer: true},
			{Address: addresses[1], Writable: true},
			{Address: addresses[2], Writable: true},
			{Address: request.InputMint},
			{Address: request.OutputMint},
			{Address: tokenProgram},
			{Address: tokenProgram},
			{Address: Program},
			{Address: eventAuthority},
			{Address: Program},
			{Address: addresses[10], Writable: true},
		},
		Data: data,
	}
}

func sharedAccountsRouteV2Fixture(
	t *testing.T,
) (jupiterquote.Request, jupiterquote.Result, solana.Instruction) {
	t.Helper()
	taker := solana.Encode(bytes.Repeat([]byte{1}, 32))
	outputMint := solana.Encode(bytes.Repeat([]byte{2}, 32))
	request := jupiterquote.Request{
		Taker: taker, InputMint: orcaswap.WrappedSOLMint, OutputMint: outputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	}
	quote := jupiterquote.Result{
		InputAmount: request.InputAmount, EstimatedOutput: 75_890, MinimumOutput: 75_511,
	}
	inputAccount, err := orcaswap.AssociatedTokenAddress(taker, request.InputMint)
	if err != nil {
		t.Fatal(err)
	}
	outputAccount, err := orcaswap.AssociatedTokenAddress(taker, request.OutputMint)
	if err != nil {
		t.Fatal(err)
	}
	return request, quote, planSharedAccountsRouteV2Fixture(
		t, request, quote, inputAccount, outputAccount,
	)
}
