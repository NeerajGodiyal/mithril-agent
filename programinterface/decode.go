package programinterface

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	// MaxAccountDataBytes is Solana's account-data limit.
	MaxAccountDataBytes = 10 << 20
	// MaxInstructionDataBytes is the Solana transaction packet limit.
	MaxInstructionDataBytes = 1232
)

// DecodedData is a local, content-bound interpretation of one account or
// event payload. It never reads a wallet or contacts a network.
type DecodedData struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Discriminator string `json:"discriminator"`
	Size          *int   `json:"size,omitempty"`
	SHA256        string `json:"sha256"`
	Bytes         int    `json:"bytes"`
	Value         any    `json:"value"`
}

func DecodeAccount(report Report, name string, data []byte) (DecodedData, error) {
	return decodeData(report, "account", report.Accounts, name, data)
}

func DecodeEvent(report Report, name string, data []byte) (DecodedData, error) {
	return decodeData(report, "event", report.Events, name, data)
}

// DecodeInstruction decodes exact instruction data with the pinned
// discriminator and argument types.
func DecodeInstruction(report Report, name string, data []byte) (DecodedData, error) {
	if len(data) == 0 || len(data) > MaxInstructionDataBytes {
		return DecodedData{}, errors.New("instruction data is empty or exceeds 1232 bytes")
	}
	var selected *Instruction
	for index := range report.Instructions {
		if report.Instructions[index].Name == name {
			if selected != nil {
				return DecodedData{}, errors.New("program interface instruction is ambiguous")
			}
			selected = &report.Instructions[index]
		}
	}
	if selected == nil {
		return DecodedData{}, errors.New("program interface has no instruction with that exact name")
	}
	discriminator, err := hex.DecodeString(selected.Discriminator)
	if err != nil || !bytes.HasPrefix(data, discriminator) {
		return DecodedData{}, errors.New("instruction data does not match the pinned discriminator")
	}
	types, err := decoderTypes(report.Types)
	if err != nil {
		return DecodedData{}, err
	}
	decoder := borshDecoder{data: data[len(discriminator):], definitions: types}
	values := make(map[string]any, len(selected.Args))
	for _, argument := range selected.Args {
		value, err := decoder.decodeIDLType(argument.Type, 0)
		if err != nil {
			return DecodedData{}, errors.New("instruction data: " + err.Error())
		}
		values[argument.Name] = value
	}
	if decoder.offset != len(decoder.data) {
		return DecodedData{}, errors.New("instruction data contains trailing bytes")
	}
	sum := sha256.Sum256(data)
	return DecodedData{
		Kind: "instruction", Name: name, Discriminator: selected.Discriminator,
		SHA256: hex.EncodeToString(sum[:]), Bytes: len(data), Value: values,
	}, nil
}

func decodeData(
	report Report,
	kind string,
	definitions []DataDefinition,
	name string,
	data []byte,
) (DecodedData, error) {
	if len(data) > MaxAccountDataBytes {
		return DecodedData{}, errors.New("data exceeds the 10 MiB Solana account limit")
	}
	var selected *DataDefinition
	for index := range definitions {
		if definitions[index].Name == name {
			if selected != nil {
				return DecodedData{}, errors.New("program interface data definition is ambiguous")
			}
			selected = &definitions[index]
		}
	}
	if selected == nil {
		return DecodedData{}, errors.New("program interface has no data definition with that exact name")
	}
	if selected.Size != nil && len(data) != *selected.Size {
		return DecodedData{}, errors.New("data does not match the pinned size discriminator")
	}
	discriminator, err := hex.DecodeString(selected.Discriminator)
	if err != nil || !bytes.HasPrefix(data, discriminator) {
		return DecodedData{}, errors.New("data does not match the pinned discriminator")
	}
	types, err := decoderTypes(report.Types)
	if err != nil {
		return DecodedData{}, err
	}
	typeDefinition, ok := types[name]
	if !ok {
		return DecodedData{}, errors.New("data definition has no matching pinned type")
	}
	decoder := borshDecoder{data: data[len(discriminator):], definitions: types}
	value, err := decoder.decodeDefined(typeDefinition, 0)
	if err != nil {
		return DecodedData{}, errors.New(kind + " data: " + err.Error())
	}
	if decoder.offset != len(decoder.data) {
		return DecodedData{}, errors.New(kind + " data contains trailing bytes")
	}
	sum := sha256.Sum256(data)
	return DecodedData{
		Kind: kind, Name: name, Discriminator: selected.Discriminator, Size: selected.Size,
		SHA256: hex.EncodeToString(sum[:]), Bytes: len(data), Value: value,
	}, nil
}

func decoderTypes(definitions []TypeDefinition) (map[string]TypeDefinition, error) {
	result := make(map[string]TypeDefinition, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		folded := strings.ToLower(definition.Name)
		if !validIdentifier(definition.Name) || len(definition.Type) == 0 {
			return nil, errors.New("program interface contains an invalid type definition")
		}
		if _, exists := seen[folded]; exists {
			return nil, errors.New("program interface type definitions are ambiguous")
		}
		seen[folded] = struct{}{}
		result[definition.Name] = definition
	}
	return result, nil
}

type borshDecoder struct {
	data        []byte
	offset      int
	definitions map[string]TypeDefinition
}

func (decoder *borshDecoder) decodeIDLType(idlType json.RawMessage, depth int) (any, error) {
	if depth > maxArgumentDepth {
		return nil, errors.New("IDL data type is nested too deeply")
	}
	if err := strictjson.Validate(idlType); err != nil {
		return nil, errors.New("IDL data type is invalid or ambiguous")
	}
	var primitive string
	if err := json.Unmarshal(idlType, &primitive); err == nil {
		return decoder.decodePrimitive(primitive)
	}
	var container map[string]json.RawMessage
	if err := json.Unmarshal(idlType, &container); err != nil || len(container) != 1 {
		return nil, errors.New("IDL data type is invalid")
	}
	if raw, ok := container["codama"]; ok {
		return decoder.decodeCodamaValue(raw, depth+1)
	}
	if inner, ok := container["option"]; ok {
		tag, err := decoder.read(1)
		if err != nil {
			return nil, err
		}
		switch tag[0] {
		case 0:
			return nil, nil
		case 1:
			return decoder.decodeIDLType(inner, depth+1)
		default:
			return nil, errors.New("Borsh option tag is invalid")
		}
	}
	if inner, ok := container["vec"]; ok {
		length, err := decoder.readLength()
		if err != nil || length > maxArgumentItems {
			return nil, errors.New("Borsh vector length is invalid or exceeds 4096 items")
		}
		result := make([]any, length)
		for index := range result {
			result[index], err = decoder.decodeIDLType(inner, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
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
		result := make([]any, int(length))
		for index := range result {
			result[index], err = decoder.decodeIDLType(parts[0], depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	if reference, ok := container["defined"]; ok {
		return decoder.decodeDefinedReference(reference, depth+1)
	}
	if _, ok := container["generic"]; ok {
		return nil, errors.New("generic IDL data types are not supported")
	}
	return nil, errors.New("IDL data container type is unsupported")
}

func (decoder *borshDecoder) decodeDefinedReference(raw json.RawMessage, depth int) (any, error) {
	var reference definedReference
	if err := json.Unmarshal(raw, &reference); err != nil ||
		reference.Name == "" || len(reference.Generics) != 0 {
		return nil, errors.New("defined IDL reference is invalid or generic")
	}
	definition, ok := decoder.definitions[reference.Name]
	if !ok {
		return nil, errors.New("defined IDL type is absent from the pinned interface")
	}
	return decoder.decodeDefined(definition, depth)
}

func (decoder *borshDecoder) decodeDefined(definition TypeDefinition, depth int) (any, error) {
	if depth > maxArgumentDepth {
		return nil, errors.New("defined IDL type is nested too deeply")
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
		return decoder.decodeIDLType(body.Alias, depth+1)
	case "struct":
		return decoder.decodeFields(body.Fields, depth+1)
	case "enum":
		return decoder.decodeEnum(body.Variants, depth+1)
	default:
		return nil, errors.New("defined IDL type kind is unsupported")
	}
}

func (decoder *borshDecoder) decodeFields(rawFields json.RawMessage, depth int) (any, error) {
	if len(rawFields) == 0 {
		return map[string]any{}, nil
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
	if _, named := probe["name"]; !named {
		result := make([]any, len(fields))
		for index := range fields {
			result[index], err = decoder.decodeIDLType(fields[index], depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	result := make(map[string]any, len(fields))
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
		result[field.Name], err = decoder.decodeIDLType(field.Type, depth+1)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (decoder *borshDecoder) decodeEnum(variants []typeVariant, depth int) (any, error) {
	if len(variants) == 0 || len(variants) > 256 {
		return nil, errors.New("enum must contain between 1 and 256 variants")
	}
	tag, err := decoder.read(1)
	if err != nil {
		return nil, err
	}
	if int(tag[0]) >= len(variants) {
		return nil, errors.New("Borsh enum tag is outside the pinned variants")
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
	variant := variants[int(tag[0])]
	if len(variant.Fields) == 0 {
		return map[string]any{variant.Name: nil}, nil
	}
	payload, err := decoder.decodeFields(variant.Fields, depth+1)
	if err != nil {
		return nil, err
	}
	return map[string]any{variant.Name: payload}, nil
}

func (decoder *borshDecoder) decodePrimitive(kind string) (any, error) {
	switch kind {
	case "bool":
		value, err := decoder.read(1)
		if err != nil {
			return nil, err
		}
		if value[0] > 1 {
			return nil, errors.New("Borsh bool is not 0 or 1")
		}
		return value[0] == 1, nil
	case "u8", "u16", "u32", "u64", "u128", "u256":
		bits, _ := strconv.Atoi(strings.TrimPrefix(kind, "u"))
		return decoder.decodeInteger(bits, false)
	case "i8", "i16", "i32", "i64", "i128", "i256":
		bits, _ := strconv.Atoi(strings.TrimPrefix(kind, "i"))
		return decoder.decodeInteger(bits, true)
	case "f32":
		value, err := decoder.read(4)
		if err != nil {
			return nil, err
		}
		decoded := math.Float32frombits(binary.LittleEndian.Uint32(value))
		if math.IsNaN(float64(decoded)) || math.IsInf(float64(decoded), 0) {
			return nil, errors.New("Borsh f32 is not finite")
		}
		return decoded, nil
	case "f64":
		value, err := decoder.read(8)
		if err != nil {
			return nil, err
		}
		decoded := math.Float64frombits(binary.LittleEndian.Uint64(value))
		if math.IsNaN(decoded) || math.IsInf(decoded, 0) {
			return nil, errors.New("Borsh f64 is not finite")
		}
		return decoded, nil
	case "string":
		length, err := decoder.readLength()
		if err != nil {
			return nil, err
		}
		value, err := decoder.read(length)
		if err != nil || !utf8.Valid(value) {
			return nil, errors.New("Borsh string length or UTF-8 is invalid")
		}
		return string(value), nil
	case "bytes":
		length, err := decoder.readLength()
		if err != nil {
			return nil, err
		}
		value, err := decoder.read(length)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString(value), nil
	case "pubkey":
		value, err := decoder.read(32)
		if err != nil {
			return nil, err
		}
		return solana.Encode(value), nil
	default:
		return nil, errors.New("primitive IDL data type is unsupported")
	}
}

func (decoder *borshDecoder) decodeInteger(bits int, signed bool) (any, error) {
	encoded, err := decoder.read(bits / 8)
	if err != nil {
		return nil, err
	}
	reversed := append([]byte(nil), encoded...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	value := new(big.Int).SetBytes(reversed)
	if signed && encoded[len(encoded)-1]&0x80 != 0 {
		value.Sub(value, new(big.Int).Lsh(big.NewInt(1), uint(bits)))
	}
	if bits <= 32 {
		if signed {
			return value.Int64(), nil
		}
		return value.Uint64(), nil
	}
	return value.String(), nil
}

func (decoder *borshDecoder) readLength() (int, error) {
	value, err := decoder.read(4)
	if err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint32(value)), nil
}

func (decoder *borshDecoder) read(length int) ([]byte, error) {
	if length < 0 || length > len(decoder.data)-decoder.offset {
		return nil, errors.New("Borsh data is truncated or has an invalid length")
	}
	value := decoder.data[decoder.offset : decoder.offset+length]
	decoder.offset += length
	return value, nil
}
