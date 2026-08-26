package programinterface

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testProgram = "11111111111111111111111111111111"

func testIDL() []byte {
	return []byte(`{
  "address": "11111111111111111111111111111111",
  "metadata": {"name":"counter","version":"0.1.0","spec":"0.1.0"},
  "instructions": [{
    "name":"increment",
    "discriminator":[11,18,104,9,104,174,59,33],
    "accounts":[{"name":"state","writable":true}],
    "args":[{"name":"amount","type":"u64"}]
  }],
  "types": []
}`)
}

func TestInspectBindsExactModernIDL(t *testing.T) {
	report, err := Inspect(testIDL(), testProgram)
	if err != nil {
		t.Fatal(err)
	}
	if report.Program != testProgram || report.Name != "counter" ||
		report.Spec != "0.1.0" || len(report.Instructions) != 1 ||
		report.Instructions[0].Discriminator != "0b12680968ae3b21" ||
		len(report.Instructions[0].Accounts) != 1 ||
		!report.Instructions[0].Accounts[0].Writable ||
		len(report.Instructions[0].Args) != 1 || report.Instructions[0].Args[0].Name != "amount" ||
		string(report.Instructions[0].Args[0].Type) != `"u64"` {
		t.Fatalf("report = %+v", report)
	}
}

func TestInspectRejectsAmbiguousOrMismatchedIDL(t *testing.T) {
	tests := map[string][]byte{
		"missing address":       bytes.Replace(testIDL(), []byte(`"address": "11111111111111111111111111111111",`), nil, 1),
		"wrong address":         bytes.Replace(testIDL(), []byte(testProgram), []byte("SysvarC1ock11111111111111111111111111111111"), 1),
		"duplicate key":         bytes.Replace(testIDL(), []byte(`"name":"increment",`), []byte(`"name":"increment","Name":"other",`), 1),
		"missing discriminator": bytes.Replace(testIDL(), []byte(`"discriminator":[11,18,104,9,104,174,59,33],`), nil, 1),
		"missing accounts":      bytes.Replace(testIDL(), []byte(`"accounts":[{"name":"state","writable":true}],`), nil, 1),
		"missing arguments":     bytes.Replace(testIDL(), []byte(`"args":[{"name":"amount","type":"u64"}]`), nil, 1),
		"wrong spec":            bytes.Replace(testIDL(), []byte(`"spec":"0.1.0"`), []byte(`"spec":"0.2.0"`), 1),
		"duplicate instruction": bytes.Replace(testIDL(), []byte(`}],
  "types"`), []byte(`},{"name":"increment","discriminator":[1,2,3,4,5,6,7,8],"accounts":[],"args":[]}],
  "types"`), 1),
	}
	for name, idl := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect(idl, testProgram); err == nil {
				t.Fatal("invalid IDL was accepted")
			}
		})
	}
	if _, err := Inspect(bytes.Repeat([]byte{' '}, MaxIDLBytes+1), testProgram); err == nil {
		t.Fatal("oversized IDL was accepted")
	}
}

func TestInspectRejectsIncompleteCodamaIDL(t *testing.T) {
	_, err := Inspect([]byte(`{"kind":"rootNode","standard":"codama","version":"1.0.0","program":{}}`), testProgram)
	if err == nil || !strings.Contains(err.Error(), "Codama program metadata is invalid") {
		t.Fatalf("Codama error = %v", err)
	}
}

func TestInspectAcceptsFrameworkSpecificDiscriminatorLengths(t *testing.T) {
	for _, discriminator := range []string{"", "1", "1,2,3,4,5,6,7,8,9"} {
		idl := bytes.Replace(testIDL(), []byte("11,18,104,9,104,174,59,33"), []byte(discriminator), 1)
		if _, err := Inspect(idl, testProgram); err != nil {
			t.Fatalf("discriminator %q: %v", discriminator, err)
		}
	}
}

func TestInspectRetainsPinnedTypeDefinitions(t *testing.T) {
	idl := bytes.Replace(testIDL(), []byte(`"types": []`), []byte(`"types":[{"name":"Amount","type":{"kind":"type","alias":"u64"}}]`), 1)
	report, err := Inspect(idl, testProgram)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Types) != 1 || report.Types[0].Name != "Amount" ||
		string(report.Types[0].Type) != `{"kind":"type","alias":"u64"}` {
		t.Fatalf("types = %+v", report.Types)
	}
}

func TestInspectRetainsAccountAndEventDefinitions(t *testing.T) {
	idl := bytes.Replace(testIDL(), []byte(`"types": []`), []byte(`
  "types":[
    {"name":"Counter","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}},
    {"name":"Changed","type":{"kind":"struct","fields":[{"name":"value","type":"u64"}]}}
  ],
  "accounts":[{"name":"Counter","discriminator":[1,2,3,4,5,6,7,8]}],
  "events":[{"name":"Changed","discriminator":[9,10,11,12,13,14,15,16]}]`), 1)
	report, err := Inspect(idl, testProgram)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Name != "Counter" ||
		report.Accounts[0].Discriminator != "0102030405060708" ||
		len(report.Events) != 1 || report.Events[0].Name != "Changed" ||
		report.Events[0].Discriminator != "090a0b0c0d0e0f10" {
		t.Fatalf("account/event definitions = %+v / %+v", report.Accounts, report.Events)
	}
}

func TestInspectRejectsDataDefinitionWithoutPinnedType(t *testing.T) {
	for name, replacement := range map[string]string{
		"missing": `"types":[],
  "accounts":[{"name":"Counter","discriminator":[1,2,3,4,5,6,7,8]}]`,
		"case mismatch": `"types":[{"name":"Counter","type":{"kind":"struct"}}],
  "accounts":[{"name":"counter","discriminator":[1,2,3,4,5,6,7,8]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			idl := bytes.Replace(testIDL(), []byte(`"types": []`), []byte(replacement), 1)
			if _, err := Inspect(idl, testProgram); err == nil || !strings.Contains(err.Error(), "matching pinned type") {
				t.Fatalf("invalid account type link = %v", err)
			}
		})
	}
}

func TestPinIsImmutableAndIdempotent(t *testing.T) {
	registry, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := Pin(registry, testProgram, testIDL())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || !filepath.IsAbs(first.Path) {
		t.Fatalf("first pin = %+v", first)
	}
	second, err := Pin(registry, testProgram, testIDL())
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Path != first.Path {
		t.Fatalf("second pin = %+v", second)
	}
	loaded, path, err := Load(registry, testProgram, first.SHA256)
	if err != nil || path != first.Path || loaded.SHA256 != first.SHA256 {
		t.Fatalf("load = %+v, %q, %v", loaded, path, err)
	}
	if err := os.WriteFile(first.Path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(registry, testProgram, first.SHA256); err == nil {
		t.Fatal("tampered pin was accepted")
	}
}

func TestReadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "idl.json")
	if err := os.WriteFile(target, testIDL(), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, err := Read(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink read = %v", err)
	}
}
