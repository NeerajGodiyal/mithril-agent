package strictjson

import "testing"

func TestDecodeRejectsAmbiguousOrExtraInput(t *testing.T) {
	type document struct {
		Method string `json:"method"`
	}
	tests := []string{
		`{"method":"first","method":"second"}`,
		`{"method":"first","Method":"second"}`,
		`{"method":"first","unknown":true}`,
		`{"method":"first"}{"method":"second"}`,
	}
	for _, input := range tests {
		var value document
		if err := Decode([]byte(input), &value); err == nil {
			t.Fatalf("ambiguous JSON was accepted: %s", input)
		}
	}
	var value document
	if err := Decode([]byte(`{"method":"ok"}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.Method != "ok" {
		t.Fatalf("decoded value = %+v", value)
	}
}

func TestValidateRejectsNestedDuplicateKeys(t *testing.T) {
	if err := Validate([]byte(`{"outer":[{"slot":1,"Slot":2}]}`)); err == nil {
		t.Fatal("nested duplicate key was accepted")
	}
	if err := Validate([]byte(`{"outer":[{"slot":1}],"extra":true}`)); err != nil {
		t.Fatal(err)
	}
}
