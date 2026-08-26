package programinterface

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEncodeArgumentsUsesPinnedBorshTypesAndIDLOrder(t *testing.T) {
	instruction := Instruction{Args: []Argument{
		{Name: "amount", Type: json.RawMessage(`"u64"`)},
		{Name: "owner", Type: json.RawMessage(`"pubkey"`)},
		{Name: "memo", Type: json.RawMessage(`{"option":"string"}`)},
		{Name: "weights", Type: json.RawMessage(`{"vec":"u16"}`)},
	}}
	encoded, err := EncodeArguments(instruction, nil, []ArgumentBinding{
		{Name: "weights", Value: json.RawMessage(`[1,513]`)},
		{Name: "memo", Value: json.RawMessage(`"ok"`)},
		{Name: "amount", Value: json.RawMessage(`"18446744073709551615"`)},
		{Name: "owner", Value: json.RawMessage(`"11111111111111111111111111111111"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Repeat([]byte{0xff}, 8), make([]byte, 32)...)
	want = append(want, 1, 2, 0, 0, 0, 'o', 'k')
	want = append(want, 2, 0, 0, 0, 1, 0, 1, 2)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded = %v, want %v", encoded, want)
	}
}

func TestEncodeArgumentsRejectsUnsupportedAndAmbiguousValues(t *testing.T) {
	for name, argument := range map[string]Argument{
		"defined": {Name: "value", Type: json.RawMessage(`{"defined":{"name":"Thing"}}`)},
		"range":   {Name: "value", Type: json.RawMessage(`"u8"`)},
	} {
		t.Run(name, func(t *testing.T) {
			value := json.RawMessage(`1`)
			if name == "range" {
				value = json.RawMessage(`256`)
			}
			if _, err := EncodeArguments(
				Instruction{Args: []Argument{argument}},
				nil,
				[]ArgumentBinding{{Name: "value", Value: value}},
			); err == nil {
				t.Fatal("unsupported or out-of-range argument was accepted")
			}
		})
	}
	if _, err := EncodeArguments(
		Instruction{Args: []Argument{{Name: "value", Type: json.RawMessage(`"u8"`)}}},
		nil,
		[]ArgumentBinding{{Name: "value", Value: json.RawMessage(`{"x":1,"x":2}`)}},
	); err == nil {
		t.Fatal("ambiguous JSON argument was accepted")
	}
	for _, argument := range []Argument{
		{Name: "value", Type: json.RawMessage(`"string"`)},
		{Name: "value", Type: json.RawMessage(`"bytes"`)},
		{Name: "value", Type: json.RawMessage(`{"vec":"u8"}`)},
	} {
		if _, err := EncodeArguments(
			Instruction{Args: []Argument{argument}},
			nil,
			[]ArgumentBinding{{Name: "value", Value: json.RawMessage(`null`)}},
		); err == nil {
			t.Fatalf("null was accepted for %s", argument.Type)
		}
	}
}

func TestEncodeArgumentsSupportsPinnedStructEnumAndAlias(t *testing.T) {
	types := []TypeDefinition{
		{Name: "Amount", Type: json.RawMessage(`{"kind":"type","alias":"u64"}`)},
		{Name: "Mode", Type: json.RawMessage(`{"kind":"enum","variants":[{"name":"Disabled"},{"name":"Fixed","fields":[{"name":"level","type":"u16"}]}]}`)},
		{Name: "Config", Type: json.RawMessage(`{"kind":"struct","fields":[{"name":"amount","type":{"defined":{"name":"Amount"}}},{"name":"mode","type":{"defined":{"name":"Mode"}}}]}`)},
	}
	instruction := Instruction{Args: []Argument{{
		Name: "config", Type: json.RawMessage(`{"defined":{"name":"Config"}}`),
	}}}
	encoded, err := EncodeArguments(instruction, types, []ArgumentBinding{{
		Name: "config", Value: json.RawMessage(`{"mode":{"Fixed":{"level":7}},"amount":513}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 2, 0, 0, 0, 0, 0, 0, 1, 7, 0}; !bytes.Equal(encoded, want) {
		t.Fatalf("defined encoding = %v, want %v", encoded, want)
	}
	if _, err := EncodeArguments(instruction, types, []ArgumentBinding{{
		Name: "config", Value: json.RawMessage(`{"amount":1,"mode":{"Disabled":null},"extra":true}`),
	}}); err == nil {
		t.Fatal("unknown struct field was accepted")
	}
}

func TestEncodeArgumentsSupportsTupleStruct(t *testing.T) {
	types := []TypeDefinition{{
		Name: "Pair", Type: json.RawMessage(`{"kind":"struct","fields":["u16","bool"]}`),
	}}
	instruction := Instruction{Args: []Argument{{
		Name: "pair", Type: json.RawMessage(`{"defined":{"name":"Pair"}}`),
	}}}
	encoded, err := EncodeArguments(instruction, types, []ArgumentBinding{{
		Name: "pair", Value: json.RawMessage(`[513,true]`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 2, 1}; !bytes.Equal(encoded, want) {
		t.Fatalf("tuple encoding = %v, want %v", encoded, want)
	}
}

func TestEncodeArgumentsRejectsAmbiguousDefinedNames(t *testing.T) {
	for name, definition := range map[string]TypeDefinition{
		"fields": {
			Name: "Value", Type: json.RawMessage(`{"kind":"struct","fields":[{"name":"value","type":"u8"},{"name":"Value","type":"u8"}]}`),
		},
		"variants": {
			Name: "Value", Type: json.RawMessage(`{"kind":"enum","variants":[{"name":"On"},{"name":"on"}]}`),
		},
		"empty fields": {
			Name: "Value", Type: json.RawMessage(`{"kind":"struct","fields":[]}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			instruction := Instruction{Args: []Argument{{
				Name: "value", Type: json.RawMessage(`{"defined":{"name":"Value"}}`),
			}}}
			if _, err := EncodeArguments(instruction, []TypeDefinition{definition}, []ArgumentBinding{{
				Name: "value", Value: json.RawMessage(`{"value":1,"Value":2}`),
			}}); err == nil {
				t.Fatal("ambiguous defined type was accepted")
			}
		})
	}
}
