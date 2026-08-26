package programinterface

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func decoderIDL() []byte {
	return []byte(`{
  "address":"11111111111111111111111111111111",
  "metadata":{"name":"decoder","version":"0.1.0","spec":"0.1.0"},
  "instructions":[{
    "name":"set","discriminator":[9,8,7,6,5,4,3,2],"accounts":[],
    "args":[{"name":"count","type":"u64"},{"name":"choice","type":{"defined":{"name":"Choice"}}}]
  }],
  "accounts":[{"name":"State","discriminator":[1,2,3,4,5,6,7,8]}],
  "types":[
    {"name":"State","type":{"kind":"struct","fields":[
      {"name":"count","type":"u64"},
      {"name":"owner","type":"pubkey"},
      {"name":"note","type":{"option":"string"}},
      {"name":"values","type":{"vec":"i16"}},
      {"name":"choice","type":{"defined":{"name":"Choice"}}}
    ]}},
    {"name":"Choice","type":{"kind":"enum","variants":[
      {"name":"Empty"},
      {"name":"Pair","fields":["u8","u128"]}
    ]}}
  ]
}`)
}

func TestDecodeInstructionUsesPinnedDiscriminatorAndArguments(t *testing.T) {
	report, err := Inspect(decoderIDL(), testProgram)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{9, 8, 7, 6, 5, 4, 3, 2}
	data = binary.LittleEndian.AppendUint64(data, 42)
	data = append(data, 0)
	decoded, err := DecodeInstruction(report, "set", data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"count": "42", "choice": map[string]any{"Empty": nil}}
	if decoded.Kind != "instruction" || decoded.Name != "set" ||
		decoded.Bytes != len(data) || len(decoded.SHA256) != 64 ||
		!reflect.DeepEqual(decoded.Value, want) {
		t.Fatalf("decoded = %#v", decoded)
	}
	for name, invalid := range map[string][]byte{
		"wrong discriminator": {1, 2, 3, 4, 5, 6, 7, 8},
		"trailing bytes":      append(append([]byte(nil), data...), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeInstruction(report, "set", invalid); err == nil {
				t.Fatal("invalid instruction data was accepted")
			}
		})
	}
}

func TestDecodeAccountUsesPinnedDiscriminatorAndBorshTypes(t *testing.T) {
	report, err := Inspect(decoderIDL(), testProgram)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	data = binary.LittleEndian.AppendUint64(data, 42)
	data = append(data, make([]byte, 32)...)
	data = append(data, 1)
	data = binary.LittleEndian.AppendUint32(data, 3)
	data = append(data, "hey"...)
	data = binary.LittleEndian.AppendUint32(data, 2)
	data = append(data, 0xfe, 0xff, 0x07, 0x00)
	data = append(data, 1, 9)
	large := make([]byte, 16)
	large[8] = 1
	data = append(data, large...)

	decoded, err := DecodeAccount(report, "State", data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"count":  "42",
		"owner":  testProgram,
		"note":   "hey",
		"values": []any{int64(-2), int64(7)},
		"choice": map[string]any{"Pair": []any{uint64(9), "18446744073709551616"}},
	}
	if decoded.Kind != "account" || decoded.Name != "State" || decoded.Bytes != len(data) ||
		len(decoded.SHA256) != 64 || !reflect.DeepEqual(decoded.Value, want) {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestDecodeAccountFailsClosed(t *testing.T) {
	report, err := Inspect(decoderIDL(), testProgram)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"wrong discriminator": {8, 7, 6, 5, 4, 3, 2, 1},
		"truncated":           {1, 2, 3, 4, 5, 6, 7, 8},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAccount(report, "State", data); err == nil {
				t.Fatal("invalid account data was accepted")
			}
		})
	}
	if _, err := DecodeAccount(report, "Missing", nil); err == nil || !strings.Contains(err.Error(), "exact name") {
		t.Fatalf("missing account = %v", err)
	}
}
