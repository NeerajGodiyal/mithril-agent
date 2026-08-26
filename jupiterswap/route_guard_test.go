package jupiterswap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestRouteGuardPinsCurrentJupiterDeployment(t *testing.T) {
	source, err := os.ReadFile("../programs/mithril-route-guard/src/lib.rs")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`JUPITER_DEPLOYMENT_SLOT: u64 = ([0-9_]+);`).FindSubmatch(source)
	if len(match) != 2 {
		t.Fatal("route guard deployment pin is missing")
	}
	slot, err := strconv.ParseUint(strings.ReplaceAll(string(match[1]), "_", ""), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if slot != DeploymentSlot {
		t.Fatalf("route guard deployment slot = %d, Go policy = %d", slot, DeploymentSlot)
	}
}

func TestRouteGuardPlanRoundTrip(t *testing.T) {
	request, quote, direct := exactInSOLFixture(t)
	inputAccount := direct[3].Accounts[1].Address
	outputAccount := direct[3].Accounts[2].Address
	shared := cloneInstructions(direct)
	shared[3] = planSharedAccountsRouteV2Fixture(
		t, request, quote, inputAccount, outputAccount,
	)

	for name, plan := range map[string][]solana.Instruction{
		"route_v2":                 direct,
		"shared_accounts_route_v2": shared,
	} {
		t.Run(name, func(t *testing.T) {
			original := cloneInstructions(plan)
			guarded, err := WrapRoutePlan(routeGuardFixture(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan, original) {
				t.Fatal("wrapping mutated the source plan")
			}
			if guarded[3].Program != routeGuardFixture().Program ||
				guarded[3].Accounts[0] != (solana.AccountMeta{Address: ProgramData}) {
				t.Fatalf("guarded route = %+v", guarded[3])
			}
			unwrapped, err := UnwrapRoutePlan(routeGuardFixture(), guarded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(unwrapped, original) {
				t.Fatal("guarded route did not round trip")
			}
		})
	}
}

func TestRouteGuardPlanRejectsDrift(t *testing.T) {
	_, _, plan := exactInSOLFixture(t)
	deployment := routeGuardFixture()

	tests := map[string]func([]solana.Instruction, *RouteGuardDeployment) []solana.Instruction{
		"invalid deployment": func(value []solana.Instruction, deployment *RouteGuardDeployment) []solana.Instruction {
			deployment.ProgramData = deployment.Program
			return value
		},
		"invalid code identity": func(value []solana.Instruction, deployment *RouteGuardDeployment) []solana.Instruction {
			deployment.CodeSHA256 = "invalid"
			return value
		},
		"missing route": func(value []solana.Instruction, _ *RouteGuardDeployment) []solana.Instruction {
			value[3].Program = solana.ComputeBudgetProgram
			return value
		},
		"multiple routes": func(value []solana.Instruction, _ *RouteGuardDeployment) []solana.Instruction {
			return append(value, value[3])
		},
		"unsupported route": func(value []solana.Instruction, _ *RouteGuardDeployment) []solana.Instruction {
			value[3].Data[0]++
			return value
		},
		"oversized route": func(value []solana.Instruction, _ *RouteGuardDeployment) []solana.Instruction {
			for len(value[3].Accounts) <= maxGuardRouteAccounts {
				value[3].Accounts = append(value[3].Accounts, solana.AccountMeta{
					Address: solana.Encode(bytes.Repeat([]byte{byte(len(value[3].Accounts))}, 32)),
				})
			}
			return value
		},
		"guard collision": func(value []solana.Instruction, deployment *RouteGuardDeployment) []solana.Instruction {
			value[0].Accounts[0].Address = deployment.Program
			return value
		},
		"program data collision": func(value []solana.Instruction, _ *RouteGuardDeployment) []solana.Instruction {
			value[0].Accounts[0].Address = ProgramData
			return value
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneInstructions(plan)
			configured := deployment
			candidate = mutate(candidate, &configured)
			if _, err := WrapRoutePlan(configured, candidate); err == nil {
				t.Fatal("accepted invalid guarded route")
			}
		})
	}
}

func TestUnwrapRoutePlanRejectsGuardDrift(t *testing.T) {
	_, _, plan := exactInSOLFixture(t)
	deployment := routeGuardFixture()
	guarded, err := WrapRoutePlan(deployment, plan)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func([]solana.Instruction){
		"writable ProgramData": func(value []solana.Instruction) {
			value[3].Accounts[0].Writable = true
		},
		"wrong ProgramData": func(value []solana.Instruction) {
			value[3].Accounts[0].Address = deployment.ProgramData
		},
		"direct route": func(value []solana.Instruction) {
			value[0].Program = Program
		},
		"multiple guarded routes": func(value []solana.Instruction) {
			value[0] = value[3]
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneInstructions(guarded)
			mutate(candidate)
			if _, err := UnwrapRoutePlan(deployment, candidate); err == nil {
				t.Fatal("accepted invalid guarded route")
			}
		})
	}
}

func routeGuardFixture() RouteGuardDeployment {
	code := []byte("guard")
	hash := sha256.Sum256(code)
	return RouteGuardDeployment{
		Program:        solana.Encode(bytes.Repeat([]byte{71}, 32)),
		ProgramData:    solana.Encode(bytes.Repeat([]byte{72}, 32)),
		DeploymentSlot: 123,
		CodeLength:     uint64(len(code)),
		CodeSHA256:     hex.EncodeToString(hash[:]),
	}
}
