package jupiterswap

import (
	"encoding/hex"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	maxGuardRouteAccounts  = 64
	maxRouteGuardCodeBytes = 64 << 10
)

// RouteGuardDeployment identifies an independently verified, immutable
// deployment of the Mithril route guard. ProgramData identifies the guard's
// own code deployment and exact code hash; Jupiter's pinned ProgramData is the
// account locked by each guarded transaction.
type RouteGuardDeployment struct {
	Program        string `json:"program"`
	ProgramData    string `json:"program_data"`
	DeploymentSlot uint64 `json:"deployment_slot"`
	CodeLength     uint64 `json:"code_length"`
	CodeSHA256     string `json:"code_sha256"`
}

func (deployment RouteGuardDeployment) Validate() error {
	program, programErr := solana.Decode32(deployment.Program)
	programData, programDataErr := solana.Decode32(deployment.ProgramData)
	codeHash, hashErr := hex.DecodeString(deployment.CodeSHA256)
	if programErr != nil || programDataErr != nil || program == ([32]byte{}) ||
		programData == ([32]byte{}) || program == programData ||
		deployment.Program == Program || deployment.Program == ProgramData ||
		deployment.ProgramData == Program || deployment.ProgramData == ProgramData ||
		deployment.DeploymentSlot == 0 || deployment.CodeLength == 0 ||
		deployment.CodeLength > maxRouteGuardCodeBytes || hashErr != nil || len(codeHash) != 32 ||
		hex.EncodeToString(codeHash) != deployment.CodeSHA256 {
		return errors.New("route guard deployment is invalid")
	}
	return nil
}

// WrapRoutePlan replaces the one supported direct Jupiter route with the
// immutable guard and prepends Jupiter's pinned ProgramData account. It does
// not authorize, sign, or submit the plan.
func WrapRoutePlan(
	deployment RouteGuardDeployment,
	instructions []solana.Instruction,
) ([]solana.Instruction, error) {
	if err := deployment.Validate(); err != nil {
		return nil, err
	}
	result := cloneRoutePlan(instructions)
	routeIndex := -1
	for index, instruction := range result {
		if instruction.Program == deployment.Program {
			return nil, errors.New("Jupiter plan is already guarded")
		}
		if instruction.Program == Program {
			if routeIndex >= 0 {
				return nil, errors.New("Jupiter plan contains multiple routes")
			}
			if !guardRouteShape(instruction) {
				return nil, errors.New("Jupiter route cannot be guarded")
			}
			routeIndex = index
		}
		for _, account := range instruction.Accounts {
			if account.Address == deployment.Program || account.Address == ProgramData {
				return nil, errors.New("Jupiter plan collides with the route guard")
			}
		}
	}
	if routeIndex < 0 {
		return nil, errors.New("Jupiter plan has no supported route")
	}

	route := &result[routeIndex]
	route.Program = deployment.Program
	route.Accounts = append(
		[]solana.AccountMeta{{Address: ProgramData}}, route.Accounts...,
	)
	return result, nil
}

// UnwrapRoutePlan recovers the direct Jupiter plan for the existing semantic
// validator. The guarded bytes still have to pass canonical message and policy
// validation before any signer can authorize them.
func UnwrapRoutePlan(
	deployment RouteGuardDeployment,
	instructions []solana.Instruction,
) ([]solana.Instruction, error) {
	if err := deployment.Validate(); err != nil {
		return nil, err
	}
	result := cloneRoutePlan(instructions)
	routeIndex := -1
	for index, instruction := range result {
		if instruction.Program == Program {
			return nil, errors.New("guarded Jupiter plan contains a direct route")
		}
		if instruction.Program == deployment.Program {
			if routeIndex >= 0 {
				return nil, errors.New("guarded Jupiter plan contains multiple routes")
			}
			routeIndex = index
		}
	}
	if routeIndex < 0 {
		return nil, errors.New("guarded Jupiter plan has no route")
	}

	route := &result[routeIndex]
	if len(route.Accounts) < 2 || route.Accounts[0] != (solana.AccountMeta{Address: ProgramData}) {
		return nil, errors.New("guarded Jupiter route has invalid ProgramData privileges")
	}
	route.Program = Program
	route.Accounts = append([]solana.AccountMeta(nil), route.Accounts[1:]...)
	if !guardRouteShape(*route) {
		return nil, errors.New("guarded Jupiter route is invalid")
	}
	for _, instruction := range result {
		for _, account := range instruction.Accounts {
			if account.Address == deployment.Program || account.Address == ProgramData {
				return nil, errors.New("guarded Jupiter plan collides with the route guard")
			}
		}
	}
	return result, nil
}

func guardRouteShape(instruction solana.Instruction) bool {
	fixedAccounts, ok := fixedRouteAccountCount(instruction.Data)
	if !ok || len(instruction.Accounts) < fixedAccounts ||
		len(instruction.Accounts) > maxGuardRouteAccounts {
		return false
	}
	minimumData := 35
	if fixedAccounts == 12 {
		minimumData = 36
	}
	return len(instruction.Data) >= minimumData
}

func cloneRoutePlan(instructions []solana.Instruction) []solana.Instruction {
	result := make([]solana.Instruction, len(instructions))
	for index := range instructions {
		result[index] = instructions[index]
		result[index].Accounts = append([]solana.AccountMeta(nil), instructions[index].Accounts...)
		result[index].Data = append([]byte(nil), instructions[index].Data...)
	}
	return result
}
