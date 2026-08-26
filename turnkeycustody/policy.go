package turnkeycustody

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const maxQualificationConditionBytes = 64 << 10

// QualificationPolicy is an unfunded, retained-candidate Turnkey policy. It is
// intentionally not an operational trading policy: every instruction and
// lookup is pinned so the provider's transaction parser can be qualified
// without granting a general signing capability.
type QualificationPolicy struct {
	PolicyName string `json:"policyName"`
	Effect     string `json:"effect"`
	Consensus  string `json:"consensus"`
	Condition  string `json:"condition"`
	Notes      string `json:"notes"`
}

// BuildJupiterQualificationPolicy creates the exact policy used by the live
// mutation harness. It permits a fresh blockhash but otherwise pins the
// canonical candidate. The caller must keep the signing account unfunded.
func BuildJupiterQualificationPolicy(
	policy jupiterswap.Policy,
	candidate proposalcheck.Candidate,
	apiUserID string,
) (QualificationPolicy, error) {
	if !safePolicyIdentifier(apiUserID) {
		return QualificationPolicy{}, errors.New("turnkey qualification API user ID is invalid")
	}
	message, tables, err := proposalcheck.ValidateCandidateMaterial(policy, candidate)
	if err != nil {
		return QualificationPolicy{}, errors.New("validate retained Jupiter candidate")
	}
	decoded, err := solana.DecodeV0Message(message, tables)
	if err != nil {
		return QualificationPolicy{}, errors.New("decode retained Jupiter candidate")
	}
	if len(decoded.StaticAccountKeys) == 0 ||
		solana.Encode(decoded.StaticAccountKeys[0][:]) != policy.Owner {
		return QualificationPolicy{}, errors.New("retained Jupiter fee payer is invalid")
	}

	parts := []string{
		"activity.type == 'ACTIVITY_TYPE_SIGN_TRANSACTION_V2'",
		"activity.params.type == 'TRANSACTION_TYPE_SOLANA'",
		fmt.Sprintf("solana.tx.account_keys.count() == %d", len(decoded.StaticAccountKeys)),
		fmt.Sprintf("solana.tx.account_keys[0] == '%s'", policy.Owner),
		fmt.Sprintf("solana.tx.instructions.count() == %d", len(decoded.Instructions)),
		fmt.Sprintf("solana.tx.address_table_lookups.count() == %d", len(decoded.AddressTableLookups)),
		"solana.tx.spl_transfers.count() == 0",
	}
	for index, key := range decoded.StaticAccountKeys {
		parts = append(parts, fmt.Sprintf(
			"solana.tx.account_keys[%d] == '%s'", index, solana.Encode(key[:]),
		))
	}

	for instructionIndex, instruction := range decoded.Instructions {
		if int(instruction.ProgramIndex) >= len(decoded.StaticAccountKeys) {
			return QualificationPolicy{}, errors.New("retained Jupiter program is not static")
		}
		staticAccounts, instructionLookups, err := qualificationInstructionShape(decoded, instruction)
		if err != nil {
			return QualificationPolicy{}, err
		}
		prefix := fmt.Sprintf("solana.tx.instructions[%d]", instructionIndex)
		parts = append(parts,
			fmt.Sprintf("%s.program_key == '%s'", prefix,
				solana.Encode(decoded.AccountKeys[instruction.ProgramIndex][:])),
			fmt.Sprintf("%s.accounts.count() == %d", prefix, len(staticAccounts)),
			fmt.Sprintf("%s.instruction_data_hex == '%s'", prefix,
				hex.EncodeToString(instruction.Data)),
			fmt.Sprintf("%s.address_table_lookups.count() == %d", prefix, len(instructionLookups)),
		)
		for accountOffset, index := range staticAccounts {
			account := fmt.Sprintf("%s.accounts[%d]", prefix, accountOffset)
			parts = append(parts,
				fmt.Sprintf("%s.account_key == '%s'", account,
					solana.Encode(decoded.StaticAccountKeys[index][:])),
				fmt.Sprintf("%s.signer == %t", account, decoded.IsSigner(index)),
				fmt.Sprintf("%s.writable == %t", account, decoded.IsWritable(index)),
			)
		}
		for lookupIndex, lookup := range instructionLookups {
			lookupPrefix := fmt.Sprintf("%s.address_table_lookups[%d]", prefix, lookupIndex)
			parts = append(parts,
				fmt.Sprintf("%s.address_table_key == '%s'", lookupPrefix,
					solana.Encode(lookup.AddressTableKey[:])),
				fmt.Sprintf("%s.index == %d", lookupPrefix, lookup.Index),
				fmt.Sprintf("%s.writable == %t", lookupPrefix, lookup.Writable),
			)
		}
	}

	parts = appendQualificationLookups(parts, "solana.tx.address_table_lookups", decoded.AddressTableLookups)

	condition := strings.Join(parts, " && ")
	if len(condition) > maxQualificationConditionBytes {
		return QualificationPolicy{}, errors.New("turnkey qualification policy exceeds its local size limit")
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return QualificationPolicy{}, errors.New("fingerprint retained Jupiter policy")
	}
	messageFingerprint := sha256.Sum256(message)
	return QualificationPolicy{
		PolicyName: "Mithril Jupiter qualification " + fingerprint[:8] + "-" +
			hex.EncodeToString(messageFingerprint[:4]),
		Effect:    "EFFECT_ALLOW",
		Consensus: fmt.Sprintf("approvers.any(user, user.id == '%s')", apiUserID),
		Condition: condition,
		Notes:     "Unfunded retained-candidate qualification only; permits blockhash refresh but pins every instruction and lookup. Never use as a funded operational policy.",
	}, nil
}

func appendQualificationLookups(
	parts []string,
	prefix string,
	lookups []solana.MessageAddressTableLookup,
) []string {
	for lookupIndex, lookup := range lookups {
		lookupPrefix := fmt.Sprintf("%s[%d]", prefix, lookupIndex)
		parts = append(parts,
			fmt.Sprintf("%s.address_table_key == '%s'", lookupPrefix, solana.Encode(lookup.AccountKey[:])),
			fmt.Sprintf("%s.writable_indexes.count() == %d", lookupPrefix, len(lookup.WritableIndexes)),
			fmt.Sprintf("%s.readonly_indexes.count() == %d", lookupPrefix, len(lookup.ReadonlyIndexes)),
		)
		for index, value := range lookup.WritableIndexes {
			parts = append(parts, fmt.Sprintf("%s.writable_indexes[%d] == %d", lookupPrefix, index, value))
		}
		for index, value := range lookup.ReadonlyIndexes {
			parts = append(parts, fmt.Sprintf("%s.readonly_indexes[%d] == %d", lookupPrefix, index, value))
		}
	}
	return parts
}

type qualificationInstructionLookup struct {
	AddressTableKey [32]byte
	Index           uint8
	Writable        bool
}

// qualificationInstructionShape mirrors Turnkey's parser: the Account list
// contains only static accounts, while each lookup-loaded account is emitted
// separately in the compiled instruction's account order.
func qualificationInstructionShape(
	message solana.V0Message,
	instruction solana.CompiledInstruction,
) ([]int, []qualificationInstructionLookup, error) {
	staticAccounts := make([]int, 0, len(instruction.Accounts))
	lookups := make([]qualificationInstructionLookup, 0, len(instruction.Accounts))
	for _, account := range instruction.Accounts {
		index := int(account)
		if index >= len(message.AccountKeys) {
			return nil, nil, errors.New("retained Jupiter account index is invalid")
		}
		if index < len(message.StaticAccountKeys) {
			staticAccounts = append(staticAccounts, index)
			continue
		}

		position := len(message.StaticAccountKeys)
		found := false
		for _, lookup := range message.AddressTableLookups {
			for _, tableIndex := range lookup.WritableIndexes {
				if position == index {
					lookups = append(lookups, qualificationInstructionLookup{
						AddressTableKey: lookup.AccountKey,
						Index:           tableIndex,
						Writable:        true,
					})
					found = true
					break
				}
				position++
			}
			if found {
				break
			}
		}
		if found {
			continue
		}
		for _, lookup := range message.AddressTableLookups {
			for _, tableIndex := range lookup.ReadonlyIndexes {
				if position == index {
					lookups = append(lookups, qualificationInstructionLookup{
						AddressTableKey: lookup.AccountKey,
						Index:           tableIndex,
					})
					found = true
					break
				}
				position++
			}
			if found {
				break
			}
		}
		if !found {
			return nil, nil, errors.New("retained Jupiter lookup index is invalid")
		}
	}
	return staticAccounts, lookups, nil
}

func safePolicyIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
