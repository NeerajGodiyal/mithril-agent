package programinterface

import (
	"bytes"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	testFeePayer = "SysvarC1ock11111111111111111111111111111111"
	testState    = "SysvarRent111111111111111111111111111111111"
)

func TestBuildNoArgsUsesPinnedAccountsAndDiscriminator(t *testing.T) {
	idl := bytes.Replace(testIDL(), []byte(`"args":[{"name":"amount","type":"u64"}]`), []byte(`"args":[]`), 1)
	report, err := Inspect(idl, testProgram)
	if err != nil {
		t.Fatal(err)
	}
	call, err := BuildNoArgs(report, "increment", testFeePayer, testState, []Binding{{
		Name: "state", Address: testState,
	}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := solana.DecodeV0Message(call.Message, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Instructions) != 1 ||
		!bytes.Equal(message.Instructions[0].Data, []byte{11, 18, 104, 9, 104, 174, 59, 33}) ||
		len(call.Transaction) == 0 {
		t.Fatalf("built call = %+v", call)
	}
}

func TestBuildNoArgsRejectsUnpinnedShape(t *testing.T) {
	idl := bytes.Replace(testIDL(), []byte(`"args":[{"name":"amount","type":"u64"}]`), []byte(`"args":[]`), 1)
	report, err := Inspect(idl, testProgram)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Report, *[]Binding){
		"instruction has arguments": func(report *Report, _ *[]Binding) {
			report.Instructions[0].Args = []Argument{{Name: "amount", Type: []byte(`"u64"`)}}
		},
		"missing binding": func(_ *Report, bindings *[]Binding) {
			*bindings = nil
		},
		"wrong binding name": func(_ *Report, bindings *[]Binding) {
			(*bindings)[0].Name = "other"
		},
		"additional signer": func(report *Report, _ *[]Binding) {
			report.Instructions[0].Accounts[0].Signer = true
		},
		"fixed address mismatch": func(report *Report, _ *[]Binding) {
			report.Instructions[0].Accounts[0].Address = testFeePayer
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := report
			candidate.Instructions = append([]Instruction(nil), report.Instructions...)
			candidate.Instructions[0].Accounts = append([]Account(nil), report.Instructions[0].Accounts...)
			bindings := []Binding{{Name: "state", Address: testState}}
			mutate(&candidate, &bindings)
			if _, err := BuildNoArgs(candidate, "increment", testFeePayer, testState, bindings); err == nil {
				t.Fatal("invalid call was accepted")
			}
		})
	}
}
