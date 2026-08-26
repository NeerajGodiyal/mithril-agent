package programinterface

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	maxArgumentJSONBytes    = 64 << 10
	maxEncodedArgumentBytes = 64 << 10
	maxArgumentItems        = 4096
	maxArgumentDepth        = 16
)

// EncodeArguments encodes the complete named argument set in pinned IDL order.
// Primitive, container, alias, struct, and enum types use Borsh. Generic and
// custom-serialization definitions remain fail-closed.
func EncodeArguments(
	instruction Instruction,
	types []TypeDefinition,
	bindings []ArgumentBinding,
) ([]byte, error) {
	if len(bindings) != len(instruction.Args) {
		return nil, errors.New("argument bindings do not match the pinned instruction")
	}
	bound := make(map[string]json.RawMessage, len(bindings))
	definitions := make(map[string]TypeDefinition, len(types))
	definitionNames := make(map[string]struct{}, len(types))
	for _, definition := range types {
		folded := strings.ToLower(definition.Name)
		if !validIdentifier(definition.Name) {
			return nil, errors.New("program interface contains an invalid type definition name")
		}
		if _, exists := definitionNames[folded]; exists {
			return nil, errors.New("program interface type definitions are ambiguous")
		}
		definitionNames[folded] = struct{}{}
		definitions[definition.Name] = definition
	}
	for _, binding := range bindings {
		if binding.Name == "" || len(binding.Value) == 0 || len(binding.Value) > maxArgumentJSONBytes {
			return nil, errors.New("argument binding name and bounded JSON value are required")
		}
		if _, exists := bound[binding.Name]; exists {
			return nil, errors.New("argument binding names must be unique")
		}
		if err := strictjson.Validate(binding.Value); err != nil {
			return nil, errors.New("argument binding JSON is invalid or ambiguous")
		}
		bound[binding.Name] = binding.Value
	}
	var encoded []byte
	for _, argument := range instruction.Args {
		value, ok := bound[argument.Name]
		if !ok {
			return nil, errors.New("argument bindings do not match the pinned instruction")
		}
		part, err := encodeIDLValue(argument.Type, value, definitions, 0)
		if err != nil {
			return nil, errors.New("argument " + strconv.Quote(argument.Name) + ": " + err.Error())
		}
		if len(encoded)+len(part) > maxEncodedArgumentBytes {
			return nil, errors.New("encoded arguments exceed 64 KiB")
		}
		encoded = append(encoded, part...)
		delete(bound, argument.Name)
	}
	if len(bound) != 0 {
		return nil, errors.New("argument bindings include names absent from the pinned instruction")
	}
	return encoded, nil
}

func encodeIDLValue(
	idlType, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if depth > maxArgumentDepth {
		return nil, errors.New("IDL argument type is nested too deeply")
	}
	if err := strictjson.Validate(idlType); err != nil {
		return nil, errors.New("IDL argument type is invalid or ambiguous")
	}
	var primitive string
	if err := json.Unmarshal(idlType, &primitive); err == nil {
		return encodePrimitive(primitive, value)
	}
	var container map[string]json.RawMessage
	if err := json.Unmarshal(idlType, &container); err != nil || len(container) != 1 {
		return nil, errors.New("IDL argument type is invalid")
	}
	if raw, ok := container["codama"]; ok {
		return encodeCodamaValue(raw, value, definitions, depth+1)
	}
	if inner, ok := container["option"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return []byte{0}, nil
		}
		encoded, err := encodeIDLValue(inner, value, definitions, depth+1)
		if err != nil {
			return nil, err
		}
		return append([]byte{1}, encoded...), nil
	}
	if inner, ok := container["vec"]; ok {
		items, err := rawArray(value)
		if err != nil || len(items) > maxArgumentItems {
			return nil, errors.New("vector argument must be a JSON array with at most 4096 items")
		}
		encoded := binary.LittleEndian.AppendUint32(nil, uint32(len(items)))
		for _, item := range items {
			part, err := encodeIDLValue(inner, item, definitions, depth+1)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, part...)
			if len(encoded) > maxEncodedArgumentBytes {
				return nil, errors.New("encoded vector exceeds 64 KiB")
			}
		}
		return encoded, nil
	}
	if raw, ok := container["array"]; ok {
		parts, err := rawArray(raw)
		if err != nil || len(parts) != 2 {
			return nil, errors.New("fixed array IDL type is invalid")
		}
		length, err := strconv.ParseUint(string(parts[1]), 10, 32)
		if err != nil || length > maxArgumentItems {
			return nil, errors.New("generic or oversized fixed arrays are unsupported")
		}
		items, err := rawArray(value)
		if err != nil || uint64(len(items)) != length {
			return nil, errors.New("fixed array argument has the wrong length")
		}
		var encoded []byte
		for _, item := range items {
			part, err := encodeIDLValue(parts[0], item, definitions, depth+1)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, part...)
		}
		return encoded, nil
	}
	if reference, ok := container["defined"]; ok {
		return encodeDefined(reference, value, definitions, depth+1)
	}
	if _, ok := container["generic"]; ok {
		return nil, errors.New("generic IDL argument types are not supported")
	}
	return nil, errors.New("IDL argument container type is unsupported")
}

type definedReference struct {
	Name     string            `json:"name"`
	Generics []json.RawMessage `json:"generics"`
}

type typeBody struct {
	Kind     string          `json:"kind"`
	Alias    json.RawMessage `json:"alias"`
	Fields   json.RawMessage `json:"fields"`
	Variants []typeVariant   `json:"variants"`
}

type typeField struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

type typeVariant struct {
	Name   string          `json:"name"`
	Fields json.RawMessage `json:"fields"`
}

func encodeDefined(
	rawReference, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	var reference definedReference
	if err := json.Unmarshal(rawReference, &reference); err != nil ||
		reference.Name == "" || len(reference.Generics) != 0 {
		return nil, errors.New("defined IDL reference is invalid or generic")
	}
	definition, ok := definitions[reference.Name]
	if !ok {
		return nil, errors.New("defined IDL type is absent from the pinned interface")
	}
	if len(definition.Generics) != 0 {
		return nil, errors.New("generic IDL type definitions are unsupported")
	}
	serialization := bytes.TrimSpace(definition.Serialization)
	if len(serialization) != 0 && !bytes.Equal(serialization, []byte(`"borsh"`)) {
		return nil, errors.New("defined IDL type does not use Borsh serialization")
	}
	if err := strictjson.Validate(definition.Type); err != nil {
		return nil, errors.New("defined IDL type is invalid or ambiguous")
	}
	var body typeBody
	if err := json.Unmarshal(definition.Type, &body); err != nil {
		return nil, errors.New("defined IDL type is invalid")
	}
	switch body.Kind {
	case "type":
		if len(body.Alias) == 0 {
			return nil, errors.New("defined IDL alias has no target type")
		}
		return encodeIDLValue(body.Alias, value, definitions, depth)
	case "struct":
		return encodeDefinedFields(body.Fields, value, definitions, depth)
	case "enum":
		return encodeEnum(body.Variants, value, definitions, depth)
	default:
		return nil, errors.New("defined IDL type kind is unsupported")
	}
}

func encodeDefinedFields(
	rawFields, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if depth > maxArgumentDepth {
		return nil, errors.New("defined IDL type is nested too deeply")
	}
	if len(rawFields) == 0 {
		var object map[string]json.RawMessage
		if len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '{' ||
			json.Unmarshal(value, &object) != nil || len(object) != 0 {
			return nil, errors.New("unit struct value must be an empty JSON object")
		}
		return nil, nil
	}
	fields, err := rawArray(rawFields)
	if err != nil || len(fields) > maxArgumentItems {
		return nil, errors.New("defined IDL fields are invalid or oversized")
	}
	if len(fields) == 0 {
		return nil, errors.New("empty IDL fields are ambiguous; omit fields for a unit type")
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(fields[0], &probe)
	if _, named := probe["name"]; named {
		return encodeNamedFields(fields, value, definitions, depth)
	}
	items, err := rawArray(value)
	if err != nil || len(items) != len(fields) {
		return nil, errors.New("tuple value must be a JSON array matching its pinned fields")
	}
	var encoded []byte
	for index := range fields {
		part, err := encodeIDLValue(fields[index], items[index], definitions, depth)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, part...)
	}
	return encoded, nil
}

func encodeNamedFields(
	fields []json.RawMessage,
	value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("named struct value must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || len(object) != len(fields) {
		return nil, errors.New("named struct value does not match its pinned fields")
	}
	var encoded []byte
	seen := make(map[string]struct{}, len(fields))
	for _, raw := range fields {
		var field typeField
		if err := json.Unmarshal(raw, &field); err != nil ||
			!validIdentifier(field.Name) || len(field.Type) == 0 {
			return nil, errors.New("named IDL field is invalid")
		}
		folded := strings.ToLower(field.Name)
		if _, exists := seen[folded]; exists {
			return nil, errors.New("named IDL fields are ambiguous")
		}
		seen[folded] = struct{}{}
		item, ok := object[field.Name]
		if !ok {
			return nil, errors.New("named struct value does not match its pinned fields")
		}
		part, err := encodeIDLValue(field.Type, item, definitions, depth)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, part...)
		delete(object, field.Name)
	}
	if len(object) != 0 {
		return nil, errors.New("named struct value contains an unknown field")
	}
	return encoded, nil
}

func encodeEnum(
	variants []typeVariant,
	value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if len(variants) == 0 || len(variants) > 256 {
		return nil, errors.New("enum must contain between 1 and 256 variants")
	}
	seen := make(map[string]struct{}, len(variants))
	for _, variant := range variants {
		folded := strings.ToLower(variant.Name)
		if !validIdentifier(variant.Name) {
			return nil, errors.New("enum contains an invalid variant name")
		}
		if _, exists := seen[folded]; exists {
			return nil, errors.New("enum variant names are ambiguous")
		}
		seen[folded] = struct{}{}
	}
	if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("enum value must be an object with one variant")
	}
	var selected map[string]json.RawMessage
	if err := json.Unmarshal(value, &selected); err != nil || len(selected) != 1 {
		return nil, errors.New("enum value must select exactly one variant")
	}
	for index, variant := range variants {
		payload, ok := selected[variant.Name]
		if !ok {
			continue
		}
		if len(variant.Fields) == 0 {
			if !bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
				return nil, errors.New("unit enum variant payload must be null")
			}
			return []byte{byte(index)}, nil
		}
		encoded, err := encodeDefinedFields(variant.Fields, payload, definitions, depth)
		if err != nil {
			return nil, err
		}
		return append([]byte{byte(index)}, encoded...), nil
	}
	return nil, errors.New("enum value names no pinned variant")
}

func encodePrimitive(kind string, value json.RawMessage) ([]byte, error) {
	value = bytes.TrimSpace(value)
	switch kind {
	case "bool":
		switch string(value) {
		case "false":
			return []byte{0}, nil
		case "true":
			return []byte{1}, nil
		default:
			return nil, errors.New("bool must be true or false")
		}
	case "u8", "u16", "u32", "u64", "u128", "u256":
		bits, _ := strconv.Atoi(strings.TrimPrefix(kind, "u"))
		return encodeInteger(value, bits, false)
	case "i8", "i16", "i32", "i64", "i128", "i256":
		bits, _ := strconv.Atoi(strings.TrimPrefix(kind, "i"))
		return encodeInteger(value, bits, true)
	case "f32", "f64":
		bits, _ := strconv.Atoi(strings.TrimPrefix(kind, "f"))
		parsed, err := strconv.ParseFloat(string(value), bits)
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return nil, errors.New("floating-point argument is invalid")
		}
		if bits == 32 {
			return binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(parsed))), nil
		}
		return binary.LittleEndian.AppendUint64(nil, math.Float64bits(parsed)), nil
	case "string":
		if len(value) == 0 || value[0] != '"' {
			return nil, errors.New("string argument must be a JSON string")
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, errors.New("string argument must be a JSON string")
		}
		if len(text) > maxEncodedArgumentBytes-4 {
			return nil, errors.New("string argument exceeds 64 KiB")
		}
		return append(binary.LittleEndian.AppendUint32(nil, uint32(len(text))), text...), nil
	case "bytes":
		var data []byte
		if len(value) > 0 && value[0] == '"' {
			var encoded string
			if err := json.Unmarshal(value, &encoded); err != nil {
				return nil, errors.New("bytes must be a bounded base64 string or byte array")
			}
			var err error
			data, err = base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil {
				return nil, errors.New("bytes base64 is not canonical")
			}
		} else if len(value) == 0 || value[0] != '[' {
			return nil, errors.New("bytes must be a bounded base64 string or byte array")
		} else if err := json.Unmarshal(value, &data); err != nil {
			return nil, errors.New("bytes must be a bounded base64 string or byte array")
		}
		if len(data) > maxEncodedArgumentBytes-4 {
			return nil, errors.New("bytes must be a bounded base64 string or byte array")
		}
		return append(binary.LittleEndian.AppendUint32(nil, uint32(len(data))), data...), nil
	case "pubkey":
		if len(value) == 0 || value[0] != '"' {
			return nil, errors.New("pubkey must be a JSON string")
		}
		var address string
		if err := json.Unmarshal(value, &address); err != nil {
			return nil, errors.New("pubkey must be a JSON string")
		}
		key, err := solana.Decode32(address)
		if err != nil {
			return nil, errors.New("pubkey is not a canonical Solana address")
		}
		return key[:], nil
	default:
		return nil, errors.New("primitive IDL argument type is unsupported")
	}
}

func encodeInteger(value json.RawMessage, bits int, signed bool) ([]byte, error) {
	text := string(bytes.TrimSpace(value))
	if len(text) > 1 && text[0] == '"' {
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, errors.New("integer argument is invalid")
		}
	}
	number, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, errors.New("integer must be a decimal JSON number or string")
	}
	limit := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	if signed {
		half := new(big.Int).Rsh(new(big.Int).Set(limit), 1)
		minimum := new(big.Int).Neg(new(big.Int).Set(half))
		if number.Cmp(minimum) < 0 || number.Cmp(half) >= 0 {
			return nil, errors.New("signed integer is out of range")
		}
		if number.Sign() < 0 {
			number.Add(number, limit)
		}
	} else if number.Sign() < 0 || number.Cmp(limit) >= 0 {
		return nil, errors.New("unsigned integer is out of range")
	}
	encoded := make([]byte, bits/8)
	bytes := number.Bytes()
	for index := range bytes {
		encoded[index] = bytes[len(bytes)-1-index]
	}
	return encoded, nil
}

func rawArray(value json.RawMessage) ([]json.RawMessage, error) {
	if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("value is not a JSON array")
	}
	var result []json.RawMessage
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, err
	}
	return result, nil
}
