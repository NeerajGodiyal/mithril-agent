package jupiterswap

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	ProfileName             = "jupiter_mainnet_guarded_exact_in_v7"
	ProfileVersion          = uint32(7)
	RequestDomain           = "mithril-agent/mainnet-jupiter-guarded-exact-in-v7"
	policyFingerprintDomain = "mithril-agent/jupiter-mainnet-policy-v7"
	actionIDDomain          = "mithril-agent/jupiter-mainnet/action-id-v1"
)

type deploymentIdentity struct {
	Program          string `json:"program"`
	ProgramData      string `json:"program_data"`
	UpgradeAuthority string `json:"upgrade_authority"`
	DeploymentSlot   uint64 `json:"deployment_slot"`
}

// Policy is the signer-independent allowlist for one Mainnet exact-in route.
// Values are base units; the operator must choose them before quoting.
type Policy struct {
	Owner                           string               `json:"owner"`
	InputMint                       string               `json:"input_mint"`
	OutputMint                      string               `json:"output_mint"`
	MaxInputAmount                  uint64               `json:"max_input_amount"`
	MinOutputAmount                 uint64               `json:"min_output_amount"`
	MaxSlippageBPS                  uint16               `json:"max_slippage_bps"`
	MaxComputeUnits                 uint32               `json:"max_compute_units"`
	MaxComputeUnitPriceMicroLamport uint64               `json:"max_compute_unit_price_micro_lamports"`
	MaxFeeLamports                  uint64               `json:"max_fee_lamports"`
	MaxTokenAccountRentLamports     uint64               `json:"max_token_account_rent_lamports"`
	RouteGuard                      RouteGuardDeployment `json:"route_guard"`
}

func (p Policy) NativeInput() bool  { return p.InputMint == orcaswap.WrappedSOLMint }
func (p Policy) NativeOutput() bool { return p.OutputMint == orcaswap.WrappedSOLMint }

// Fingerprint binds every route and limit field plus the reviewed Jupiter
// deployment into the signer profile.
func (p Policy) Fingerprint() (string, error) {
	return p.fingerprint(deploymentIdentity{
		Program:          Program,
		ProgramData:      ProgramData,
		UpgradeAuthority: UpgradeAuthority,
		DeploymentSlot:   DeploymentSlot,
	})
}

func (p Policy) fingerprint(deployment deploymentIdentity) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	program, programErr := solana.Decode32(deployment.Program)
	programData, programDataErr := solana.Decode32(deployment.ProgramData)
	upgradeAuthority, upgradeAuthorityErr := solana.Decode32(deployment.UpgradeAuthority)
	if programErr != nil || programDataErr != nil || upgradeAuthorityErr != nil ||
		program == programData || program == upgradeAuthority || programData == upgradeAuthority ||
		deployment.DeploymentSlot == 0 {
		return "", errors.New("Jupiter deployment identity is invalid")
	}
	encoded, err := json.Marshal(struct {
		Policy     Policy             `json:"policy"`
		Deployment deploymentIdentity `json:"deployment"`
	}{p, deployment})
	if err != nil {
		return "", errors.New("encode Jupiter policy fingerprint")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(policyFingerprintDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ComputeActionID permits one Jupiter action per configured schedule window.
func ComputeActionID(profileFingerprint string, scheduleWindowStartUnix int64) (string, error) {
	decoded, err := hex.DecodeString(profileFingerprint)
	if err != nil || len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != profileFingerprint || scheduleWindowStartUnix <= 0 {
		return "", errors.New("Jupiter action identity is invalid")
	}
	encoded, err := json.Marshal(struct {
		Domain                  string `json:"domain"`
		ProfileFingerprint      string `json:"profile_sha256"`
		ScheduleWindowStartUnix int64  `json:"schedule_window_start_unix"`
	}{actionIDDomain, profileFingerprint, scheduleWindowStartUnix})
	if err != nil {
		return "", errors.New("encode Jupiter action identity")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (p Policy) Validate() error {
	if err := p.validateTrade(); err != nil {
		return err
	}
	return p.RouteGuard.Validate()
}

func (p Policy) validateTrade() error {
	owner, ownerErr := solana.Decode32(p.Owner)
	input, inputErr := solana.Decode32(p.InputMint)
	output, outputErr := solana.Decode32(p.OutputMint)
	if ownerErr != nil || outputErr != nil || inputErr != nil || owner == input || owner == output ||
		input == output || p.NativeInput() == p.NativeOutput() {
		return errors.New("Jupiter policy addresses are invalid")
	}
	if p.MaxInputAmount == 0 || p.MinOutputAmount == 0 ||
		p.MaxSlippageBPS == 0 || p.MaxSlippageBPS > 500 || p.MaxComputeUnits == 0 ||
		p.MaxComputeUnits > solana.MaxComputeUnitLimit ||
		p.MaxComputeUnitPriceMicroLamport == 0 || p.MaxFeeLamports == 0 ||
		p.MaxTokenAccountRentLamports == 0 || p.MaxTokenAccountRentLamports > 10_000_000 {
		return errors.New("Jupiter policy limits are invalid")
	}
	return nil
}

// ValidateProposal applies the operator's limits to the untrusted Jupiter
// response before the message is compiled or simulated.
func ValidateProposal(
	policy Policy,
	request jupiterquote.Request,
	quote jupiterquote.Result,
	computeBudget []solana.Instruction,
	instructions []solana.Instruction,
) (Intent, error) {
	if err := policy.validateTrade(); err != nil {
		return Intent{}, err
	}
	if request.Taker != policy.Owner || request.InputMint != policy.InputMint ||
		request.OutputMint != policy.OutputMint || request.InputAmount == 0 ||
		request.InputAmount > policy.MaxInputAmount || request.SlippageBPS == 0 ||
		request.SlippageBPS > policy.MaxSlippageBPS || quote.MinimumOutput < policy.MinOutputAmount {
		return Intent{}, errors.New("Jupiter proposal is outside policy")
	}
	if policy.NativeInput() {
		outputAccount, err := orcaswap.AssociatedTokenAddress(policy.Owner, policy.OutputMint)
		if err != nil || request.DestinationTokenAccount != outputAccount {
			return Intent{}, errors.New("Jupiter proposal does not use the pre-created canonical output account")
		}
	} else if request.DestinationTokenAccount != "" {
		return Intent{}, errors.New("Jupiter native output must return to the protected wallet")
	}
	if len(computeBudget) != 1 || computeBudget[0].Program != solana.ComputeBudgetProgram ||
		len(computeBudget[0].Accounts) != 0 || len(computeBudget[0].Data) != 9 ||
		computeBudget[0].Data[0] != 3 {
		return Intent{}, errors.New("Jupiter compute-unit price is outside policy")
	}
	price := binary.LittleEndian.Uint64(computeBudget[0].Data[1:])
	if price == 0 || price > policy.MaxComputeUnitPriceMicroLamport {
		return Intent{}, errors.New("Jupiter compute-unit price is outside policy")
	}
	var (
		intent Intent
		err    error
	)
	if policy.NativeInput() {
		intent, err = ValidateExactInSOL(request, quote, instructions)
	} else {
		intent, err = validateExactInTokenToSOL(request, quote, instructions)
	}
	if err != nil {
		return Intent{}, err
	}
	if policy.NativeInput() && intent.OutputAccountCreated {
		return Intent{}, errors.New("Jupiter proposal attempts to create the protected output account")
	}
	return intent, nil
}
