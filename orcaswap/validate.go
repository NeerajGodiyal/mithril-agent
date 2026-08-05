package orcaswap

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/bits"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	ProfileName    = "orca_devnet_swap_v1"
	ProfileVersion = uint32(1)
	RequestDomain  = "mithril-agent/devnet-orca-swap-v1"
	actionIDDomain = "mithril-agent/orca-devnet-swap-v1/action-id"

	SystemProgram                       = "11111111111111111111111111111111"
	TokenProgram                        = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	AssociatedTokenProgram              = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"
	MemoProgram                         = "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"
	WhirlpoolProgram                    = "whirLbMiicVdio4qvUfM5KAg6Ct8VwpYzGff3uctyCc"
	UpgradeableLoader                   = "BPFLoaderUpgradeab1e11111111111111111111111"
	WhirlpoolProgramData                = "CtXfPzz36dH5Ws4UYKZvrQ1Xqzn42ecDW6y8NKuiN8nD"
	WhirlpoolUpgradeAuth                = "8rHQUMS8qg4qrQriPpTbrZqk2yjLrUHSLpA6MzgMSiM"
	WhirlpoolDeploySlot                 = uint64(439560432)
	WrappedSOLMint                      = "So11111111111111111111111111111111111111112"
	DevnetPool                          = "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt"
	DevnetUSDCMint                      = "BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k"
	DefaultMaxOutputAccountRentLamports = uint64(3_000_000)
)

var swapV2Discriminator = []byte{43, 4, 237, 11, 26, 201, 30, 98}

type Policy struct {
	Owner                        string `json:"owner"`
	Pool                         string `json:"pool"`
	InputMint                    string `json:"input_mint"`
	OutputMint                   string `json:"output_mint"`
	InputTokenAccount            string `json:"input_token_account"`
	OutputTokenAccount           string `json:"output_token_account"`
	TokenVaultA                  string `json:"token_vault_a"`
	TokenVaultB                  string `json:"token_vault_b"`
	Oracle                       string `json:"oracle"`
	ProgramData                  string `json:"program_data"`
	UpgradeAuthority             string `json:"upgrade_authority"`
	DeploymentSlot               uint64 `json:"deployment_slot"`
	MaxInputLamports             uint64 `json:"max_input_lamports"`
	MinOutputAmount              uint64 `json:"min_output_amount"`
	MaxSlippageBPS               uint16 `json:"max_slippage_bps"`
	MaxOutputAccountRentLamports uint64 `json:"max_output_account_rent_lamports"`
}

type Quote struct {
	InputAmount     uint64
	EstimatedOutput uint64
	MinimumOutput   uint64
	SlippageBPS     uint16
}

type Intent struct {
	Owner                string
	Pool                 string
	InputMint            string
	OutputMint           string
	InputTokenAccount    string
	OutputTokenAccount   string
	RecentBlockhash      string
	InputAmount          uint64
	MinimumOutput        uint64
	OutputAccountCreated bool
}

// DiscoverPolicy extracts the fixed route accounts from SDK output, then
// validates the complete instruction sequence before returning them.
func DiscoverPolicy(
	owner string,
	quote Quote,
	instructions []solana.Instruction,
) (Policy, error) {
	if len(instructions) != 5 && len(instructions) != 6 {
		return Policy{}, errors.New("Orca swap must use the canonical WSOL setup and at most one output-account setup")
	}
	swap := instructions[len(instructions)-2]
	if swap.Program != WhirlpoolProgram || len(swap.Accounts) != 17 {
		return Policy{}, errors.New("Orca swap instruction shape is outside policy")
	}
	policy := Policy{
		Owner: owner, Pool: DevnetPool,
		InputMint: WrappedSOLMint, OutputMint: DevnetUSDCMint,
		InputTokenAccount:            swap.Accounts[7].Address,
		OutputTokenAccount:           swap.Accounts[9].Address,
		TokenVaultA:                  swap.Accounts[8].Address,
		TokenVaultB:                  swap.Accounts[10].Address,
		Oracle:                       swap.Accounts[14].Address,
		ProgramData:                  WhirlpoolProgramData,
		UpgradeAuthority:             WhirlpoolUpgradeAuth,
		DeploymentSlot:               WhirlpoolDeploySlot,
		MaxInputLamports:             quote.InputAmount,
		MinOutputAmount:              quote.MinimumOutput,
		MaxSlippageBPS:               quote.SlippageBPS,
		MaxOutputAccountRentLamports: DefaultMaxOutputAccountRentLamports,
	}
	if swap.Accounts[3].Address != owner || swap.Accounts[4].Address != DevnetPool ||
		swap.Accounts[5].Address != WrappedSOLMint ||
		swap.Accounts[6].Address != DevnetUSDCMint {
		return Policy{}, errors.New("Orca swap route does not match the fixed Devnet market")
	}
	if _, err := ValidateInstructions(policy, quote, instructions); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (p Policy) Validate() error {
	addresses := []string{
		p.Owner, p.Pool, p.InputMint, p.OutputMint, p.InputTokenAccount,
		p.OutputTokenAccount, p.TokenVaultA, p.TokenVaultB, p.Oracle,
		p.ProgramData, p.UpgradeAuthority,
	}
	seen := make(map[[32]byte]struct{}, len(addresses))
	for _, address := range addresses {
		key, err := solana.Decode32(address)
		if err != nil {
			return errors.New("Orca swap policy contains an invalid address")
		}
		if _, exists := seen[key]; exists {
			return errors.New("Orca swap policy addresses must be distinct")
		}
		seen[key] = struct{}{}
	}
	if p.Pool != DevnetPool || p.InputMint != WrappedSOLMint ||
		p.OutputMint != DevnetUSDCMint || p.ProgramData != WhirlpoolProgramData ||
		p.UpgradeAuthority != WhirlpoolUpgradeAuth || p.DeploymentSlot != WhirlpoolDeploySlot {
		return errors.New("Orca swap v1 must use the fixed Devnet WSOL-to-devUSDC market")
	}
	oracle, err := OracleAddress(p.Pool)
	if err != nil || p.Oracle != oracle {
		return errors.New("Orca swap oracle is not the pool's canonical account")
	}
	inputTokenAccount, err := AssociatedTokenAddress(p.Owner, p.InputMint)
	if err != nil || p.InputTokenAccount != inputTokenAccount {
		return errors.New("Orca input token account is not the wallet's canonical account")
	}
	outputTokenAccount, err := AssociatedTokenAddress(p.Owner, p.OutputMint)
	if err != nil || p.OutputTokenAccount != outputTokenAccount {
		return errors.New("Orca output token account is not the wallet's canonical account")
	}
	if p.MaxInputLamports == 0 || p.MinOutputAmount == 0 ||
		p.MaxSlippageBPS == 0 || p.MaxSlippageBPS > 500 ||
		p.MaxOutputAccountRentLamports == 0 || p.MaxOutputAccountRentLamports > 10_000_000 {
		return errors.New("Orca swap policy limits are invalid")
	}
	return nil
}

func ComputeActionID(profileFingerprint string, scheduleWindowStartUnix int64) (string, error) {
	return computeActionID(actionIDDomain, profileFingerprint, scheduleWindowStartUnix)
}

func computeActionID(domain, profileFingerprint string, scheduleWindowStartUnix int64) (string, error) {
	decoded, err := hex.DecodeString(profileFingerprint)
	if err != nil || len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != profileFingerprint {
		return "", errors.New("swap profile fingerprint is invalid")
	}
	if scheduleWindowStartUnix <= 0 {
		return "", errors.New("swap schedule window start is invalid")
	}
	encoded, err := json.Marshal(struct {
		Domain                  string `json:"domain"`
		ProfileFingerprint      string `json:"profile_sha256"`
		ScheduleWindowStartUnix int64  `json:"schedule_window_start_unix"`
	}{
		Domain:                  domain,
		ProfileFingerprint:      profileFingerprint,
		ScheduleWindowStartUnix: scheduleWindowStartUnix,
	})
	if err != nil {
		return "", errors.New("encode swap action identity")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateInstructions checks the richer SDK output before compilation. The
// signer repeats the security-relevant checks on the compiled message.
func ValidateInstructions(
	policy Policy,
	quote Quote,
	instructions []solana.Instruction,
) (Intent, error) {
	if err := policy.Validate(); err != nil {
		return Intent{}, err
	}
	if quote.InputAmount == 0 || quote.InputAmount > policy.MaxInputLamports ||
		quote.EstimatedOutput == 0 || quote.MinimumOutput > quote.EstimatedOutput ||
		quote.MinimumOutput < policy.MinOutputAmount ||
		quote.SlippageBPS == 0 || quote.SlippageBPS > policy.MaxSlippageBPS {
		return Intent{}, errors.New("Orca quote is outside policy")
	}
	high, low := bits.Mul64(quote.EstimatedOutput, uint64(10_000-quote.SlippageBPS))
	minimumAllowed, _ := bits.Div64(high, low, 10_000)
	if quote.MinimumOutput < minimumAllowed {
		return Intent{}, errors.New("Orca quote minimum output falls below the slippage floor")
	}
	if len(instructions) != 5 && len(instructions) != 6 {
		return Intent{}, errors.New("Orca swap must use the canonical WSOL setup and at most one output-account setup")
	}
	for _, instruction := range instructions {
		for _, account := range instruction.Accounts {
			if account.Signer && account.Address != policy.Owner {
				return Intent{}, errors.New("Orca swap requires an unexpected signer")
			}
		}
	}
	if err := validateInstructionRoles(policy, instructions); err != nil {
		return Intent{}, err
	}
	dummyBlockhash := solana.Encode(bytes.Repeat([]byte{1}, 32))
	message, err := solana.BuildLegacyMessage(policy.Owner, dummyBlockhash, instructions)
	if err != nil {
		return Intent{}, err
	}
	intent, err := DecodeMessage(policy, message)
	if err != nil {
		return Intent{}, err
	}
	if intent.InputAmount != quote.InputAmount || intent.MinimumOutput != quote.MinimumOutput {
		return Intent{}, errors.New("Orca instruction amounts do not match the quote")
	}
	return intent, nil
}

func DecodeMessage(policy Policy, message []byte) (Intent, error) {
	if err := policy.Validate(); err != nil {
		return Intent{}, err
	}
	decoded, err := solana.DecodeLegacyMessage(message)
	if err != nil {
		return Intent{}, err
	}
	if len(decoded.AccountKeys) == 0 || address(decoded.AccountKeys[0]) != policy.Owner ||
		decoded.RequiredSignatures != 1 || decoded.ReadonlySigned != 0 {
		return Intent{}, errors.New("Orca swap fee payer is outside policy")
	}
	if decoded.RecentBlockhash == ([32]byte{}) {
		return Intent{}, errors.New("Orca swap recent blockhash is invalid")
	}
	if len(decoded.Instructions) != 5 && len(decoded.Instructions) != 6 {
		return Intent{}, errors.New("Orca swap must use the canonical WSOL setup and at most one output-account setup")
	}

	for _, instruction := range decoded.Instructions {
		if decoded.IsSigner(int(instruction.ProgramIndex)) || decoded.IsWritable(int(instruction.ProgramIndex)) {
			return Intent{}, errors.New("Orca swap program account has unsafe privileges")
		}
	}
	offset := 0
	if err := validateATAInstruction(decoded, decoded.Instructions[offset], policy.InputTokenAccount, policy.InputMint, policy.Owner); err != nil {
		return Intent{}, err
	}
	offset++
	outputAccountCreated := len(decoded.Instructions) == 6
	if outputAccountCreated {
		if err := validateATAInstruction(decoded, decoded.Instructions[offset], policy.OutputTokenAccount, policy.OutputMint, policy.Owner); err != nil {
			return Intent{}, err
		}
		offset++
	}
	transferAmount, err := validateSystemTransfer(decoded, decoded.Instructions[offset], policy.Owner, policy.InputTokenAccount)
	if err != nil {
		return Intent{}, err
	}
	offset++
	if err := validateTokenInstruction(decoded, decoded.Instructions[offset], 17, []string{policy.InputTokenAccount}); err != nil {
		return Intent{}, err
	}
	offset++
	swapInstruction := decoded.Instructions[offset]
	minimumOutput, err := validateSwapV2(decoded, swapInstruction, policy, transferAmount)
	if err != nil {
		return Intent{}, err
	}
	offset++
	if err := validateTokenInstruction(decoded, decoded.Instructions[offset], 9, []string{
		policy.InputTokenAccount, policy.Owner, policy.Owner,
	}); err != nil {
		return Intent{}, err
	}
	if transferAmount == 0 || transferAmount > policy.MaxInputLamports ||
		minimumOutput < policy.MinOutputAmount {
		return Intent{}, errors.New("Orca swap amounts are outside policy")
	}
	if err := validateMessagePrivileges(decoded, swapInstruction, policy); err != nil {
		return Intent{}, err
	}
	if err := rejectUnusedAccounts(decoded); err != nil {
		return Intent{}, err
	}
	return Intent{
		Owner:                policy.Owner,
		Pool:                 policy.Pool,
		InputMint:            policy.InputMint,
		OutputMint:           policy.OutputMint,
		InputTokenAccount:    policy.InputTokenAccount,
		OutputTokenAccount:   policy.OutputTokenAccount,
		RecentBlockhash:      address(decoded.RecentBlockhash),
		InputAmount:          transferAmount,
		MinimumOutput:        minimumOutput,
		OutputAccountCreated: outputAccountCreated,
	}, nil
}

func validateMessagePrivileges(
	message solana.LegacyMessage,
	swap solana.CompiledInstruction,
	policy Policy,
) error {
	expected := map[string]bool{
		policy.Owner: true, policy.Pool: true,
		policy.InputMint: false, policy.OutputMint: false,
		policy.InputTokenAccount: true, policy.OutputTokenAccount: true,
		policy.TokenVaultA: true, policy.TokenVaultB: true, policy.Oracle: true,
		SystemProgram: false, TokenProgram: false, AssociatedTokenProgram: false,
		MemoProgram: false, WhirlpoolProgram: false,
	}
	for _, index := range [...]int{11, 12, 13, 15, 16} {
		candidate := account(message, swap, index)
		if _, exists := expected[candidate]; exists {
			return errors.New("Orca swap dynamic accounts must be distinct")
		}
		expected[candidate] = true
	}
	if len(message.AccountKeys) != len(expected) {
		return errors.New("Orca swap account set is outside policy")
	}
	for index, key := range message.AccountKeys {
		candidate := address(key)
		writable, ok := expected[candidate]
		if !ok || message.IsSigner(index) != (candidate == policy.Owner) ||
			message.IsWritable(index) != writable {
			return errors.New("Orca swap account privileges are outside policy")
		}
	}
	return nil
}

func validateInstructionRoles(policy Policy, instructions []solana.Instruction) error {
	offset := 0
	if !matchesInstruction(instructions[offset], AssociatedTokenProgram, []solana.AccountMeta{
		{Address: policy.Owner, Signer: true, Writable: true},
		{Address: policy.InputTokenAccount, Writable: true},
		{Address: policy.Owner}, {Address: policy.InputMint},
		{Address: SystemProgram}, {Address: TokenProgram},
	}, []byte{1}) {
		return errors.New("Orca input-token setup is outside policy")
	}
	offset++
	if len(instructions) == 6 {
		if !matchesInstruction(instructions[offset], AssociatedTokenProgram, []solana.AccountMeta{
			{Address: policy.Owner, Signer: true, Writable: true},
			{Address: policy.OutputTokenAccount, Writable: true},
			{Address: policy.Owner}, {Address: policy.OutputMint},
			{Address: SystemProgram}, {Address: TokenProgram},
		}, []byte{1}) {
			return errors.New("Orca output-token setup is outside policy")
		}
		offset++
	}
	if !matchesInstruction(instructions[offset], SystemProgram, []solana.AccountMeta{
		{Address: policy.Owner, Signer: true, Writable: true},
		{Address: policy.InputTokenAccount, Writable: true},
	}, instructions[offset].Data) || len(instructions[offset].Data) != 12 {
		return errors.New("Orca native-token funding roles are outside policy")
	}
	offset++
	if !matchesInstruction(instructions[offset], TokenProgram, []solana.AccountMeta{
		{Address: policy.InputTokenAccount, Writable: true},
	}, []byte{17}) {
		return errors.New("Orca native-token sync roles are outside policy")
	}
	offset++
	swap := instructions[offset]
	if swap.Program != WhirlpoolProgram || len(swap.Accounts) != 17 ||
		!metaEqual(swap.Accounts[0], solana.AccountMeta{Address: TokenProgram}) ||
		!metaEqual(swap.Accounts[1], solana.AccountMeta{Address: TokenProgram}) ||
		!metaEqual(swap.Accounts[2], solana.AccountMeta{Address: MemoProgram}) ||
		!metaEqual(swap.Accounts[3], solana.AccountMeta{Address: policy.Owner, Signer: true}) ||
		!metaEqual(swap.Accounts[4], solana.AccountMeta{Address: policy.Pool, Writable: true}) ||
		!metaEqual(swap.Accounts[5], solana.AccountMeta{Address: policy.InputMint}) ||
		!metaEqual(swap.Accounts[6], solana.AccountMeta{Address: policy.OutputMint}) ||
		!metaEqual(swap.Accounts[7], solana.AccountMeta{Address: policy.InputTokenAccount, Writable: true}) ||
		!metaEqual(swap.Accounts[8], solana.AccountMeta{Address: policy.TokenVaultA, Writable: true}) ||
		!metaEqual(swap.Accounts[9], solana.AccountMeta{Address: policy.OutputTokenAccount, Writable: true}) ||
		!metaEqual(swap.Accounts[10], solana.AccountMeta{Address: policy.TokenVaultB, Writable: true}) ||
		!metaEqual(swap.Accounts[14], solana.AccountMeta{Address: policy.Oracle, Writable: true}) {
		return errors.New("Orca swap account roles are outside policy")
	}
	for _, index := range [...]int{11, 12, 13, 15, 16} {
		if swap.Accounts[index].Signer || !swap.Accounts[index].Writable {
			return errors.New("Orca swap dynamic account roles are outside policy")
		}
	}
	offset++
	if !matchesInstruction(instructions[offset], TokenProgram, []solana.AccountMeta{
		{Address: policy.InputTokenAccount, Writable: true},
		{Address: policy.Owner, Writable: true},
		{Address: policy.Owner, Signer: true},
	}, []byte{9}) {
		return errors.New("Orca native-token cleanup roles are outside policy")
	}
	return nil
}

// AssociatedTokenAddress derives the wallet's canonical account for a mint.
func AssociatedTokenAddress(owner, mint string) (string, error) {
	ownerKey, err := solana.Decode32(owner)
	if err != nil {
		return "", errors.New("associated token owner is invalid")
	}
	mintKey, err := solana.Decode32(mint)
	if err != nil {
		return "", errors.New("associated token mint is invalid")
	}
	tokenProgram, err := solana.Decode32(TokenProgram)
	if err != nil {
		return "", errors.New("token program is invalid")
	}
	address, _, err := solana.FindProgramAddress(
		[][]byte{ownerKey[:], tokenProgram[:], mintKey[:]},
		AssociatedTokenProgram,
	)
	if err != nil {
		return "", errors.New("derive associated token account")
	}
	return address, nil
}

// OracleAddress derives the canonical Whirlpool oracle for a pool.
func OracleAddress(pool string) (string, error) {
	poolKey, err := solana.Decode32(pool)
	if err != nil {
		return "", errors.New("Orca pool is invalid")
	}
	derived, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("oracle"), poolKey[:]},
		WhirlpoolProgram,
	)
	if err != nil {
		return "", errors.New("derive Orca oracle")
	}
	return derived, nil
}

func matchesInstruction(
	instruction solana.Instruction,
	program string,
	accounts []solana.AccountMeta,
	data []byte,
) bool {
	if instruction.Program != program || len(instruction.Accounts) != len(accounts) ||
		!bytes.Equal(instruction.Data, data) {
		return false
	}
	for index := range accounts {
		if !metaEqual(instruction.Accounts[index], accounts[index]) {
			return false
		}
	}
	return true
}

func metaEqual(left, right solana.AccountMeta) bool {
	return left == right
}

func validateATAInstruction(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	tokenAccount, mint, owner string,
) error {
	if program(message, instruction) != AssociatedTokenProgram ||
		!bytes.Equal(instruction.Data, []byte{1}) ||
		!accountsEqual(message, instruction, []string{
			owner, tokenAccount, owner, mint, SystemProgram, TokenProgram,
		}) {
		return errors.New("Orca associated-token instruction is outside policy")
	}
	return nil
}

func validateSystemTransfer(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	owner, inputTokenAccount string,
) (uint64, error) {
	if program(message, instruction) != SystemProgram ||
		!accountsEqual(message, instruction, []string{owner, inputTokenAccount}) ||
		len(instruction.Data) != 12 || binary.LittleEndian.Uint32(instruction.Data[:4]) != 2 {
		return 0, errors.New("Orca native-token funding instruction is outside policy")
	}
	amount := binary.LittleEndian.Uint64(instruction.Data[4:])
	if amount == 0 {
		return 0, errors.New("Orca native-token funding amount is zero")
	}
	return amount, nil
}

func validateTokenInstruction(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	discriminator byte,
	accounts []string,
) error {
	if program(message, instruction) != TokenProgram ||
		!bytes.Equal(instruction.Data, []byte{discriminator}) ||
		!accountsEqual(message, instruction, accounts) {
		return errors.New("Orca token instruction is outside policy")
	}
	return nil
}

func validateSwapV2(
	message solana.LegacyMessage,
	instruction solana.CompiledInstruction,
	policy Policy,
	inputAmount uint64,
) (uint64, error) {
	if program(message, instruction) != WhirlpoolProgram || len(instruction.Accounts) != 17 ||
		len(instruction.Data) != 49 || !bytes.Equal(instruction.Data[:8], swapV2Discriminator) {
		return 0, errors.New("Orca swap instruction shape is outside policy")
	}
	wantPrefix := []string{
		TokenProgram, TokenProgram, MemoProgram, policy.Owner, policy.Pool,
		policy.InputMint, policy.OutputMint, policy.InputTokenAccount, policy.TokenVaultA,
		policy.OutputTokenAccount, policy.TokenVaultB,
	}
	if !accountsPrefixEqual(message, instruction, wantPrefix) {
		return 0, errors.New("Orca swap instruction accounts are outside policy")
	}
	if account(message, instruction, 14) != policy.Oracle {
		return 0, errors.New("Orca swap oracle is outside policy")
	}
	for _, index := range [...]int{11, 12, 13, 14, 15, 16} {
		if !message.IsWritable(int(instruction.Accounts[index])) ||
			message.IsSigner(int(instruction.Accounts[index])) {
			return 0, errors.New("Orca swap dynamic account privileges are invalid")
		}
	}
	if binary.LittleEndian.Uint64(instruction.Data[8:16]) != inputAmount ||
		!bytes.Equal(instruction.Data[24:40], make([]byte, 16)) ||
		instruction.Data[40] != 1 || instruction.Data[41] != 1 ||
		!bytes.Equal(instruction.Data[42:], []byte{1, 1, 0, 0, 0, 6, 2}) {
		return 0, errors.New("Orca swap instruction data is outside policy")
	}
	minimumOutput := binary.LittleEndian.Uint64(instruction.Data[16:24])
	if minimumOutput == 0 {
		return 0, errors.New("Orca swap minimum output is zero")
	}
	return minimumOutput, nil
}

func rejectUnusedAccounts(message solana.LegacyMessage) error {
	used := make([]bool, len(message.AccountKeys))
	used[0] = true
	for _, instruction := range message.Instructions {
		used[int(instruction.ProgramIndex)] = true
		for _, index := range instruction.Accounts {
			used[int(index)] = true
		}
	}
	for _, value := range used {
		if !value {
			return errors.New("Orca swap message contains an unused account")
		}
	}
	return nil
}

func program(message solana.LegacyMessage, instruction solana.CompiledInstruction) string {
	return address(message.AccountKeys[int(instruction.ProgramIndex)])
}

func account(message solana.LegacyMessage, instruction solana.CompiledInstruction, index int) string {
	return address(message.AccountKeys[int(instruction.Accounts[index])])
}

func accountsEqual(message solana.LegacyMessage, instruction solana.CompiledInstruction, want []string) bool {
	return len(instruction.Accounts) == len(want) && accountsPrefixEqual(message, instruction, want)
}

func accountsPrefixEqual(message solana.LegacyMessage, instruction solana.CompiledInstruction, want []string) bool {
	if len(instruction.Accounts) < len(want) {
		return false
	}
	for index, expected := range want {
		if account(message, instruction, index) != expected {
			return false
		}
	}
	return true
}

func address(key [32]byte) string {
	return solana.Encode(key[:])
}
