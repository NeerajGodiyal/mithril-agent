package programinterface

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

type codamaRoot struct {
	Kind     string         `json:"kind"`
	Standard string         `json:"standard"`
	Version  string         `json:"version"`
	Program  *codamaProgram `json:"program"`
}

type codamaProgram struct {
	Kind         string              `json:"kind"`
	Name         string              `json:"name"`
	PublicKey    string              `json:"publicKey"`
	Version      string              `json:"version"`
	Instructions []codamaInstruction `json:"instructions"`
	DefinedTypes []codamaDefinedType `json:"definedTypes"`
	Accounts     []codamaDataNode    `json:"accounts"`
	Events       []codamaDataNode    `json:"events"`
}

type codamaInstruction struct {
	Kind              string                     `json:"kind"`
	Name              string                     `json:"name"`
	Accounts          []codamaInstructionAccount `json:"accounts"`
	Arguments         []codamaField              `json:"arguments"`
	Discriminators    []json.RawMessage          `json:"discriminators"`
	RemainingAccounts []json.RawMessage          `json:"remainingAccounts"`
}

type codamaInstructionAccount struct {
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Writable     bool            `json:"isWritable"`
	Signer       json.RawMessage `json:"isSigner"`
	Optional     bool            `json:"isOptional"`
	DefaultValue json.RawMessage `json:"defaultValue"`
}

type codamaField struct {
	Kind                 string          `json:"kind"`
	Name                 string          `json:"name"`
	Type                 json.RawMessage `json:"type"`
	DefaultValue         json.RawMessage `json:"defaultValue"`
	DefaultValueStrategy string          `json:"defaultValueStrategy"`
}

type codamaDefinedType struct {
	Kind string          `json:"kind"`
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

type codamaDataNode struct {
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Data           json.RawMessage   `json:"data"`
	Discriminators []json.RawMessage `json:"discriminators"`
}

// inspectCodama binds a current Codama v1 interface directly to the existing
// content-addressed registry. Codama type nodes stay intact inside a small
// wrapper so construction and decoding use their exact codec semantics rather
// than guessing an Anchor/Borsh equivalent.
func inspectCodama(data []byte, expectedProgram string) (Report, error) {
	var root codamaRoot
	if err := json.Unmarshal(data, &root); err != nil || root.Program == nil {
		return Report{}, errors.New("Codama IDL root is invalid")
	}
	if root.Kind != "rootNode" || root.Standard != "codama" || !codamaV1(root.Version) {
		return Report{}, errors.New("Codama IDL must use the supported 1.x root format")
	}
	program := root.Program
	if program.Kind != "programNode" || !validIdentifier(program.Name) || program.Version == "" {
		return Report{}, errors.New("Codama program metadata is invalid")
	}
	if _, err := solana.Decode32(program.PublicKey); err != nil {
		return Report{}, errors.New("Codama program public key is invalid")
	}
	if program.PublicKey != expectedProgram {
		return Report{}, errors.New("Codama program public key does not match the expected program")
	}
	if program.Instructions == nil || len(program.Instructions) > maxInstructions ||
		len(program.DefinedTypes)+len(program.Accounts)+len(program.Events) > maxTypeDefinitions {
		return Report{}, errors.New("Codama definition count is outside the supported range")
	}

	sum := sha256.Sum256(data)
	report := Report{
		Program: program.PublicKey, SHA256: hex.EncodeToString(sum[:]), Name: program.Name,
		Version: program.Version, Spec: "codama/" + root.Version,
		Instructions: make([]Instruction, 0, len(program.Instructions)),
		Types:        make([]TypeDefinition, 0, len(program.DefinedTypes)+len(program.Accounts)+len(program.Events)),
		Accounts:     make([]DataDefinition, 0, len(program.Accounts)),
		Events:       make([]DataDefinition, 0, len(program.Events)),
	}
	typeNames := make(map[string]struct{}, cap(report.Types))
	for _, definition := range program.DefinedTypes {
		if definition.Kind != "definedTypeNode" || !validIdentifier(definition.Name) || len(definition.Type) == 0 {
			return Report{}, errors.New("Codama IDL contains an invalid defined type")
		}
		if err := appendCodamaType(&report, typeNames, definition.Name, definition.Type); err != nil {
			return Report{}, err
		}
	}
	definitions := typeDefinitionMap(report.Types)

	var err error
	report.Accounts, err = inspectCodamaDataNodes(&report, typeNames, definitions, program.Accounts, "accountNode")
	if err != nil {
		return Report{}, fmt.Errorf("Codama accounts: %w", err)
	}
	definitions = typeDefinitionMap(report.Types)
	report.Events, err = inspectCodamaDataNodes(&report, typeNames, definitions, program.Events, "eventNode")
	if err != nil {
		return Report{}, fmt.Errorf("Codama events: %w", err)
	}
	definitions = typeDefinitionMap(report.Types)
	if err := inspectCodamaInstructions(&report, definitions, program.Instructions); err != nil {
		return Report{}, err
	}
	return report, nil
}

func codamaV1(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[0] != "1" {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

func appendCodamaType(report *Report, names map[string]struct{}, name string, raw json.RawMessage) error {
	folded := strings.ToLower(name)
	if _, exists := names[folded]; exists {
		return errors.New("Codama type names are duplicated")
	}
	wrapped, err := wrapCodamaType(raw)
	if err != nil {
		return err
	}
	names[folded] = struct{}{}
	report.Types = append(report.Types, TypeDefinition{
		Name: name,
		Type: json.RawMessage(`{"kind":"type","alias":` + string(wrapped) + `}`),
	})
	return nil
}

func wrapCodamaType(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("Codama type is missing")
	}
	var node struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &node); err != nil ||
		(!strings.HasSuffix(node.Kind, "TypeNode") && node.Kind != "definedTypeLinkNode") {
		return nil, errors.New("Codama type node is invalid")
	}
	wrapped, err := json.Marshal(struct {
		Codama json.RawMessage `json:"codama"`
	}{Codama: append(json.RawMessage(nil), raw...)})
	if err != nil {
		return nil, errors.New("encode Codama type wrapper")
	}
	return wrapped, nil
}

func typeDefinitionMap(types []TypeDefinition) map[string]TypeDefinition {
	result := make(map[string]TypeDefinition, len(types))
	for _, definition := range types {
		result[definition.Name] = definition
	}
	return result
}

func inspectCodamaDataNodes(
	report *Report,
	typeNames map[string]struct{},
	definitions map[string]TypeDefinition,
	nodes []codamaDataNode,
	wantKind string,
) ([]DataDefinition, error) {
	result := make([]DataDefinition, 0, len(nodes))
	seenNames := make(map[string]struct{}, len(nodes))
	seenDiscriminators := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		folded := strings.ToLower(node.Name)
		if node.Kind != wantKind || !validIdentifier(node.Name) || len(node.Data) == 0 {
			return nil, errors.New("definition is invalid")
		}
		if _, exists := seenNames[folded]; exists {
			return nil, errors.New("definition names are duplicated")
		}
		fields, err := codamaStructFields(node.Data)
		if err != nil {
			return nil, err
		}
		discriminator, removed, size, err := codamaDataDiscriminator(node.Discriminators, fields, definitions)
		if err != nil {
			return nil, err
		}
		data, err := stripCodamaFields(node.Data, removed)
		if err != nil {
			return nil, err
		}
		if err := appendCodamaType(report, typeNames, node.Name, data); err != nil {
			return nil, err
		}
		encoded := hex.EncodeToString(discriminator)
		identity := encoded
		if size != nil {
			identity = fmt.Sprintf("size:%d", *size)
		}
		if identity != "" {
			if _, exists := seenDiscriminators[identity]; exists {
				return nil, errors.New("definition discriminators are duplicated")
			}
			seenDiscriminators[identity] = struct{}{}
		}
		seenNames[folded] = struct{}{}
		result = append(result, DataDefinition{Name: node.Name, Discriminator: encoded, Size: size})
		definitions[node.Name] = report.Types[len(report.Types)-1]
	}
	return result, nil
}

func inspectCodamaInstructions(
	report *Report,
	definitions map[string]TypeDefinition,
	instructions []codamaInstruction,
) error {
	seenNames := make(map[string]struct{}, len(instructions))
	seenDiscriminators := make(map[string]struct{}, len(instructions))
	for _, raw := range instructions {
		folded := strings.ToLower(raw.Name)
		if raw.Kind != "instructionNode" || !validIdentifier(raw.Name) ||
			raw.Accounts == nil || raw.Arguments == nil {
			return errors.New("Codama instruction is invalid")
		}
		if _, exists := seenNames[folded]; exists {
			return errors.New("Codama instruction names are duplicated")
		}
		if len(raw.Accounts) > maxAccounts || len(raw.Arguments) > maxInstructionArgs {
			return fmt.Errorf("Codama instruction %q exceeds supported account or argument limits", raw.Name)
		}
		discriminator, removed, err := codamaDiscriminator(raw.Discriminators, raw.Arguments, definitions)
		if err != nil {
			return fmt.Errorf("Codama instruction %q discriminator: %w", raw.Name, err)
		}
		encodedDiscriminator := hex.EncodeToString(discriminator)
		if encodedDiscriminator != "" {
			if _, exists := seenDiscriminators[encodedDiscriminator]; exists {
				return errors.New("Codama instruction discriminators are duplicated")
			}
			seenDiscriminators[encodedDiscriminator] = struct{}{}
		}

		accounts := make([]Account, 0, len(raw.Accounts))
		accountNames := make(map[string]struct{}, len(raw.Accounts))
		for _, account := range raw.Accounts {
			accountFolded := strings.ToLower(account.Name)
			if account.Kind != "instructionAccountNode" || !validIdentifier(account.Name) {
				return fmt.Errorf("Codama instruction %q contains an invalid account", raw.Name)
			}
			if _, exists := accountNames[accountFolded]; exists {
				return fmt.Errorf("Codama instruction %q contains duplicate account names", raw.Name)
			}
			address, err := codamaStaticAddress(account.DefaultValue, report.Program)
			if err != nil {
				return fmt.Errorf("Codama instruction %q account %q: %w", raw.Name, account.Name, err)
			}
			signer, signerMode, err := codamaSigner(account.Signer)
			if err != nil {
				return fmt.Errorf("Codama instruction %q account %q: %w", raw.Name, account.Name, err)
			}
			accountNames[accountFolded] = struct{}{}
			accounts = append(accounts, Account{
				Name: account.Name, Writable: account.Writable, Signer: signer, SignerMode: signerMode,
				Optional: account.Optional, Address: address,
			})
		}
		args := make([]Argument, 0, len(raw.Arguments)-len(removed))
		argNames := make(map[string]struct{}, len(raw.Arguments))
		for _, arg := range raw.Arguments {
			argFolded := strings.ToLower(arg.Name)
			if arg.Kind != "instructionArgumentNode" || !validIdentifier(arg.Name) || len(arg.Type) == 0 {
				return fmt.Errorf("Codama instruction %q contains an invalid argument", raw.Name)
			}
			if _, exists := argNames[argFolded]; exists {
				return fmt.Errorf("Codama instruction %q contains duplicate argument names", raw.Name)
			}
			argNames[argFolded] = struct{}{}
			if _, omit := removed[arg.Name]; omit {
				continue
			}
			wrapped, err := wrapCodamaType(arg.Type)
			if err != nil {
				return fmt.Errorf("Codama instruction %q argument %q: %w", raw.Name, arg.Name, err)
			}
			args = append(args, Argument{Name: arg.Name, Type: wrapped})
		}
		seenNames[folded] = struct{}{}
		report.Instructions = append(report.Instructions, Instruction{
			Name: raw.Name, Discriminator: encodedDiscriminator, Accounts: accounts, Args: args,
			DynamicRemainingAccounts: len(raw.RemainingAccounts) != 0,
		})
	}
	return nil
}

func codamaSigner(raw json.RawMessage) (bool, string, error) {
	var signer bool
	if err := json.Unmarshal(raw, &signer); err == nil {
		return signer, "", nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil && mode == "either" {
		return false, mode, nil
	}
	return false, "", errors.New("signer mode is invalid or unsupported")
}

func codamaDataDiscriminator(
	discriminators []json.RawMessage,
	fields []codamaField,
	definitions map[string]TypeDefinition,
) ([]byte, map[string]struct{}, *int, error) {
	if len(discriminators) == 1 {
		var discriminator struct {
			Kind string `json:"kind"`
			Size int    `json:"size"`
		}
		if err := json.Unmarshal(discriminators[0], &discriminator); err == nil &&
			discriminator.Kind == "sizeDiscriminatorNode" {
			if discriminator.Size <= 0 || discriminator.Size > MaxAccountDataBytes {
				return nil, nil, nil, errors.New("size discriminator is outside the Solana account limit")
			}
			return nil, map[string]struct{}{}, &discriminator.Size, nil
		}
	}
	discriminator, removed, err := codamaDiscriminator(discriminators, fields, definitions)
	return discriminator, removed, nil, err
}

func codamaStaticAddress(raw json.RawMessage, program string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value struct {
		Kind      string `json:"kind"`
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("default account value is invalid")
	}
	switch value.Kind {
	case "programIdValueNode":
		return program, nil
	case "publicKeyValueNode":
		if _, err := solana.Decode32(value.PublicKey); err != nil {
			return "", errors.New("default public key is invalid")
		}
		return value.PublicKey, nil
	default:
		return "", nil
	}
}

func codamaStructFields(raw json.RawMessage) ([]codamaField, error) {
	var data struct {
		Kind   string        `json:"kind"`
		Fields []codamaField `json:"fields"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.Kind != "structTypeNode" || data.Fields == nil {
		return nil, errors.New("data must use a Codama struct type")
	}
	return data.Fields, nil
}

func stripCodamaFields(raw json.RawMessage, removed map[string]struct{}) (json.RawMessage, error) {
	if len(removed) == 0 {
		return append(json.RawMessage(nil), raw...), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("Codama struct type is invalid")
	}
	var fields []json.RawMessage
	if err := json.Unmarshal(object["fields"], &fields); err != nil {
		return nil, errors.New("Codama struct fields are invalid")
	}
	kept := make([]json.RawMessage, 0, len(fields)-len(removed))
	for _, rawField := range fields {
		var field codamaField
		if err := json.Unmarshal(rawField, &field); err != nil {
			return nil, errors.New("Codama struct field is invalid")
		}
		if _, omit := removed[field.Name]; !omit {
			kept = append(kept, rawField)
		}
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return nil, errors.New("encode Codama struct fields")
	}
	object["fields"] = encoded
	result, err := json.Marshal(object)
	if err != nil {
		return nil, errors.New("encode Codama struct type")
	}
	return result, nil
}

func codamaDiscriminator(
	discriminators []json.RawMessage,
	fields []codamaField,
	definitions map[string]TypeDefinition,
) ([]byte, map[string]struct{}, error) {
	removed := make(map[string]struct{})
	var result []byte
	var occupied []bool
	for _, raw := range discriminators {
		var discriminator struct {
			Kind     string          `json:"kind"`
			Offset   int             `json:"offset"`
			Name     string          `json:"name"`
			Constant json.RawMessage `json:"constant"`
		}
		if err := json.Unmarshal(raw, &discriminator); err != nil || discriminator.Offset < 0 {
			return nil, nil, errors.New("discriminator node is invalid")
		}
		var encoded []byte
		switch discriminator.Kind {
		case "constantDiscriminatorNode":
			typeNode, value, err := codamaConstant(discriminator.Constant)
			if err != nil {
				return nil, nil, err
			}
			encoded, err = encodeCodamaValue(typeNode, value, definitions, 0)
			if err != nil {
				return nil, nil, err
			}
		case "fieldDiscriminatorNode":
			var selected *codamaField
			for index := range fields {
				if fields[index].Name == discriminator.Name {
					selected = &fields[index]
					break
				}
			}
			if selected == nil || selected.DefaultValueStrategy != "omitted" || len(selected.DefaultValue) == 0 {
				return nil, nil, errors.New("field discriminator must reference one omitted default field")
			}
			value, err := codamaValueJSON(selected.DefaultValue)
			if err != nil {
				return nil, nil, err
			}
			encoded, err = encodeCodamaValue(selected.Type, value, definitions, 0)
			if err != nil {
				return nil, nil, err
			}
			removed[selected.Name] = struct{}{}
		default:
			return nil, nil, errors.New("discriminator kind is unsupported")
		}
		if len(encoded) == 0 || discriminator.Offset+len(encoded) > MaxInstructionDataBytes {
			return nil, nil, errors.New("discriminator bytes are empty or oversized")
		}
		end := discriminator.Offset + len(encoded)
		if end > len(result) {
			result = append(result, make([]byte, end-len(result))...)
			occupied = append(occupied, make([]bool, end-len(occupied))...)
		}
		for index, value := range encoded {
			position := discriminator.Offset + index
			if occupied[position] && result[position] != value {
				return nil, nil, errors.New("discriminator bytes conflict")
			}
			occupied[position] = true
			result[position] = value
		}
	}
	for _, present := range occupied {
		if !present {
			return nil, nil, errors.New("non-prefix discriminator gaps are unsupported")
		}
	}
	return result, removed, nil
}

func codamaConstant(raw json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var constant struct {
		Kind  string          `json:"kind"`
		Type  json.RawMessage `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &constant); err != nil || constant.Kind != "constantValueNode" ||
		len(constant.Type) == 0 || len(constant.Value) == 0 {
		return nil, nil, errors.New("constant discriminator is invalid")
	}
	value, err := codamaValueJSON(constant.Value)
	return constant.Type, value, err
}

func codamaValueJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value struct {
		Kind      string          `json:"kind"`
		Number    json.RawMessage `json:"number"`
		Boolean   *bool           `json:"boolean"`
		String    string          `json:"string"`
		PublicKey string          `json:"publicKey"`
		Data      string          `json:"data"`
		Encoding  string          `json:"encoding"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("Codama default value is invalid")
	}
	switch value.Kind {
	case "numberValueNode":
		if len(value.Number) == 0 {
			return nil, errors.New("Codama number value is missing")
		}
		return append(json.RawMessage(nil), value.Number...), nil
	case "booleanValueNode":
		if value.Boolean == nil {
			return nil, errors.New("Codama boolean value is missing")
		}
		return json.Marshal(*value.Boolean)
	case "stringValueNode":
		return json.Marshal(value.String)
	case "publicKeyValueNode":
		if _, err := solana.Decode32(value.PublicKey); err != nil {
			return nil, errors.New("Codama public key value is invalid")
		}
		return json.Marshal(value.PublicKey)
	case "bytesValueNode":
		var decoded []byte
		var err error
		switch value.Encoding {
		case "base16":
			decoded, err = hex.DecodeString(value.Data)
		case "base64":
			decoded, err = base64.StdEncoding.Strict().DecodeString(value.Data)
		case "utf8":
			decoded = []byte(value.Data)
		default:
			return nil, errors.New("Codama byte value encoding is unsupported")
		}
		if err != nil {
			return nil, errors.New("Codama byte value is invalid")
		}
		return json.Marshal(base64.StdEncoding.EncodeToString(decoded))
	default:
		return nil, errors.New("Codama default value kind is unsupported")
	}
}
