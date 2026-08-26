package turnkeycustody

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	liveJupiterQualificationEnabled = "MITHRIL_AGENT_TURNKEY_JUPITER_QUALIFY"
	liveJupiterCandidateFile        = "MITHRIL_AGENT_TURNKEY_JUPITER_CANDIDATE_FILE"
	liveJupiterPolicyFile           = "MITHRIL_AGENT_TURNKEY_JUPITER_POLICY_FILE"
	maxJupiterCandidateBytes        = 512 << 10
	maxJupiterPolicyBytes           = 64 << 10
)

// TestLiveCurrentJupiterQualificationPolicyFits checks that today's keyless
// Jupiter proposal can still be represented by the retained-candidate Turnkey
// policy. It does not verify provider evidence, sign, or submit anything.
func TestLiveCurrentJupiterQualificationPolicyFits(t *testing.T) {
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
	checks := []struct {
		name    string
		request jupiterquote.Request
	}{
		{"native-input", jupiterquote.Request{
			Taker: taker, InputMint: orcaswap.WrappedSOLMint,
			OutputMint: outputMint, DestinationTokenAccount: destinationAccount,
			InputAmount: 1_000_000, SlippageBPS: 50,
		}},
		{"token-input", jupiterquote.Request{
			Taker: taker, InputMint: outputMint, OutputMint: orcaswap.WrappedSOLMint,
			InputAmount: 1_000_000, SlippageBPS: 50,
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			checkLiveCurrentJupiterQualificationPolicyFits(t, client, check.request)
		})
	}
}

func checkLiveCurrentJupiterQualificationPolicyFits(
	t *testing.T,
	client *jupiterquote.Client,
	request jupiterquote.Request,
) {
	t.Helper()
	var result jupiterquote.BuildResult
	var proposalErr error
	var err error
	for attempt := range 3 {
		result, err = client.Build(t.Context(), request)
		if err != nil {
			if errors.Is(err, jupiterquote.ErrTemporarilyUnavailable) && attempt < 2 {
				select {
				case <-t.Context().Done():
					t.Fatal("current Jupiter qualification check was canceled")
				case <-time.After(2100 * time.Millisecond):
				}
				continue
			}
			t.Fatal(err)
		}
		result.Instructions, err = jupiterswap.RemoveRedundantOutputAccountSetup(
			request, result.Instructions,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, proposalErr = jupiterswap.ValidateProposal(
			jupiterswap.Policy{
				Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
				MaxInputAmount: request.InputAmount, MinOutputAmount: result.Quote.MinimumOutput,
				MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: solana.MaxComputeUnitLimit,
				MaxComputeUnitPriceMicroLamport: ^uint64(0),
				MaxFeeLamports:                  1, MaxTokenAccountRentLamports: 1,
			},
			request, result.Quote, result.ComputeBudget, result.Instructions,
		)
		if proposalErr == nil {
			break
		}
		if attempt < 2 {
			select {
			case <-t.Context().Done():
				t.Fatal("current Jupiter qualification check was canceled")
			case <-time.After(2100 * time.Millisecond):
			}
		}
	}
	if proposalErr != nil {
		t.Fatal(proposalErr)
	}
	if len(result.ComputeBudget) != 1 || len(result.ComputeBudget[0].Data) != 9 ||
		result.ComputeBudget[0].Data[0] != 3 {
		t.Fatal("current Jupiter proposal has an unsupported compute-price instruction")
	}
	price := binary.LittleEndian.Uint64(result.ComputeBudget[0].Data[1:])
	policy := jupiterswap.Policy{
		Owner: request.Taker, InputMint: request.InputMint, OutputMint: request.OutputMint,
		MaxInputAmount: request.InputAmount, MinOutputAmount: result.Quote.MinimumOutput,
		MaxSlippageBPS: request.SlippageBPS, MaxComputeUnits: solana.MaxComputeUnitLimit,
		MaxComputeUnitPriceMicroLamport: price,
		MaxFeeLamports:                  1_000_000, MaxTokenAccountRentLamports: 10_000_000,
		RouteGuard: turnkeyRouteGuard(),
	}
	if _, err := jupiterswap.ValidateProposal(
		policy, request, result.Quote, result.ComputeBudget, result.Instructions,
	); err != nil {
		t.Fatal(err)
	}
	limit, err := solana.SetComputeUnitLimitInstruction(solana.MaxComputeUnitLimit)
	if err != nil {
		t.Fatal(err)
	}
	instructions := make([]solana.Instruction, 1, 1+len(result.ComputeBudget)+len(result.Instructions))
	instructions[0] = limit
	instructions = append(instructions, result.ComputeBudget...)
	instructions = append(instructions, result.Instructions...)
	message, err := jupiterswap.BuildGuardedPolicyV0Message(
		policy, request.Taker, solana.Encode(result.RecentBlockhash[:]), instructions,
		result.ClaimedAddressTables,
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := solana.DecodeV0Message(message, result.ClaimedAddressTables)
	if err != nil {
		t.Fatal(err)
	}
	usedTables, err := jupiterswap.UsedAddressTables(message, result.ClaimedAddressTables)
	if err != nil {
		t.Fatal(err)
	}
	if len(usedTables) != len(decoded.AddressTableLookups) {
		t.Fatal("current Jupiter proposal retained incomplete lookup-table policy evidence")
	}
	evidence, err := jupiterswap.EncodeAddressTables(usedTables)
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposalcheck.Candidate{
		Version: proposalcheck.CandidateVersion, Policy: policy, Request: request,
		Quote: result.Quote, MessageBase64: base64.StdEncoding.EncodeToString(message),
		AddressTables: evidence, LastValidBlockHeight: result.LastValidBlockHeight,
	}
	document, err := BuildJupiterQualificationPolicy(policy, candidate, "live-compatibility-user")
	if err != nil {
		t.Fatal(err)
	}
	if document.Condition == "" || len(document.Condition) > maxQualificationConditionBytes {
		t.Fatal("current Jupiter qualification policy does not fit")
	}
}

// TestLiveTurnkeyJupiterPolicyQualification proves a separately installed
// Turnkey policy against one retained, already checked Jupiter candidate. It
// has no RPC or submitter and cannot broadcast the signed transaction.
func TestLiveTurnkeyJupiterPolicyQualification(t *testing.T) {
	if os.Getenv(liveJupiterQualificationEnabled) != "1" {
		t.Skip("set MITHRIL_AGENT_TURNKEY_JUPITER_QUALIFY=1 for retained Jupiter qualification")
	}
	candidateData, err := securefile.ReadPrivate(
		os.Getenv(liveJupiterCandidateFile), maxJupiterCandidateBytes,
	)
	if err != nil {
		t.Fatal("read protected Jupiter candidate")
	}
	defer clear(candidateData)
	candidate, err := proposalcheck.DecodeCandidate(candidateData)
	if err != nil {
		t.Fatal("decode protected Jupiter candidate")
	}
	policyData, err := securefile.ReadPrivate(os.Getenv(liveJupiterPolicyFile), maxJupiterPolicyBytes)
	if err != nil {
		t.Fatal("read protected Jupiter policy")
	}
	defer clear(policyData)
	var policy jupiterswap.Policy
	if err := strictjson.Decode(policyData, &policy); err != nil {
		t.Fatal("decode protected Jupiter policy")
	}
	message, tables, err := proposalcheck.ValidateCandidateMaterial(policy, candidate)
	if err != nil {
		t.Fatal("retained Jupiter candidate does not match protected policy")
	}
	signWith := os.Getenv(liveSignWith)
	stamper := verifiedLiveAPIKeyStamper(t)
	verifyLiveSigningAddress(t, stamper, os.Getenv(liveOrganizationID), signWith, policy.Owner)
	custody, err := newWithStamper(stamper, Config{
		OrganizationID: os.Getenv(liveOrganizationID),
		SignWith:       signWith,
	})
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := solana.BuildUnsignedV0Transaction(message, tables)
	if err != nil {
		t.Fatal("build retained Jupiter transaction")
	}
	request := liveCustodyRequest(unsigned, time.Now().UTC())
	signed, err := custody.Sign(t.Context(), request)
	if err != nil {
		t.Fatalf("exact retained Jupiter transaction was refused: %v", err)
	}
	decoded, err := solana.DecodeSignedV0Transaction(signed, tables)
	if err != nil || !bytes.Equal(decoded.Message.Raw, message) {
		t.Fatal("Turnkey changed or incorrectly signed the retained Jupiter transaction")
	}
	retry, err := custody.Sign(t.Context(), request)
	if err != nil || !bytes.Equal(retry, signed) {
		t.Fatal("exact retained Jupiter activity retry was not idempotent")
	}

	mutations, err := retainedJupiterMutations(message, tables, policy)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range mutations {
		t.Run("reject_"+name+"_mutation", func(t *testing.T) {
			transaction, err := solana.BuildUnsignedV0Transaction(mutation.message, mutation.tables)
			if err != nil {
				t.Fatalf("build %s mutation: %v", name, err)
			}
			if err := verifyLivePolicyRejection(
				t.Context(), custody, transaction, request, signed, time.Now().UTC(),
			); err != nil {
				t.Fatalf("Turnkey %s mutation check failed: %v", name, err)
			}
		})
	}
}

type retainedMutation struct {
	message []byte
	tables  map[[32]byte][][32]byte
}

type retainedInstructionLayout struct {
	start, end, accountsStart, accountsEnd, dataStart, dataEnd int
	programIndex                                               uint8
}

type retainedLookupLayout struct {
	tableOffset int
}

type retainedMessageLayout struct {
	staticStart                                    int
	instructionCountOffset, instructionCountLength int
	instructions                                   []retainedInstructionLayout
	lookups                                        []retainedLookupLayout
}

type retainedRouteFields struct {
	inputAmount, quotedOutput, slippage, platformFee int
	inputMintAccount, outputMintAccount              int
}

func retainedJupiterMutations(
	message []byte,
	tables map[[32]byte][][32]byte,
	policy jupiterswap.Policy,
) (map[string]retainedMutation, error) {
	decoded, err := solana.DecodeV0Message(message, tables)
	if err != nil {
		return nil, errors.New("decode retained Jupiter message")
	}
	layout, err := parseRetainedMessageLayout(message)
	if err != nil || len(layout.instructions) != len(decoded.Instructions) || len(layout.lookups) == 0 {
		return nil, errors.New("retained Jupiter message layout is not qualification-ready")
	}
	mutations := make(map[string]retainedMutation, 12+len(layout.lookups))
	add := func(name string, changed []byte, changedTables map[[32]byte][][32]byte) error {
		if bytes.Equal(message, changed) {
			return fmt.Errorf("%s mutation did not change the Jupiter message", name)
		}
		if _, err := solana.DecodeV0Message(changed, changedTables); err != nil {
			return fmt.Errorf("%s mutation is not a valid version-zero message", name)
		}
		mutations[name] = retainedMutation{changed, changedTables}
		return nil
	}
	flip := func(name string, offset int) error {
		changed := bytes.Clone(message)
		changed[offset] ^= 1
		return add(name, changed, tables)
	}
	var route *retainedInstructionLayout
	var routeInstruction *solana.CompiledInstruction
	var computeLimit *retainedInstructionLayout
	var computePrice *retainedInstructionLayout
	for index := range decoded.Instructions {
		instruction := &decoded.Instructions[index]
		program := solana.Encode(decoded.AccountKeys[instruction.ProgramIndex][:])
		switch program {
		case policy.RouteGuard.Program:
			route = &layout.instructions[index]
			routeInstruction = instruction
		case solana.ComputeBudgetProgram:
			current := &layout.instructions[index]
			switch {
			case len(instruction.Data) == 5 && instruction.Data[0] == 2:
				computeLimit = current
			case len(instruction.Data) == 9 && instruction.Data[0] == 3:
				computePrice = current
			}
		}
	}
	fields, ok := retainedJupiterRouteFields(routeInstruction)
	fields.inputMintAccount++
	fields.outputMintAccount++
	if route == nil || !ok || computeLimit == nil || computePrice == nil ||
		route.dataEnd-route.dataStart <= fields.platformFee+1 ||
		route.accountsEnd-route.accountsStart <= fields.outputMintAccount {
		return nil, errors.New("retained Jupiter route or compute instruction is not qualification-ready")
	}
	programIndex := int(route.programIndex)
	if programIndex >= len(decoded.StaticAccountKeys) {
		return nil, errors.New("retained Jupiter program is not a static account")
	}
	if err := flip("program", layout.staticStart+programIndex*32); err != nil {
		return nil, err
	}
	changedAccount := bytes.Clone(message)
	changedAccount[route.accountsStart], changedAccount[route.accountsStart+1] =
		changedAccount[route.accountsStart+1], changedAccount[route.accountsStart]
	if err := add("account", changedAccount, tables); err != nil {
		return nil, err
	}
	changedOutputMint := bytes.Clone(message)
	changedOutputMint[route.accountsStart+fields.outputMintAccount] =
		changedOutputMint[route.accountsStart+fields.inputMintAccount]
	if err := add("output_mint", changedOutputMint, tables); err != nil {
		return nil, err
	}
	changedInstructionType := bytes.Clone(message)
	changedInstructionType[route.dataStart] ^= 1
	if err := add("instruction_type", changedInstructionType, tables); err != nil {
		return nil, err
	}
	changedInput := bytes.Clone(message)
	binary.LittleEndian.PutUint64(
		changedInput[route.dataStart+fields.inputAmount:], policy.MaxInputAmount+1,
	)
	if err := add("input_cap", changedInput, tables); err != nil {
		return nil, err
	}
	changedOutput := bytes.Clone(message)
	binary.LittleEndian.PutUint64(changedOutput[route.dataStart+fields.quotedOutput:], 0)
	if err := add("zero_output", changedOutput, tables); err != nil {
		return nil, err
	}
	changedSlippage := bytes.Clone(message)
	binary.LittleEndian.PutUint16(
		changedSlippage[route.dataStart+fields.slippage:], policy.MaxSlippageBPS+1,
	)
	if err := add("slippage_cap", changedSlippage, tables); err != nil {
		return nil, err
	}
	changedPlatformFee := bytes.Clone(message)
	binary.LittleEndian.PutUint16(changedPlatformFee[route.dataStart+fields.platformFee:], 1)
	if err := add("platform_fee", changedPlatformFee, tables); err != nil {
		return nil, err
	}
	changedComputeLimit := bytes.Clone(message)
	binary.LittleEndian.PutUint32(
		changedComputeLimit[computeLimit.dataStart+1:], policy.MaxComputeUnits+1,
	)
	if err := add("compute_limit", changedComputeLimit, tables); err != nil {
		return nil, err
	}
	changedComputePrice := bytes.Clone(message)
	binary.LittleEndian.PutUint64(
		changedComputePrice[computePrice.dataStart+1:], policy.MaxComputeUnitPriceMicroLamport+1,
	)
	if err := add("compute_price", changedComputePrice, tables); err != nil {
		return nil, err
	}

	for index, lookup := range layout.lookups {
		originalKey := decoded.AddressTableLookups[index].AccountKey
		changedKey := originalKey
		changedKey[0] ^= 1
		changed := bytes.Clone(message)
		copy(changed[lookup.tableOffset:lookup.tableOffset+32], changedKey[:])
		changedTables := cloneAddressTables(tables)
		contents := changedTables[originalKey]
		delete(changedTables, originalKey)
		changedTables[changedKey] = contents
		if err := add(fmt.Sprintf("lookup_table_%d", index), changed, changedTables); err != nil {
			return nil, err
		}

	}

	if layout.instructionCountLength != 1 || len(layout.instructions) >= 64 {
		return nil, errors.New("retained Jupiter instruction count cannot be safely mutated")
	}
	changed := make([]byte, 0, len(message)+(route.end-route.start))
	changed = append(changed, message[:layout.instructionCountOffset]...)
	changed = append(changed, byte(len(layout.instructions)+1))
	changed = append(changed, message[layout.instructionCountOffset+1:route.end]...)
	changed = append(changed, message[route.start:route.end]...)
	changed = append(changed, message[route.end:]...)
	if err := add("extra_instruction", changed, tables); err != nil {
		return nil, err
	}
	return mutations, nil
}

func retainedJupiterRouteFields(instruction *solana.CompiledInstruction) (retainedRouteFields, bool) {
	if instruction == nil || len(instruction.Data) < 8 {
		return retainedRouteFields{}, false
	}
	switch {
	case bytes.Equal(instruction.Data[:8], []byte{187, 100, 250, 204, 49, 196, 175, 20}):
		return retainedRouteFields{
			inputAmount: 8, quotedOutput: 16, slippage: 24, platformFee: 26,
			inputMintAccount: 3, outputMintAccount: 4,
		}, true
	case bytes.Equal(instruction.Data[:8], []byte{209, 152, 83, 147, 124, 254, 216, 233}):
		return retainedRouteFields{
			inputAmount: 9, quotedOutput: 17, slippage: 25, platformFee: 27,
			inputMintAccount: 6, outputMintAccount: 7,
		}, true
	default:
		return retainedRouteFields{}, false
	}
}

func parseRetainedMessageLayout(message []byte) (retainedMessageLayout, error) {
	var layout retainedMessageLayout
	if len(message) < 4 || message[0] != 0x80 {
		return layout, errors.New("message is not version zero")
	}
	offset := 4
	staticCount, _, err := readRetainedShortVec(message, &offset)
	if err != nil || staticCount == 0 || staticCount > 256 {
		return layout, errors.New("static account layout is invalid")
	}
	layout.staticStart = offset
	offset += staticCount * 32
	offset += 32
	if offset > len(message) {
		return layout, errors.New("message header is truncated")
	}
	layout.instructionCountOffset = offset
	instructionCount, encodedLength, err := readRetainedShortVec(message, &offset)
	if err != nil || instructionCount == 0 || instructionCount > 64 {
		return layout, errors.New("instruction layout is invalid")
	}
	layout.instructionCountLength = encodedLength
	for range instructionCount {
		instruction := retainedInstructionLayout{start: offset}
		if offset >= len(message) {
			return layout, errors.New("instruction program is truncated")
		}
		instruction.programIndex = message[offset]
		offset++
		accountCount, _, err := readRetainedShortVec(message, &offset)
		if err != nil || accountCount > 255 || offset+accountCount > len(message) {
			return layout, errors.New("instruction accounts are truncated")
		}
		instruction.accountsStart, instruction.accountsEnd = offset, offset+accountCount
		offset += accountCount
		dataLength, _, err := readRetainedShortVec(message, &offset)
		if err != nil || offset+dataLength > len(message) {
			return layout, errors.New("instruction data is truncated")
		}
		instruction.dataStart, instruction.dataEnd = offset, offset+dataLength
		offset += dataLength
		instruction.end = offset
		layout.instructions = append(layout.instructions, instruction)
	}
	lookupCount, _, err := readRetainedShortVec(message, &offset)
	if err != nil || lookupCount > 64 {
		return layout, errors.New("lookup layout is invalid")
	}
	for range lookupCount {
		lookup := retainedLookupLayout{tableOffset: offset}
		offset += 32
		writableCount, _, err := readRetainedShortVec(message, &offset)
		if err != nil || offset+writableCount > len(message) {
			return layout, errors.New("writable lookup is truncated")
		}
		offset += writableCount
		readonlyCount, _, err := readRetainedShortVec(message, &offset)
		if err != nil || offset+readonlyCount > len(message) {
			return layout, errors.New("readonly lookup is truncated")
		}
		offset += readonlyCount
		layout.lookups = append(layout.lookups, lookup)
	}
	if offset != len(message) {
		return layout, errors.New("message layout has trailing bytes")
	}
	return layout, nil
}

func readRetainedShortVec(data []byte, offset *int) (int, int, error) {
	start := *offset
	value := 0
	for index := 0; index < 3; index++ {
		if *offset >= len(data) {
			return 0, 0, errors.New("short vector is truncated")
		}
		current := data[*offset]
		*offset++
		value |= int(current&0x7f) << (index * 7)
		if current&0x80 == 0 {
			return value, *offset - start, nil
		}
	}
	return 0, 0, errors.New("short vector is too long")
}

func cloneAddressTables(source map[[32]byte][][32]byte) map[[32]byte][][32]byte {
	clone := make(map[[32]byte][][32]byte, len(source))
	for table, addresses := range source {
		clone[table] = append([][32]byte(nil), addresses...)
	}
	return clone
}

func TestRetainedJupiterMutationSuiteKeepsValidVersionZeroMessages(t *testing.T) {
	source := solana.Encode(bytes.Repeat([]byte{1}, 32))
	lookupKey, _ := solana.Decode32(solana.Encode(bytes.Repeat([]byte{9}, 32)))
	table := [][32]byte{{2}, {3}, {4}, {5}, {6}, {7}, {8}, {9}, {10}, {11}}
	tables := map[[32]byte][][32]byte{lookupKey: table}
	limit, err := solana.SetComputeUnitLimitInstruction(200_000)
	if err != nil {
		t.Fatal(err)
	}
	priceData := make([]byte, 9)
	priceData[0] = 3
	binary.LittleEndian.PutUint64(priceData[1:], 10)
	routeData := bytes.Repeat([]byte{1}, 40)
	copy(routeData, []byte{187, 100, 250, 204, 49, 196, 175, 20})
	binary.LittleEndian.PutUint64(routeData[8:16], 1_000_000)
	binary.LittleEndian.PutUint64(routeData[16:24], 1)
	binary.LittleEndian.PutUint16(routeData[24:26], 50)
	binary.LittleEndian.PutUint16(routeData[26:28], 0)
	policy := jupiterswap.Policy{
		MaxInputAmount: 1_000_000, MaxSlippageBPS: 50,
		MaxComputeUnits: 200_000, MaxComputeUnitPriceMicroLamport: 10,
		RouteGuard: turnkeyRouteGuard(),
	}
	instructions, err := jupiterswap.WrapRoutePlan(
		policy.RouteGuard,
		[]solana.Instruction{
			limit,
			{Program: solana.ComputeBudgetProgram, Data: priceData},
			{
				Program: jupiterswap.Program,
				Accounts: []solana.AccountMeta{
					{Address: source, Signer: true, Writable: true},
					{Address: solana.Encode(table[0][:]), Writable: true},
					{Address: solana.Encode(table[1][:])},
					{Address: solana.Encode(table[2][:])},
					{Address: solana.Encode(table[3][:])},
					{Address: solana.Encode(table[4][:])},
					{Address: solana.Encode(table[5][:])},
					{Address: solana.Encode(table[6][:])},
					{Address: solana.Encode(table[7][:])},
					{Address: solana.Encode(table[8][:])},
				},
				Data: routeData,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	message, err := solana.BuildV0Message(
		source, solana.Encode(bytes.Repeat([]byte{5}, 32)), instructions, tables,
	)
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := retainedJupiterMutations(message, tables, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"program", "account", "output_mint", "instruction_type", "input_cap", "zero_output",
		"slippage_cap", "platform_fee", "compute_limit", "compute_price", "lookup_table_0",
		"extra_instruction",
	} {
		if _, ok := mutations[required]; !ok {
			t.Fatalf("missing %s mutation", required)
		}
	}
}

func TestRetainedJupiterRouteFieldsSupportsBothV2Shapes(t *testing.T) {
	for name, fixture := range map[string]struct {
		data   []byte
		fields retainedRouteFields
	}{
		"route": {
			data: []byte{187, 100, 250, 204, 49, 196, 175, 20},
			fields: retainedRouteFields{
				inputAmount: 8, quotedOutput: 16, slippage: 24, platformFee: 26,
				inputMintAccount: 3, outputMintAccount: 4,
			},
		},
		"shared route": {
			data: []byte{209, 152, 83, 147, 124, 254, 216, 233},
			fields: retainedRouteFields{
				inputAmount: 9, quotedOutput: 17, slippage: 25, platformFee: 27,
				inputMintAccount: 6, outputMintAccount: 7,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := retainedJupiterRouteFields(&solana.CompiledInstruction{Data: fixture.data})
			if !ok || got != fixture.fields {
				t.Fatalf("route fields = %+v, %t", got, ok)
			}
		})
	}
}
