package jupiterswap

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// MessageIntent is the value movement and compute budget independently
// recovered from a final version-0 transaction message.
type MessageIntent struct {
	Intent
	ComputeUnits                 uint32
	ComputeUnitPriceMicroLamport uint64
	RecentBlockhash              string
}

// BuildPolicyV0Message keeps every account visible to a hosted transaction
// policy when the transaction still fits Solana's packet limit. Larger routes
// fall back to keeping the fixed route policy accounts static.
func BuildPolicyV0Message(
	feePayer,
	recentBlockhash string,
	instructions []solana.Instruction,
	addressTables map[[32]byte][][32]byte,
) ([]byte, error) {
	return buildPolicyV0Message(Program, feePayer, recentBlockhash, instructions, addressTables)
}

// BuildGuardedPolicyV0Message replaces the reviewed direct Jupiter route with
// the pinned immutable guard before canonical compilation.
func BuildGuardedPolicyV0Message(
	policy Policy,
	feePayer,
	recentBlockhash string,
	instructions []solana.Instruction,
	addressTables map[[32]byte][][32]byte,
) ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	guarded, err := WrapRoutePlan(policy.RouteGuard, instructions)
	if err != nil {
		return nil, err
	}
	return buildPolicyV0Message(
		policy.RouteGuard.Program, feePayer, recentBlockhash, guarded, addressTables,
	)
}

func buildPolicyV0Message(
	routeProgram,
	feePayer,
	recentBlockhash string,
	instructions []solana.Instruction,
	addressTables map[[32]byte][][32]byte,
) ([]byte, error) {
	staticAccounts, err := policyStaticAccounts(routeProgram, instructions)
	if err != nil {
		return nil, err
	}
	allAccounts := policyAllAccounts(instructions)
	if len(allAccounts) <= 64 {
		if message, err := solana.BuildV0MessageWithStaticAccounts(
			feePayer, recentBlockhash, instructions, nil, allAccounts,
		); err == nil {
			return message, nil
		}
	}
	return solana.BuildV0MessageWithStaticAccounts(
		feePayer, recentBlockhash, instructions, addressTables, staticAccounts,
	)
}

// UsedAddressTables returns only the independently verified table contents
// actually referenced by a compiled policy message. Static-first compilation
// can make a quote provider's claimed tables unnecessary.
func UsedAddressTables(
	message []byte,
	verified map[[32]byte][][32]byte,
) (map[[32]byte][][32]byte, error) {
	decoded, err := solana.DecodeV0Message(message, verified)
	if err != nil {
		return nil, err
	}
	if len(decoded.AddressTableLookups) == 0 {
		return nil, nil
	}
	used := make(map[[32]byte][][32]byte, len(decoded.AddressTableLookups))
	for _, lookup := range decoded.AddressTableLookups {
		table, ok := verified[lookup.AccountKey]
		if !ok {
			return nil, errors.New("Jupiter policy message address table is missing")
		}
		used[lookup.AccountKey] = append([][32]byte(nil), table...)
	}
	return used, nil
}

func policyAllAccounts(instructions []solana.Instruction) []string {
	seen := make(map[string]struct{})
	accounts := make([]string, 0, 32)
	for _, instruction := range instructions {
		for _, account := range instruction.Accounts {
			if _, ok := seen[account.Address]; ok {
				continue
			}
			seen[account.Address] = struct{}{}
			accounts = append(accounts, account.Address)
		}
	}
	return accounts
}

func policyStaticAccounts(routeProgram string, instructions []solana.Instruction) ([]string, error) {
	var fixed []string
	for _, instruction := range instructions {
		if instruction.Program != routeProgram {
			continue
		}
		count, ok := fixedRouteAccountCount(instruction.Data)
		if routeProgram != Program {
			count++
		}
		if fixed != nil || !ok || len(instruction.Accounts) < count {
			return nil, errors.New("Jupiter policy message must contain one route_v2 instruction")
		}
		fixed = make([]string, count)
		for index := range fixed {
			fixed[index] = instruction.Accounts[index].Address
		}
	}
	if fixed == nil {
		return nil, errors.New("Jupiter policy message is missing route_v2")
	}
	return fixed, nil
}

// ValidateSignedV0Transaction applies the same semantic policy to an already
// signed transaction and verifies its Ed25519 signature. It does not submit or
// otherwise authorize the transaction.
func ValidateSignedV0Transaction(
	policy Policy,
	request jupiterquote.Request,
	quote jupiterquote.Result,
	transaction []byte,
	addressTables map[[32]byte][][32]byte,
) (MessageIntent, string, error) {
	decoded, err := solana.DecodeSignedV0Transaction(transaction, addressTables)
	if err != nil {
		return MessageIntent{}, "", errors.New("decode signed Jupiter v0 transaction")
	}
	intent, err := ValidateV0Message(policy, request, quote, decoded.Message.Raw, addressTables)
	if err != nil {
		return MessageIntent{}, "", err
	}
	return intent, solana.Encode(decoded.Signature[:]), nil
}

// ValidateV0Message re-decodes the exact bytes presented to a signer and
// requires them to be the canonical compilation of the one allowed Jupiter
// plan. The byte comparison is important: Solana privileges are transaction
// wide, so validating only the source instruction metadata is insufficient.
func ValidateV0Message(
	policy Policy,
	request jupiterquote.Request,
	quote jupiterquote.Result,
	message []byte,
	addressTables map[[32]byte][][32]byte,
) (MessageIntent, error) {
	decoded, err := solana.DecodeV0Message(message, addressTables)
	if err != nil {
		return MessageIntent{}, errors.New("decode Jupiter v0 message")
	}
	if err := solana.ValidateV0MessageForSigner(decoded, policy.Owner); err != nil {
		return MessageIntent{}, errors.New("validate Jupiter v0 signer shape")
	}
	if len(addressTables) != len(decoded.AddressTableLookups) {
		return MessageIntent{}, errors.New("Jupiter v0 address-table evidence is not exact")
	}
	if len(decoded.Instructions) < 3 {
		return MessageIntent{}, errors.New("Jupiter v0 message is missing its compute budget or route")
	}
	limit, err := decodeComputeLimit(decoded, decoded.Instructions[0])
	if err != nil || limit > policy.MaxComputeUnits {
		return MessageIntent{}, errors.New("Jupiter compute-unit limit is outside policy")
	}
	price, priceInstruction, err := decodeComputePrice(decoded, decoded.Instructions[1])
	if err != nil || price > policy.MaxComputeUnitPriceMicroLamport {
		return MessageIntent{}, errors.New("Jupiter compute-unit price is outside policy")
	}

	plan := make([]solana.Instruction, len(decoded.Instructions)-2)
	for index, compiled := range decoded.Instructions[2:] {
		plan[index], err = canonicalPlanInstruction(decoded, compiled, policy.RouteGuard)
		if err != nil {
			return MessageIntent{}, err
		}
	}
	direct, err := UnwrapRoutePlan(policy.RouteGuard, plan)
	if err != nil {
		return MessageIntent{}, err
	}
	intent, err := ValidateProposal(
		policy, request, quote, []solana.Instruction{priceInstruction}, direct,
	)
	if err != nil {
		return MessageIntent{}, err
	}
	limitInstruction, _ := solana.SetComputeUnitLimitInstruction(limit)
	canonical := make([]solana.Instruction, 0, len(decoded.Instructions))
	canonical = append(canonical, limitInstruction, priceInstruction)
	canonical = append(canonical, plan...)
	rebuilt, err := buildPolicyV0Message(
		policy.RouteGuard.Program, policy.Owner, solana.Encode(decoded.RecentBlockhash[:]),
		canonical, addressTables,
	)
	if err != nil || !bytes.Equal(rebuilt, message) {
		return MessageIntent{}, errors.New("Jupiter v0 message is not the canonical allowed plan")
	}
	return MessageIntent{
		Intent: intent, ComputeUnits: limit, ComputeUnitPriceMicroLamport: price,
		RecentBlockhash: solana.Encode(decoded.RecentBlockhash[:]),
	}, nil
}

func decodeComputeLimit(message solana.V0Message, instruction solana.CompiledInstruction) (uint32, error) {
	if !compiledProgramIs(message, instruction, solana.ComputeBudgetProgram) ||
		len(instruction.Accounts) != 0 || len(instruction.Data) != 5 || instruction.Data[0] != 2 {
		return 0, errors.New("invalid compute-unit limit")
	}
	value := binary.LittleEndian.Uint32(instruction.Data[1:])
	if value == 0 || value > solana.MaxComputeUnitLimit {
		return 0, errors.New("invalid compute-unit limit")
	}
	return value, nil
}

func decodeComputePrice(
	message solana.V0Message,
	instruction solana.CompiledInstruction,
) (uint64, solana.Instruction, error) {
	if !compiledProgramIs(message, instruction, solana.ComputeBudgetProgram) ||
		len(instruction.Accounts) != 0 || len(instruction.Data) != 9 || instruction.Data[0] != 3 {
		return 0, solana.Instruction{}, errors.New("invalid compute-unit price")
	}
	value := binary.LittleEndian.Uint64(instruction.Data[1:])
	if value == 0 {
		return 0, solana.Instruction{}, errors.New("invalid compute-unit price")
	}
	return value, solana.Instruction{
		Program: solana.ComputeBudgetProgram, Data: append([]byte(nil), instruction.Data...),
	}, nil
}

func compiledProgramIs(message solana.V0Message, instruction solana.CompiledInstruction, program string) bool {
	return int(instruction.ProgramIndex) < len(message.AccountKeys) &&
		solana.Encode(message.AccountKeys[instruction.ProgramIndex][:]) == program
}

func canonicalPlanInstruction(
	message solana.V0Message,
	compiled solana.CompiledInstruction,
	deployment RouteGuardDeployment,
) (solana.Instruction, error) {
	if int(compiled.ProgramIndex) >= len(message.AccountKeys) {
		return solana.Instruction{}, errors.New("Jupiter instruction program index is invalid")
	}
	program := solana.Encode(message.AccountKeys[compiled.ProgramIndex][:])
	addresses := make([]string, len(compiled.Accounts))
	for index, accountIndex := range compiled.Accounts {
		if int(accountIndex) >= len(message.AccountKeys) {
			return solana.Instruction{}, errors.New("Jupiter instruction account index is invalid")
		}
		addresses[index] = solana.Encode(message.AccountKeys[accountIndex][:])
	}
	accounts, ok := canonicalAccountMetas(message, compiled, program, addresses, deployment)
	if !ok {
		return solana.Instruction{}, errors.New("Jupiter v0 message contains an unsupported instruction")
	}
	return solana.Instruction{
		Program: program, Accounts: accounts, Data: append([]byte(nil), compiled.Data...),
	}, nil
}

func canonicalAccountMetas(
	message solana.V0Message,
	compiled solana.CompiledInstruction,
	program string,
	addresses []string,
	deployment RouteGuardDeployment,
) ([]solana.AccountMeta, bool) {
	switch {
	case program == orcaswap.AssociatedTokenProgram && len(addresses) == 6 &&
		bytes.Equal(compiled.Data, []byte{1}):
		return []solana.AccountMeta{
			{Address: addresses[0], Signer: true, Writable: true},
			{Address: addresses[1], Writable: true}, {Address: addresses[2]},
			{Address: addresses[3]}, {Address: addresses[4]}, {Address: addresses[5]},
		}, true
	case program == orcaswap.SystemProgram && len(addresses) == 2 && len(compiled.Data) == 12 &&
		binary.LittleEndian.Uint32(compiled.Data[:4]) == 2:
		return []solana.AccountMeta{
			{Address: addresses[0], Signer: true, Writable: true},
			{Address: addresses[1], Writable: true},
		}, true
	case program == orcaswap.TokenProgram && len(addresses) == 1 &&
		bytes.Equal(compiled.Data, []byte{17}):
		return []solana.AccountMeta{{Address: addresses[0], Writable: true}}, true
	case program == orcaswap.TokenProgram && len(addresses) == 3 &&
		bytes.Equal(compiled.Data, []byte{9}):
		return []solana.AccountMeta{
			{Address: addresses[0], Writable: true}, {Address: addresses[1], Writable: true},
			{Address: addresses[2], Signer: true},
		}, true
	case program == Program:
		return nil, false
	case program == deployment.Program && len(addresses) > 1 &&
		addresses[0] == ProgramData && !message.IsSigner(int(compiled.Accounts[0])) &&
		!message.IsWritable(int(compiled.Accounts[0])):
		route := compiled
		route.Accounts = route.Accounts[1:]
		accounts, ok := canonicalRouteAccountMetas(message, route, addresses[1:])
		if !ok {
			return nil, false
		}
		return append([]solana.AccountMeta{{Address: ProgramData}}, accounts...), true
	default:
		return nil, false
	}
}

func fixedRouteAccountCount(data []byte) (int, bool) {
	if len(data) < 8 {
		return 0, false
	}
	switch {
	case bytes.Equal(data[:8], routeV2Discriminator[:]):
		return 10, true
	case bytes.Equal(data[:8], sharedAccountsRouteV2Discriminator[:]):
		return 12, true
	default:
		return 0, false
	}
}

func canonicalRouteAccountMetas(
	message solana.V0Message,
	compiled solana.CompiledInstruction,
	addresses []string,
) ([]solana.AccountMeta, bool) {
	count, ok := fixedRouteAccountCount(compiled.Data)
	if !ok || len(addresses) < count {
		return nil, false
	}
	if count == 10 {
		hasExplicitDestination := addresses[7] != Program
		for index := 1; index < count; index++ {
			wantWritable := index == 1 || index == 2 || index == 7 && hasExplicitDestination
			if message.IsWritable(int(compiled.Accounts[index])) != wantWritable {
				return nil, false
			}
		}
		accounts := []solana.AccountMeta{
			{Address: addresses[0], Signer: true},
			{Address: addresses[1], Writable: true}, {Address: addresses[2], Writable: true},
			{Address: addresses[3]}, {Address: addresses[4]}, {Address: addresses[5]},
			{Address: addresses[6]},
			{Address: addresses[7], Writable: hasExplicitDestination}, {Address: addresses[8]},
			{Address: addresses[9]},
		}
		return appendRemainingMetas(message, compiled, addresses, accounts), true
	}
	for index := range count {
		if index == 1 {
			continue
		}
		wantWritable := index >= 2 && index <= 5
		if message.IsWritable(int(compiled.Accounts[index])) != wantWritable {
			return nil, false
		}
	}
	accounts := []solana.AccountMeta{
		{Address: addresses[0]}, {Address: addresses[1], Signer: true},
		{Address: addresses[2], Writable: true}, {Address: addresses[3], Writable: true},
		{Address: addresses[4], Writable: true}, {Address: addresses[5], Writable: true},
		{Address: addresses[6]}, {Address: addresses[7]}, {Address: addresses[8]},
		{Address: addresses[9]}, {Address: addresses[10]}, {Address: addresses[11]},
	}
	return appendRemainingMetas(message, compiled, addresses, accounts), true
}

func appendRemainingMetas(
	message solana.V0Message,
	compiled solana.CompiledInstruction,
	addresses []string,
	accounts []solana.AccountMeta,
) []solana.AccountMeta {
	for index := len(accounts); index < len(addresses); index++ {
		accountIndex := int(compiled.Accounts[index])
		accounts = append(accounts, solana.AccountMeta{
			Address: addresses[index], Writable: message.IsWritable(accountIndex),
		})
	}
	return accounts
}
