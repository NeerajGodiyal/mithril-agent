package programinterface

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

func testCodamaIDL() []byte {
	return []byte(`{
  "kind":"rootNode","standard":"codama","version":"1.0.0",
  "program":{
    "kind":"programNode","name":"counter","publicKey":"11111111111111111111111111111111","version":"1.0.0",
    "definedTypes":[{
      "kind":"definedTypeNode","name":"amountValue",
      "type":{"kind":"numberTypeNode","format":"u64","endian":"le"}
    }],
    "accounts":[{
      "kind":"accountNode","name":"Counter",
      "data":{"kind":"structTypeNode","fields":[
        {"kind":"structFieldTypeNode","name":"value","type":{"kind":"numberTypeNode","format":"u64","endian":"le"}}
      ]},
      "discriminators":[{
        "kind":"constantDiscriminatorNode","offset":0,
        "constant":{"kind":"constantValueNode","type":{"kind":"bytesTypeNode"},"value":{"kind":"bytesValueNode","encoding":"base16","data":"01"}}
      }]
    }],
    "events":[{
      "kind":"eventNode","name":"Changed",
      "data":{"kind":"structTypeNode","fields":[
        {"kind":"structFieldTypeNode","name":"message","type":{"kind":"fixedSizeTypeNode","size":4,"type":{"kind":"stringTypeNode","encoding":"utf8"}}}
      ]},
      "discriminators":[{
        "kind":"constantDiscriminatorNode","offset":0,
        "constant":{"kind":"constantValueNode","type":{"kind":"bytesTypeNode"},"value":{"kind":"bytesValueNode","encoding":"base16","data":"aabb"}}
      }]
    }],
    "instructions":[{
      "kind":"instructionNode","name":"increment",
      "accounts":[
        {"kind":"instructionAccountNode","name":"state","isWritable":true,"isSigner":false,"isOptional":false},
        {"kind":"instructionAccountNode","name":"systemProgram","isWritable":false,"isSigner":false,"isOptional":false,"defaultValue":{"kind":"publicKeyValueNode","publicKey":"11111111111111111111111111111111"}}
      ],
      "arguments":[
        {"kind":"instructionArgumentNode","name":"discriminator","type":{"kind":"numberTypeNode","format":"u8","endian":"le"},"defaultValue":{"kind":"numberValueNode","number":7},"defaultValueStrategy":"omitted"},
        {"kind":"instructionArgumentNode","name":"amount","type":{"kind":"definedTypeLinkNode","name":"amountValue"}},
        {"kind":"instructionArgumentNode","name":"note","type":{"kind":"fixedSizeTypeNode","size":4,"type":{"kind":"stringTypeNode","encoding":"utf8"}}}
      ],
      "discriminators":[{"kind":"fieldDiscriminatorNode","name":"discriminator","offset":0}]
    }]
  }
}`)
}

func TestInspectBuildsAndDecodesCurrentCodamaV1(t *testing.T) {
	report, err := Inspect(testCodamaIDL(), testProgram)
	if err != nil {
		t.Fatal(err)
	}
	if report.Spec != "codama/1.0.0" || report.Name != "counter" || report.Version != "1.0.0" {
		t.Fatalf("unexpected report metadata: %+v", report)
	}
	if len(report.Instructions) != 1 || report.Instructions[0].Discriminator != "07" ||
		len(report.Instructions[0].Args) != 2 || report.Instructions[0].Args[0].Name != "amount" ||
		len(report.Instructions[0].Accounts) != 2 || report.Instructions[0].Accounts[1].Address != testProgram {
		t.Fatalf("unexpected Codama instruction: %+v", report.Instructions)
	}

	encoded, err := EncodeArguments(report.Instructions[0], report.Types, []ArgumentBinding{
		{Name: "amount", Value: json.RawMessage(`9`)},
		{Name: "note", Value: json.RawMessage(`"ok"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := binary.LittleEndian.AppendUint64(nil, 9)
	want = append(want, 'o', 'k', 0, 0)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded = %x, want %x", encoded, want)
	}
	instructionData := append([]byte{7}, encoded...)
	decoded, err := DecodeInstruction(report, "increment", instructionData)
	if err != nil {
		t.Fatal(err)
	}
	values := decoded.Value.(map[string]any)
	if values["amount"] != "9" || values["note"] != "ok" {
		t.Fatalf("decoded instruction = %#v", values)
	}

	accountData := binary.LittleEndian.AppendUint64([]byte{1}, 42)
	account, err := DecodeAccount(report, "Counter", accountData)
	if err != nil {
		t.Fatal(err)
	}
	if account.Value.(map[string]any)["value"] != "42" {
		t.Fatalf("decoded account = %#v", account.Value)
	}
	event, err := DecodeEvent(report, "Changed", []byte{0xaa, 0xbb, 'h', 'i', 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if event.Value.(map[string]any)["message"] != "hi" {
		t.Fatalf("decoded event = %#v", event.Value)
	}
}

func TestCodamaPinAndLoadRemainContentAddressed(t *testing.T) {
	registry := t.TempDir()
	pin, err := Pin(registry, testProgram, testCodamaIDL())
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(registry, testProgram, pin.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Spec != "codama/1.0.0" || loaded.SHA256 != pin.SHA256 {
		t.Fatalf("loaded pin = %+v", loaded)
	}
}

func TestCodamaDynamicRemainingAccountsArePinnedButNeverConstructed(t *testing.T) {
	idl := bytes.Replace(testCodamaIDL(), []byte(`"discriminators":[{"kind":"fieldDiscriminatorNode"`), []byte(`"remainingAccounts":[{"kind":"instructionRemainingAccountsNode","isOptional":true}],"discriminators":[{"kind":"fieldDiscriminatorNode"`), 1)
	report, err := Inspect(idl, testProgram)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Instructions[0].DynamicRemainingAccounts {
		t.Fatal("dynamic remaining-account capability was not retained")
	}
	_, err = Build(report, "increment", testProgram, testProgram, []Binding{
		{Name: "state", Address: testProgram},
		{Name: "systemProgram", Address: testProgram},
	}, []ArgumentBinding{
		{Name: "amount", Value: json.RawMessage(`9`)},
		{Name: "note", Value: json.RawMessage(`"ok"`)},
	})
	if err == nil || !strings.Contains(err.Error(), "dynamic remaining accounts") {
		t.Fatalf("build error = %v", err)
	}
}

func TestCodamaSizeDiscriminatorAndConditionalSignerStayFailClosed(t *testing.T) {
	idl := []byte(`{
  "kind":"rootNode","standard":"codama","version":"1.0.0",
  "program":{"kind":"programNode","name":"tokenLike","publicKey":"11111111111111111111111111111111","version":"1.0.0",
    "definedTypes":[],"events":[],
    "accounts":[{"kind":"accountNode","name":"Mint","size":8,
      "data":{"kind":"structTypeNode","fields":[
        {"kind":"structFieldTypeNode","name":"supply","type":{"kind":"numberTypeNode","format":"u64","endian":"le"}}
      ]},
      "discriminators":[{"kind":"sizeDiscriminatorNode","size":8}]}],
    "instructions":[{"kind":"instructionNode","name":"transfer",
      "accounts":[{"kind":"instructionAccountNode","name":"authority","isWritable":false,"isSigner":"either","isOptional":false}],
      "arguments":[],"discriminators":[]}]}}
`)
	report, err := Inspect(idl, testProgram)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Size == nil || *report.Accounts[0].Size != 8 ||
		len(report.Instructions) != 1 || len(report.Instructions[0].Accounts) != 1 ||
		report.Instructions[0].Accounts[0].SignerMode != "either" {
		t.Fatalf("report = %+v", report)
	}
	data := binary.LittleEndian.AppendUint64(nil, 42)
	decoded, err := DecodeAccount(report, "Mint", data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Size == nil || *decoded.Size != 8 || decoded.Value.(map[string]any)["supply"] != "42" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if _, err := DecodeAccount(report, "Mint", append(data, 0)); err == nil ||
		!strings.Contains(err.Error(), "size discriminator") {
		t.Fatalf("oversized account error = %v", err)
	}
	_, err = Build(report, "transfer", testProgram, testProgram, []Binding{{
		Name: "authority", Address: testProgram,
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "conditional signer accounts") {
		t.Fatalf("build error = %v", err)
	}
}

func TestOfficialMemoAdapterBuildsOnlyUnsignedNoSignerMemos(t *testing.T) {
	idl := []byte(`{
  "kind":"rootNode","standard":"codama","version":"1.0.0",
  "program":{"kind":"programNode","name":"memo","publicKey":"MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr","version":"3.0.1",
    "accounts":[],"definedTypes":[],"events":[],
    "instructions":[{"kind":"instructionNode","name":"addMemo","accounts":[],
      "arguments":[{"kind":"instructionArgumentNode","name":"memo","type":{"kind":"stringTypeNode","encoding":"utf8"}}],
      "remainingAccounts":[{"kind":"instructionRemainingAccountsNode","isOptional":true,"isSigner":true}]}]}}
`)
	report, err := Inspect(idl, memoProgramAddress)
	if err != nil {
		t.Fatal(err)
	}
	call, err := Build(report, "addMemo", testProgram, testProgram, nil, []ArgumentBinding{{
		Name: "memo", Value: json.RawMessage(`"walletless"`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := solana.DecodeV0Message(call.Message, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Instructions) != 1 || len(message.Instructions[0].Accounts) != 0 ||
		!bytes.Equal(message.Instructions[0].Data, []byte("walletless")) {
		t.Fatalf("unexpected Memo instruction: %+v", message.Instructions)
	}
	if _, err := Build(report, "addMemo", testProgram, testProgram, []Binding{{
		Name: "signers", Address: testProgram,
	}}, []ArgumentBinding{{Name: "memo", Value: json.RawMessage(`"signed"`)}}); err == nil {
		t.Fatal("dynamic Memo signer account was accepted")
	}
	if _, err := Build(report, "addMemo", testProgram, testProgram, nil, []ArgumentBinding{{
		Name: "memo", Value: json.RawMessage(`"` + strings.Repeat("x", maxUnsignedMemoBytes+1) + `"`),
	}}); err == nil || !strings.Contains(err.Error(), "566-byte") {
		t.Fatalf("oversized Memo error = %v", err)
	}
}

func TestCodamaInspectionFailsClosedOnFormatAndDiscriminatorAmbiguity(t *testing.T) {
	tests := map[string][]byte{
		"new major":                bytes.Replace(testCodamaIDL(), []byte(`"version":"1.0.0"`), []byte(`"version":"2.0.0"`), 1),
		"wrong program":            bytes.Replace(testCodamaIDL(), []byte(testProgram), []byte("SysvarC1ock11111111111111111111111111111111"), 1),
		"big endian discriminator": bytes.Replace(testCodamaIDL(), []byte(`"format":"u8","endian":"le"`), []byte(`"format":"u8","endian":"be"`), 1),
		"discriminator gap":        bytes.Replace(testCodamaIDL(), []byte(`"name":"discriminator","offset":0`), []byte(`"name":"discriminator","offset":1`), 1),
		"non-omitted field":        bytes.Replace(testCodamaIDL(), []byte(`"defaultValueStrategy":"omitted"`), []byte(`"defaultValueStrategy":"optional"`), 1),
	}
	for name, idl := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect(idl, testProgram); err == nil {
				t.Fatal("invalid Codama IDL was accepted")
			}
		})
	}
}

func TestCodamaEnumRejectsDuplicateDiscriminators(t *testing.T) {
	typeNode := json.RawMessage(`{
  "kind":"enumTypeNode",
  "size":{"kind":"numberTypeNode","format":"u8","endian":"le"},
  "variants":[
    {"kind":"enumEmptyVariantTypeNode","name":"first","discriminator":0},
    {"kind":"enumEmptyVariantTypeNode","name":"second","discriminator":0}
  ]
}`)
	if _, err := encodeCodamaValue(typeNode, json.RawMessage(`{"first":null}`), nil, 0); err == nil ||
		!strings.Contains(err.Error(), "discriminators are ambiguous") {
		t.Fatalf("encode error = %v", err)
	}
	decoder := borshDecoder{data: []byte{0}}
	if _, err := decoder.decodeCodamaValue(typeNode, 0); err == nil ||
		!strings.Contains(err.Error(), "discriminators are ambiguous") {
		t.Fatalf("decode error = %v", err)
	}
}

func TestCodamaRoundTripsOfficialCompositeShapes(t *testing.T) {
	typeNode := json.RawMessage(`{
  "kind":"structTypeNode","fields":[
    {"kind":"structFieldTypeNode","name":"status","type":{
      "kind":"enumTypeNode","size":{"kind":"numberTypeNode","format":"u8","endian":"le"},
      "variants":[
        {"kind":"enumEmptyVariantTypeNode","name":"sunset","discriminator":0},
        {"kind":"enumEmptyVariantTypeNode","name":"active","discriminator":1}
      ]
    }},
    {"kind":"structFieldTypeNode","name":"destinations","type":{
      "kind":"arrayTypeNode","item":{"kind":"publicKeyTypeNode"},
      "count":{"kind":"fixedCountNode","value":2}
    }},
    {"kind":"structFieldTypeNode","name":"metadataUri","type":{
      "kind":"fixedSizeTypeNode","size":8,
      "type":{"kind":"stringTypeNode","encoding":"utf8"}
    }}
  ]
}`)
	value := json.RawMessage(`{
  "status":{"active":null},
  "destinations":["11111111111111111111111111111111","11111111111111111111111111111111"],
  "metadataUri":"plan"
}`)
	encoded, err := encodeCodamaValue(typeNode, value, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 1+2*32+8 || encoded[0] != 1 {
		t.Fatalf("encoded composite = %x", encoded)
	}
	decoder := borshDecoder{data: encoded}
	decoded, err := decoder.decodeCodamaValue(typeNode, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := decoded.(map[string]any)
	if fields["status"].(map[string]any)["active"] != nil || fields["metadataUri"] != "plan" {
		t.Fatalf("decoded composite = %#v", fields)
	}
	destinations := fields["destinations"].([]any)
	if len(destinations) != 2 || destinations[0] != testProgram || destinations[1] != testProgram {
		t.Fatalf("decoded destinations = %#v", destinations)
	}
}
