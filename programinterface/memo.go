package programinterface

import (
	"errors"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	memoProgramAddress   = "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"
	maxUnsignedMemoBytes = 566
)

// validateUnsignedMemoAdapter admits only the official Memo addMemo shape
// with no optional signer accounts. The transaction fee payer is still the
// sole message signer, but the Memo instruction itself receives no accounts.
func validateUnsignedMemoAdapter(report Report, instruction Instruction, bindings []Binding) error {
	if report.Program != memoProgramAddress || report.Spec != "codama/1.0.0" ||
		instruction.Name != "addMemo" || instruction.Discriminator != "" ||
		len(instruction.Accounts) != 0 || len(bindings) != 0 || len(instruction.Args) != 1 ||
		instruction.Args[0].Name != "memo" {
		return errors.New("pinned interface is not the reviewed unsigned Memo adapter")
	}
	var wrapped struct {
		Codama codamaTypeNode `json:"codama"`
	}
	if err := strictjson.Decode(instruction.Args[0].Type, &wrapped); err != nil ||
		wrapped.Codama.Kind != "stringTypeNode" || wrapped.Codama.Encoding != "utf8" {
		return errors.New("pinned Memo interface does not use raw UTF-8 data")
	}
	return nil
}
