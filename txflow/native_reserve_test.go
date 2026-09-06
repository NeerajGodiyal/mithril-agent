package txflow

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestVerifyNativeReserveBoundaries(t *testing.T) {
	owner := solana.Encode(bytes.Repeat([]byte{8}, 32))
	for _, name := range []string{"equal", "spare", "insufficient", "underflow", "overflow", "zero reserve", "zero upfront",
		"disagreement", "stale primary", "stale secondary", "ahead", "wrong owner", "executable", "data", "RPC error", "bad address", "zero slot", "inverted slots"} {
		t.Run(name, func(t *testing.T) {
			primary := &fakeProvider{identity: "primary", balance: 1000, balanceSlot: 112}
			secondary := &fakeProvider{identity: "secondary", balance: 1000, balanceSlot: 113}
			node := &fakeProvider{identity: "node"}
			upfront, reserve, minimum, maximum := uint64(600), uint64(400), uint64(112), uint64(250)
			address := owner
			switch name {
			case "spare":
				primary.balance++
				secondary.balance++
			case "insufficient":
				primary.balance--
				secondary.balance--
			case "underflow":
				reserve = 1001
			case "overflow":
				primary.balance = ^uint64(0)
				secondary.balance = primary.balance
				upfront = ^uint64(0)
			case "zero reserve":
				reserve = 0
			case "zero upfront":
				upfront = 0
			case "disagreement":
				secondary.balance--
			case "stale primary":
				primary.balanceSlot = 111
			case "stale secondary":
				secondary.balanceSlot = 111
			case "ahead":
				secondary.balanceSlot = 251
			case "wrong owner":
				primary.accountOwner = owner
				secondary.accountOwner = owner
			case "executable":
				primary.accountExecutable = true
				secondary.accountExecutable = true
			case "data":
				primary.accountDataLength = 1
				secondary.accountDataLength = 1
			case "RPC error":
				primary.balanceErr = errors.New("offline injected failure")
			case "bad address":
				address = "bad"
			case "zero slot":
				minimum = 0
			case "inverted slots":
				maximum = minimum - 1
			}
			lifecycle, err := New(node, primary, secondary)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lifecycle.VerifyNativeReserve(t.Context(), address, upfront, reserve, minimum, maximum)
			if name == "equal" || name == "spare" {
				if err != nil || got.Address != owner || got.PrimaryLamports != primary.balance {
					t.Fatalf("valid reserve rejected: %+v, %v", got, err)
				}
			} else if err == nil || got != (AccountEvidence{}) {
				t.Fatalf("invalid reserve accepted: %+v, %v", got, err)
			}
			if node.sendCalls != 0 || primary.sendCalls != 0 || secondary.sendCalls != 0 {
				t.Fatal("balance check submitted a transaction")
			}
		})
	}
}
