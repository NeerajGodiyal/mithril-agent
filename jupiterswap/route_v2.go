// Package jupiterswap validates the fixed security-relevant prefix of current
// Jupiter Swap V2 instructions. It deliberately supports only ExactIn route_v2
// and shared_accounts_route_v2.
package jupiterswap

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	// Program is Jupiter's Aggregator V6 program address.
	Program = "JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4"
	// ProgramData, UpgradeAuthority, and DeploymentSlot pin the current
	// upgradeable deployment. A Jupiter upgrade must be reviewed before these
	// values are changed.
	ProgramData      = "4Ec7ZxZS6Sbdg5UGSLHbAnM7GQHp2eFd4KYWRexAipQT"
	UpgradeAuthority = "CvQZZ23qYDWF2RUpxYJ8y9K4skmuvYEEjH7fK58jtipQ"
	DeploymentSlot   = uint64(441316428)

	eventAuthority = "D8cy77BBepLMngZx6ZukaTff5hCt1HrWyKk3Hnd9oitf"
	tokenProgram   = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	authorityCount = 16
)

var (
	routeV2Discriminator               = [8]byte{187, 100, 250, 204, 49, 196, 175, 20}
	sharedAccountsRouteV2Discriminator = [8]byte{209, 152, 83, 147, 124, 254, 216, 233}
)

// Intent is the bounded value movement encoded by one ExactIn route_v2.
type Intent struct {
	SourceTokenAccount      string
	DestinationTokenAccount string
	InputAmount             uint64
	QuotedOutput            uint64
	MinimumOutput           uint64
	SlippageBPS             uint16
	OutputAccountCreated    bool
}

// ValidateRouteV2 validates the fixed accounts and amount header documented by
// Jupiter's current aggregator IDL. The remaining route plan is executed only
// by the pinned Jupiter program and must still pass exact Mithril simulation.
func ValidateRouteV2(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	instruction solana.Instruction,
) (Intent, error) {
	if instruction.Program != Program || len(instruction.Data) < 8 {
		return Intent{}, errors.New("Jupiter route_v2 instruction shape is invalid")
	}
	switch {
	case bytes.Equal(instruction.Data[:8], routeV2Discriminator[:]):
		return validateRouteV2(request, quote, instruction)
	case bytes.Equal(instruction.Data[:8], sharedAccountsRouteV2Discriminator[:]):
		return validateSharedAccountsRouteV2(request, quote, instruction)
	default:
		return Intent{}, errors.New("Jupiter route_v2 instruction shape is invalid")
	}
}

func validateRouteV2(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	instruction solana.Instruction,
) (Intent, error) {
	if len(instruction.Accounts) < 10 || len(instruction.Data) <= 34 {
		return Intent{}, errors.New("Jupiter route_v2 instruction shape is invalid")
	}
	destination := solana.AccountMeta{Address: Program}
	if request.DestinationTokenAccount != "" {
		destination = solana.AccountMeta{
			Address: request.DestinationTokenAccount, Writable: true,
		}
	}
	wantAccounts := []solana.AccountMeta{
		{Address: request.Taker, Signer: true},
		{Address: instruction.Accounts[1].Address, Writable: true},
		{Address: instruction.Accounts[2].Address, Writable: true},
		{Address: request.InputMint},
		{Address: request.OutputMint},
		{Address: tokenProgram},
		{Address: tokenProgram},
		destination,
		{Address: eventAuthority},
		{Address: Program},
	}
	for index, want := range wantAccounts {
		if instruction.Accounts[index] != want {
			return Intent{}, errors.New("Jupiter route_v2 fixed accounts are invalid")
		}
	}
	if instruction.Accounts[1].Address == instruction.Accounts[2].Address {
		return Intent{}, errors.New("Jupiter route_v2 token accounts must differ")
	}
	if hasSigner(instruction.Accounts[10:]) {
		return Intent{}, errors.New("Jupiter route_v2 remaining accounts include an additional signer")
	}
	input, quoted, slippage, err := validateRouteAmounts(request, quote, instruction.Data[8:])
	if err != nil {
		return Intent{}, err
	}
	return Intent{
		SourceTokenAccount:      instruction.Accounts[1].Address,
		DestinationTokenAccount: instruction.Accounts[2].Address,
		InputAmount:             input, QuotedOutput: quoted, MinimumOutput: quote.MinimumOutput,
		SlippageBPS: slippage,
	}, nil
}

func validateSharedAccountsRouteV2(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	instruction solana.Instruction,
) (Intent, error) {
	if len(instruction.Accounts) < 12 || len(instruction.Data) <= 35 ||
		instruction.Data[8] >= authorityCount {
		return Intent{}, errors.New("Jupiter shared route_v2 instruction shape is invalid")
	}
	authority, _, err := solana.FindProgramAddress(
		[][]byte{[]byte("authority"), {instruction.Data[8]}}, Program,
	)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter shared route_v2 authority")
	}
	programSource, err := orcaswap.AssociatedTokenAddress(authority, request.InputMint)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter shared route_v2 source")
	}
	programDestination, err := orcaswap.AssociatedTokenAddress(authority, request.OutputMint)
	if err != nil {
		return Intent{}, errors.New("derive Jupiter shared route_v2 destination")
	}
	wantAccounts := []solana.AccountMeta{
		{Address: authority},
		{Address: request.Taker, Signer: true},
		{Address: instruction.Accounts[2].Address, Writable: true},
		{Address: programSource, Writable: true},
		{Address: programDestination, Writable: true},
		{Address: instruction.Accounts[5].Address, Writable: true},
		{Address: request.InputMint},
		{Address: request.OutputMint},
		{Address: tokenProgram},
		{Address: tokenProgram},
		{Address: eventAuthority},
		{Address: Program},
	}
	for index, want := range wantAccounts {
		if instruction.Accounts[index] != want {
			return Intent{}, errors.New("Jupiter shared route_v2 fixed accounts are invalid")
		}
	}
	if instruction.Accounts[2].Address == instruction.Accounts[5].Address ||
		instruction.Accounts[3].Address == instruction.Accounts[4].Address ||
		hasSigner(instruction.Accounts[12:]) {
		return Intent{}, errors.New("Jupiter shared route_v2 token accounts are invalid")
	}
	input, quoted, slippage, err := validateRouteAmounts(request, quote, instruction.Data[9:])
	if err != nil {
		return Intent{}, err
	}
	return Intent{
		SourceTokenAccount:      instruction.Accounts[2].Address,
		DestinationTokenAccount: instruction.Accounts[5].Address,
		InputAmount:             input, QuotedOutput: quoted, MinimumOutput: quote.MinimumOutput,
		SlippageBPS: slippage,
	}, nil
}

func validateRouteAmounts(
	request jupiterquote.Request,
	quote jupiterquote.Result,
	data []byte,
) (uint64, uint64, uint16, error) {
	if len(data) <= 26 {
		return 0, 0, 0, errors.New("Jupiter route_v2 instruction shape is invalid")
	}
	input := binary.LittleEndian.Uint64(data[:8])
	quoted := binary.LittleEndian.Uint64(data[8:16])
	slippage := binary.LittleEndian.Uint16(data[16:18])
	platformFee := binary.LittleEndian.Uint16(data[18:20])
	positiveSlippage := binary.LittleEndian.Uint16(data[20:22])
	routeSteps := binary.LittleEndian.Uint32(data[22:26])
	if input == 0 || input != request.InputAmount || input != quote.InputAmount ||
		quoted == 0 || quoted != quote.EstimatedOutput || slippage != request.SlippageBPS ||
		platformFee != 0 || positiveSlippage != 0 || routeSteps == 0 || routeSteps > 64 {
		return 0, 0, 0, errors.New("Jupiter route_v2 amounts or limits are invalid")
	}
	return input, quoted, slippage, nil
}

func hasSigner(accounts []solana.AccountMeta) bool {
	for _, account := range accounts {
		if account.Signer {
			return true
		}
	}
	return false
}
