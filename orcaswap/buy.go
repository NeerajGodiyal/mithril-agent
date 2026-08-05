package orcaswap

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/bits"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	BuyProfileName                          = "orca_devnet_buy_v2"
	BuyProfileVersion                uint32 = 2
	BuyRequestDomain                        = "mithril-agent/devnet-orca-buy-v2"
	buyActionIDDomain                       = "mithril-agent/orca-devnet-buy-v2/action-id"
	DefaultMaxTemporaryRentLamports         = uint64(3_000_000)
	tokenAccountSize                        = uint64(165)
	createAccountWithSeedInstruction        = uint32(3)
	initializeAccount3Instruction           = byte(18)
)

func ComputeBuyActionID(profileFingerprint string, scheduleWindowStartUnix int64) (string, error) {
	return computeActionID(buyActionIDDomain, profileFingerprint, scheduleWindowStartUnix)
}

// BuyPolicyV2 pins the B-to-A side of the Devnet SOL/devUSDC pool.
// Inputs use devUSDC base units; outputs and temporary-account limits use lamports.
type BuyPolicyV2 struct {
	Owner                    string `json:"owner"`
	Pool                     string `json:"pool"`
	TokenMintA               string `json:"token_mint_a"`
	TokenMintB               string `json:"token_mint_b"`
	InputTokenAccount        string `json:"input_token_account"`
	TokenVaultA              string `json:"token_vault_a"`
	TokenVaultB              string `json:"token_vault_b"`
	Oracle                   string `json:"oracle"`
	ProgramData              string `json:"program_data"`
	UpgradeAuthority         string `json:"upgrade_authority"`
	DeploymentSlot           uint64 `json:"deployment_slot"`
	MaxInputTokenAmount      uint64 `json:"max_input_token_amount"`
	MinOutputLamports        uint64 `json:"min_output_lamports"`
	MaxSlippageBPS           uint16 `json:"max_slippage_bps"`
	MaxTemporaryRentLamports uint64 `json:"max_temporary_rent_lamports"`
}

type BuyIntentV2 struct {
	Owner                 string
	Pool                  string
	InputMint             string
	OutputMint            string
	InputTokenAccount     string
	TemporaryWSOLAccount  string
	TemporarySeed         string
	RecentBlockhash       string
	InputAmount           uint64
	MinimumOutputLamports uint64
	TemporaryRentLamports uint64
}

// DiscoverBuyPolicyV2 requires the canonical input account to exist already.
// Creating it here would add a permanent rent debit that this policy does not authorize.
func DiscoverBuyPolicyV2(
	owner string,
	quote Quote,
	instructions []solana.Instruction,
) (BuyPolicyV2, error) {
	if len(instructions) != 4 {
		return BuyPolicyV2{}, errors.New("Orca buy requires an existing input account and the fixed temporary-WSOL sequence")
	}
	swap := instructions[2]
	if swap.Program != WhirlpoolProgram || len(swap.Accounts) != 17 {
		return BuyPolicyV2{}, errors.New("Orca buy instruction shape is outside policy")
	}
	policy := BuyPolicyV2{
		Owner: owner, Pool: DevnetPool,
		TokenMintA: WrappedSOLMint, TokenMintB: DevnetUSDCMint,
		InputTokenAccount:        swap.Accounts[9].Address,
		TokenVaultA:              swap.Accounts[8].Address,
		TokenVaultB:              swap.Accounts[10].Address,
		Oracle:                   swap.Accounts[14].Address,
		ProgramData:              WhirlpoolProgramData,
		UpgradeAuthority:         WhirlpoolUpgradeAuth,
		DeploymentSlot:           WhirlpoolDeploySlot,
		MaxInputTokenAmount:      quote.InputAmount,
		MinOutputLamports:        quote.MinimumOutput,
		MaxSlippageBPS:           quote.SlippageBPS,
		MaxTemporaryRentLamports: DefaultMaxTemporaryRentLamports,
	}
	if swap.Accounts[3].Address != owner || swap.Accounts[4].Address != DevnetPool ||
		swap.Accounts[5].Address != WrappedSOLMint ||
		swap.Accounts[6].Address != DevnetUSDCMint {
		return BuyPolicyV2{}, errors.New("Orca buy route does not match the fixed Devnet market")
	}
	if _, err := ValidateBuyInstructionsV2(policy, quote, instructions); err != nil {
		return BuyPolicyV2{}, err
	}
	return policy, nil
}

func (p BuyPolicyV2) Validate() error {
	addresses := []string{
		p.Owner, p.Pool, p.TokenMintA, p.TokenMintB, p.InputTokenAccount,
		p.TokenVaultA, p.TokenVaultB, p.Oracle, p.ProgramData, p.UpgradeAuthority,
	}
	seen := make(map[[32]byte]struct{}, len(addresses))
	for _, value := range addresses {
		key, err := solana.Decode32(value)
		if err != nil {
			return errors.New("Orca buy policy contains an invalid address")
		}
		if _, exists := seen[key]; exists {
			return errors.New("Orca buy policy addresses must be distinct")
		}
		seen[key] = struct{}{}
	}
	if p.Pool != DevnetPool || p.TokenMintA != WrappedSOLMint ||
		p.TokenMintB != DevnetUSDCMint || p.ProgramData != WhirlpoolProgramData ||
		p.UpgradeAuthority != WhirlpoolUpgradeAuth || p.DeploymentSlot != WhirlpoolDeploySlot {
		return errors.New("Orca buy v2 must use the fixed Devnet devUSDC-to-WSOL market")
	}
	oracle, err := OracleAddress(p.Pool)
	if err != nil || p.Oracle != oracle {
		return errors.New("Orca buy oracle is not the pool's canonical account")
	}
	inputAccount, err := AssociatedTokenAddress(p.Owner, p.TokenMintB)
	if err != nil || p.InputTokenAccount != inputAccount {
		return errors.New("Orca buy input token account is not the wallet's canonical account")
	}
	if p.MaxInputTokenAmount == 0 || p.MinOutputLamports == 0 ||
		p.MaxSlippageBPS == 0 || p.MaxSlippageBPS > 500 ||
		p.MaxTemporaryRentLamports == 0 || p.MaxTemporaryRentLamports > 10_000_000 {
		return errors.New("Orca buy policy limits are invalid")
	}
	return nil
}

func ValidateBuyInstructionsV2(
	policy BuyPolicyV2,
	quote Quote,
	instructions []solana.Instruction,
) (BuyIntentV2, error) {
	if err := policy.Validate(); err != nil {
		return BuyIntentV2{}, err
	}
	if quote.InputAmount == 0 || quote.InputAmount > policy.MaxInputTokenAmount ||
		quote.EstimatedOutput == 0 || quote.MinimumOutput > quote.EstimatedOutput ||
		quote.MinimumOutput < policy.MinOutputLamports || quote.SlippageBPS == 0 ||
		quote.SlippageBPS > policy.MaxSlippageBPS {
		return BuyIntentV2{}, errors.New("Orca buy quote is outside policy")
	}
	high, low := bits.Mul64(quote.EstimatedOutput, uint64(10_000-quote.SlippageBPS))
	minimumAllowed, _ := bits.Div64(high, low, 10_000)
	if quote.MinimumOutput < minimumAllowed {
		return BuyIntentV2{}, errors.New("Orca buy minimum output falls below the slippage floor")
	}
	if len(instructions) != 4 {
		return BuyIntentV2{}, errors.New("Orca buy requires an existing input account and the fixed temporary-WSOL sequence")
	}
	for _, instruction := range instructions {
		for _, account := range instruction.Accounts {
			if account.Signer && account.Address != policy.Owner {
				return BuyIntentV2{}, errors.New("Orca buy requires an unexpected signer")
			}
		}
	}
	if err := validateBuyInstructionRoles(policy, instructions); err != nil {
		return BuyIntentV2{}, err
	}
	dummyBlockhash := solana.Encode(bytes.Repeat([]byte{1}, 32))
	message, err := solana.BuildLegacyMessage(policy.Owner, dummyBlockhash, instructions)
	if err != nil {
		return BuyIntentV2{}, err
	}
	intent, err := DecodeBuyMessageV2(policy, message)
	if err != nil {
		return BuyIntentV2{}, err
	}
	if intent.InputAmount != quote.InputAmount ||
		intent.MinimumOutputLamports != quote.MinimumOutput {
		return BuyIntentV2{}, errors.New("Orca buy instruction amounts do not match the quote")
	}
	return intent, nil
}

func DecodeBuyMessageV2(policy BuyPolicyV2, message []byte) (BuyIntentV2, error) {
	if err := policy.Validate(); err != nil {
		return BuyIntentV2{}, err
	}
	decoded, err := solana.DecodeLegacyMessage(message)
	if err != nil {
		return BuyIntentV2{}, err
	}
	if len(decoded.AccountKeys) == 0 || address(decoded.AccountKeys[0]) != policy.Owner ||
		decoded.RequiredSignatures != 1 || decoded.ReadonlySigned != 0 {
		return BuyIntentV2{}, errors.New("Orca buy fee payer is outside policy")
	}
	if decoded.RecentBlockhash == ([32]byte{}) {
		return BuyIntentV2{}, errors.New("Orca buy recent blockhash is invalid")
	}
	if len(decoded.Instructions) != 4 {
		return BuyIntentV2{}, errors.New("Orca buy must use the fixed temporary-WSOL instruction sequence")
	}
	for _, instruction := range decoded.Instructions {
		if decoded.IsSigner(int(instruction.ProgramIndex)) ||
			decoded.IsWritable(int(instruction.ProgramIndex)) {
			return BuyIntentV2{}, errors.New("Orca buy program account has unsafe privileges")
		}
	}
	temporary, seed, rent, err := validateCreateWithSeed(
		decoded, decoded.Instructions[0], policy.Owner, policy.MaxTemporaryRentLamports,
	)
	if err != nil {
		return BuyIntentV2{}, err
	}
	if err := validateInitializeAccount3(decoded, decoded.Instructions[1], temporary, policy); err != nil {
		return BuyIntentV2{}, err
	}
	inputAmount, minimumOutput, err := validateBuySwapV2(decoded, decoded.Instructions[2], temporary, policy)
	if err != nil {
		return BuyIntentV2{}, err
	}
	if err := validateTokenInstruction(decoded, decoded.Instructions[3], 9, []string{
		temporary, policy.Owner, policy.Owner,
	}); err != nil {
		return BuyIntentV2{}, err
	}
	if inputAmount == 0 || inputAmount > policy.MaxInputTokenAmount ||
		minimumOutput < policy.MinOutputLamports {
		return BuyIntentV2{}, errors.New("Orca buy amounts are outside policy")
	}
	if err := validateBuyMessagePrivileges(decoded, decoded.Instructions[2], temporary, policy); err != nil {
		return BuyIntentV2{}, err
	}
	if err := rejectUnusedAccounts(decoded); err != nil {
		return BuyIntentV2{}, err
	}
	return BuyIntentV2{
		Owner: policy.Owner, Pool: policy.Pool,
		InputMint: policy.TokenMintB, OutputMint: policy.TokenMintA,
		InputTokenAccount: policy.InputTokenAccount, TemporaryWSOLAccount: temporary,
		TemporarySeed: seed, RecentBlockhash: address(decoded.RecentBlockhash),
		InputAmount: inputAmount, MinimumOutputLamports: minimumOutput,
		TemporaryRentLamports: rent,
	}, nil
}

func validateBuyInstructionRoles(policy BuyPolicyV2, instructions []solana.Instruction) error {
	temporary, _, rent, err := parseCreateWithSeedData(instructions[0].Data, policy.Owner)
	if err != nil || rent == 0 || rent > policy.MaxTemporaryRentLamports ||
		!matchesInstruction(instructions[0], SystemProgram, []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: temporary, Writable: true},
			{Address: policy.Owner, Signer: true},
		}, instructions[0].Data) {
		return errors.New("Orca buy temporary-account setup is outside policy")
	}
	owner, _ := solana.Decode32(policy.Owner)
	initData := append([]byte{initializeAccount3Instruction}, owner[:]...)
	if !matchesInstruction(instructions[1], TokenProgram, []solana.AccountMeta{
		{Address: temporary, Writable: true}, {Address: policy.TokenMintA},
	}, initData) {
		return errors.New("Orca buy temporary-account initialization is outside policy")
	}
	swap := instructions[2]
	if swap.Program != WhirlpoolProgram || len(swap.Accounts) != 17 ||
		!metaEqual(swap.Accounts[0], solana.AccountMeta{Address: TokenProgram}) ||
		!metaEqual(swap.Accounts[1], solana.AccountMeta{Address: TokenProgram}) ||
		!metaEqual(swap.Accounts[2], solana.AccountMeta{Address: MemoProgram}) ||
		!metaEqual(swap.Accounts[3], solana.AccountMeta{Address: policy.Owner, Signer: true}) ||
		!metaEqual(swap.Accounts[4], solana.AccountMeta{Address: policy.Pool, Writable: true}) ||
		!metaEqual(swap.Accounts[5], solana.AccountMeta{Address: policy.TokenMintA}) ||
		!metaEqual(swap.Accounts[6], solana.AccountMeta{Address: policy.TokenMintB}) ||
		!metaEqual(swap.Accounts[7], solana.AccountMeta{Address: temporary, Writable: true}) ||
		!metaEqual(swap.Accounts[8], solana.AccountMeta{Address: policy.TokenVaultA, Writable: true}) ||
		!metaEqual(swap.Accounts[9], solana.AccountMeta{Address: policy.InputTokenAccount, Writable: true}) ||
		!metaEqual(swap.Accounts[10], solana.AccountMeta{Address: policy.TokenVaultB, Writable: true}) ||
		!metaEqual(swap.Accounts[14], solana.AccountMeta{Address: policy.Oracle, Writable: true}) {
		return errors.New("Orca buy account roles are outside policy")
	}
	for _, index := range [...]int{11, 12, 13, 15, 16} {
		if swap.Accounts[index].Signer || !swap.Accounts[index].Writable {
			return errors.New("Orca buy dynamic account roles are outside policy")
		}
	}
	if !matchesInstruction(instructions[3], TokenProgram, []solana.AccountMeta{
		{Address: temporary, Writable: true},
		{Address: policy.Owner, Writable: true},
		{Address: policy.Owner, Signer: true},
	}, []byte{9}) {
		return errors.New("Orca buy temporary-account cleanup is outside policy")
	}
	return nil
}

func validateCreateWithSeed(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	owner string,
	maxRent uint64,
) (temporary, seed string, rent uint64, err error) {
	if program(message, instruction) != SystemProgram {
		return "", "", 0, errors.New("Orca buy temporary-account program is outside policy")
	}
	temporary, seed, rent, err = parseCreateWithSeedData(instruction.Data, owner)
	if err != nil || rent == 0 || rent > maxRent ||
		!accountsEqual(message, instruction, []string{owner, temporary, owner}) {
		return "", "", 0, errors.New("Orca buy temporary-account setup is outside policy")
	}
	return temporary, seed, rent, nil
}

func parseCreateWithSeedData(data []byte, owner string) (temporary, seed string, rent uint64, err error) {
	const fixed = 4 + 32 + 8 + 8 + 8 + 32
	if len(data) < fixed || binary.LittleEndian.Uint32(data[:4]) != createAccountWithSeedInstruction {
		return "", "", 0, errors.New("invalid create-with-seed data")
	}
	ownerKey, ownerErr := solana.Decode32(owner)
	if ownerErr != nil || !bytes.Equal(data[4:36], ownerKey[:]) {
		return "", "", 0, errors.New("invalid create-with-seed base")
	}
	seedLength := binary.LittleEndian.Uint64(data[36:44])
	if seedLength == 0 || seedLength > 32 || seedLength > uint64(len(data)-fixed) {
		return "", "", 0, errors.New("invalid create-with-seed seed")
	}
	seedEnd := 44 + int(seedLength)
	if len(data) != seedEnd+8+8+32 {
		return "", "", 0, errors.New("invalid create-with-seed length")
	}
	seedBytes := data[44:seedEnd]
	if len(seedBytes) != 13 {
		return "", "", 0, errors.New("invalid create-with-seed seed")
	}
	for _, value := range seedBytes {
		if value < '0' || value > '9' {
			return "", "", 0, errors.New("invalid create-with-seed seed")
		}
	}
	rent = binary.LittleEndian.Uint64(data[seedEnd : seedEnd+8])
	if binary.LittleEndian.Uint64(data[seedEnd+8:seedEnd+16]) != tokenAccountSize {
		return "", "", 0, errors.New("invalid create-with-seed space")
	}
	tokenProgram, tokenErr := solana.Decode32(TokenProgram)
	if tokenErr != nil || !bytes.Equal(data[seedEnd+16:], tokenProgram[:]) {
		return "", "", 0, errors.New("invalid create-with-seed owner")
	}
	hash := sha256.New()
	_, _ = hash.Write(ownerKey[:])
	_, _ = hash.Write(seedBytes)
	_, _ = hash.Write(tokenProgram[:])
	return solana.Encode(hash.Sum(nil)), string(seedBytes), rent, nil
}

func validateInitializeAccount3(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	temporary string,
	policy BuyPolicyV2,
) error {
	owner, _ := solana.Decode32(policy.Owner)
	wantData := append([]byte{initializeAccount3Instruction}, owner[:]...)
	if program(message, instruction) != TokenProgram ||
		!accountsEqual(message, instruction, []string{temporary, policy.TokenMintA}) ||
		!bytes.Equal(instruction.Data, wantData) {
		return errors.New("Orca buy temporary-account initialization is outside policy")
	}
	return nil
}

func validateBuySwapV2(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	temporary string,
	policy BuyPolicyV2,
) (inputAmount, minimumOutput uint64, err error) {
	if program(message, instruction) != WhirlpoolProgram || len(instruction.Accounts) != 17 ||
		len(instruction.Data) != 49 || !bytes.Equal(instruction.Data[:8], swapV2Discriminator) {
		return 0, 0, errors.New("Orca buy instruction shape is outside policy")
	}
	wantPrefix := []string{
		TokenProgram, TokenProgram, MemoProgram, policy.Owner, policy.Pool,
		policy.TokenMintA, policy.TokenMintB, temporary, policy.TokenVaultA,
		policy.InputTokenAccount, policy.TokenVaultB,
	}
	if !accountsPrefixEqual(message, instruction, wantPrefix) ||
		account(message, instruction, 14) != policy.Oracle {
		return 0, 0, errors.New("Orca buy instruction accounts are outside policy")
	}
	for _, index := range [...]int{11, 12, 13, 14, 15, 16} {
		if !message.IsWritable(int(instruction.Accounts[index])) ||
			message.IsSigner(int(instruction.Accounts[index])) {
			return 0, 0, errors.New("Orca buy dynamic account privileges are invalid")
		}
	}
	if !bytes.Equal(instruction.Data[24:40], make([]byte, 16)) ||
		instruction.Data[40] != 1 || instruction.Data[41] != 0 ||
		!bytes.Equal(instruction.Data[42:], []byte{1, 1, 0, 0, 0, 6, 2}) {
		return 0, 0, errors.New("Orca buy instruction data is outside policy")
	}
	inputAmount = binary.LittleEndian.Uint64(instruction.Data[8:16])
	minimumOutput = binary.LittleEndian.Uint64(instruction.Data[16:24])
	if inputAmount == 0 || minimumOutput == 0 {
		return 0, 0, errors.New("Orca buy instruction amount is zero")
	}
	return inputAmount, minimumOutput, nil
}

func validateBuyMessagePrivileges(
	message solana.LegacyMessage,
	swap solana.CompiledInstruction,
	temporary string,
	policy BuyPolicyV2,
) error {
	expected := map[string]bool{
		policy.Owner: true, policy.Pool: true,
		policy.TokenMintA: false, policy.TokenMintB: false,
		policy.InputTokenAccount: true, temporary: true,
		policy.TokenVaultA: true, policy.TokenVaultB: true, policy.Oracle: true,
		SystemProgram: false, TokenProgram: false, MemoProgram: false,
		WhirlpoolProgram: false,
	}
	for _, index := range [...]int{11, 12, 13, 15, 16} {
		candidate := account(message, swap, index)
		if _, exists := expected[candidate]; exists {
			return errors.New("Orca buy dynamic accounts must be distinct")
		}
		expected[candidate] = true
	}
	if len(message.AccountKeys) != len(expected) {
		return errors.New("Orca buy account set is outside policy")
	}
	for index, key := range message.AccountKeys {
		candidate := address(key)
		writable, ok := expected[candidate]
		if !ok || message.IsSigner(index) != (candidate == policy.Owner) ||
			message.IsWritable(index) != writable {
			return errors.New("Orca buy account privileges are outside policy")
		}
	}
	return nil
}
