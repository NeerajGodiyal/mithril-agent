package programinterface

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

type codamaTypeNode struct {
	Kind     string            `json:"kind"`
	Format   string            `json:"format"`
	Endian   string            `json:"endian"`
	Encoding string            `json:"encoding"`
	Name     string            `json:"name"`
	Type     json.RawMessage   `json:"type"`
	Item     json.RawMessage   `json:"item"`
	Count    json.RawMessage   `json:"count"`
	Prefix   json.RawMessage   `json:"prefix"`
	Size     json.RawMessage   `json:"size"`
	Fields   []codamaField     `json:"fields"`
	Items    []json.RawMessage `json:"items"`
	Variants []codamaVariant   `json:"variants"`
}

type codamaVariant struct {
	Kind          string            `json:"kind"`
	Name          string            `json:"name"`
	Discriminator json.RawMessage   `json:"discriminator"`
	Fields        []codamaField     `json:"fields"`
	Items         []json.RawMessage `json:"items"`
}

func encodeCodamaValue(
	rawType, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if depth > maxArgumentDepth {
		return nil, errors.New("Codama type is nested too deeply")
	}
	if err := strictjson.Validate(rawType); err != nil {
		return nil, errors.New("Codama type is invalid or ambiguous")
	}
	var node codamaTypeNode
	if err := json.Unmarshal(rawType, &node); err != nil {
		return nil, errors.New("Codama type is invalid")
	}
	switch node.Kind {
	case "numberTypeNode":
		format, err := codamaNumberFormat(node)
		if err != nil {
			return nil, err
		}
		return encodePrimitive(format, value)
	case "booleanTypeNode":
		if err := codamaBoolSize(node.Size); err != nil {
			return nil, err
		}
		return encodePrimitive("bool", value)
	case "publicKeyTypeNode":
		return encodePrimitive("pubkey", value)
	case "bytesTypeNode":
		return encodeRawCodamaBytes(value)
	case "stringTypeNode":
		return encodeRawCodamaString(node.Encoding, value)
	case "definedTypeLinkNode":
		_, ok := definitions[node.Name]
		if !ok || !validIdentifier(node.Name) {
			return nil, errors.New("Codama defined type link is unresolved")
		}
		reference, _ := json.Marshal(definedReference{Name: node.Name})
		return encodeDefined(reference, value, definitions, depth+1)
	case "structTypeNode":
		return encodeCodamaStruct(node.Fields, value, definitions, depth+1)
	case "tupleTypeNode":
		return encodeCodamaTuple(node.Items, value, definitions, depth+1)
	case "arrayTypeNode":
		return encodeCodamaArray(node.Item, node.Count, value, definitions, depth+1)
	case "optionTypeNode":
		return encodeCodamaOption(node.Type, node.Prefix, value, definitions, depth+1)
	case "fixedSizeTypeNode":
		return encodeCodamaFixed(node.Type, node.Size, value, definitions, depth+1)
	case "sizePrefixTypeNode":
		return encodeCodamaSizePrefixed(node.Type, node.Prefix, value, definitions, depth+1)
	case "enumTypeNode":
		return encodeCodamaEnum(node.Size, node.Variants, value, definitions, depth+1)
	default:
		return nil, fmt.Errorf("Codama type kind %q is unsupported", node.Kind)
	}
}

func (decoder *borshDecoder) decodeCodamaValue(rawType json.RawMessage, depth int) (any, error) {
	if depth > maxArgumentDepth {
		return nil, errors.New("Codama type is nested too deeply")
	}
	if err := strictjson.Validate(rawType); err != nil {
		return nil, errors.New("Codama type is invalid or ambiguous")
	}
	var node codamaTypeNode
	if err := json.Unmarshal(rawType, &node); err != nil {
		return nil, errors.New("Codama type is invalid")
	}
	switch node.Kind {
	case "numberTypeNode":
		format, err := codamaNumberFormat(node)
		if err != nil {
			return nil, err
		}
		return decoder.decodePrimitive(format)
	case "booleanTypeNode":
		if err := codamaBoolSize(node.Size); err != nil {
			return nil, err
		}
		return decoder.decodePrimitive("bool")
	case "publicKeyTypeNode":
		return decoder.decodePrimitive("pubkey")
	case "bytesTypeNode":
		data, err := decoder.read(len(decoder.data) - decoder.offset)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString(data), nil
	case "stringTypeNode":
		data, err := decoder.read(len(decoder.data) - decoder.offset)
		if err != nil {
			return nil, err
		}
		return decodeRawCodamaString(node.Encoding, data)
	case "definedTypeLinkNode":
		definition, ok := decoder.definitions[node.Name]
		if !ok || !validIdentifier(node.Name) {
			return nil, errors.New("Codama defined type link is unresolved")
		}
		return decoder.decodeDefined(definition, depth+1)
	case "structTypeNode":
		return decoder.decodeCodamaStruct(node.Fields, depth+1)
	case "tupleTypeNode":
		return decoder.decodeCodamaTuple(node.Items, depth+1)
	case "arrayTypeNode":
		return decoder.decodeCodamaArray(node.Item, node.Count, depth+1)
	case "optionTypeNode":
		return decoder.decodeCodamaOption(node.Type, node.Prefix, depth+1)
	case "fixedSizeTypeNode":
		return decoder.decodeCodamaFixed(node.Type, node.Size, depth+1)
	case "sizePrefixTypeNode":
		return decoder.decodeCodamaSizePrefixed(node.Type, node.Prefix, depth+1)
	case "enumTypeNode":
		return decoder.decodeCodamaEnum(node.Size, node.Variants, depth+1)
	default:
		return nil, fmt.Errorf("Codama type kind %q is unsupported", node.Kind)
	}
}

func codamaNumberFormat(node codamaTypeNode) (string, error) {
	if node.Endian != "le" {
		return "", errors.New("Codama big-endian or unspecified numbers are unsupported")
	}
	switch node.Format {
	case "u8", "u16", "u32", "u64", "u128", "u256",
		"i8", "i16", "i32", "i64", "i128", "i256", "f32", "f64":
		return node.Format, nil
	default:
		return "", errors.New("Codama number format is unsupported")
	}
}

func codamaBoolSize(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var size codamaTypeNode
	if err := json.Unmarshal(raw, &size); err != nil {
		return errors.New("Codama boolean size is invalid")
	}
	format, err := codamaNumberFormat(size)
	if err != nil || format != "u8" {
		return errors.New("only one-byte Codama booleans are supported")
	}
	return nil
}

func encodeRawCodamaBytes(value json.RawMessage) ([]byte, error) {
	value = bytes.TrimSpace(value)
	if len(value) > 0 && value[0] == '"' {
		var encoded string
		if err := json.Unmarshal(value, &encoded); err != nil {
			return nil, errors.New("Codama bytes must be base64 or a byte array")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, errors.New("Codama bytes base64 is not canonical")
		}
		return data, nil
	}
	var data []byte
	if err := json.Unmarshal(value, &data); err != nil {
		return nil, errors.New("Codama bytes must be base64 or a byte array")
	}
	return data, nil
}

func encodeRawCodamaString(encoding string, value json.RawMessage) ([]byte, error) {
	if encoding != "utf8" {
		return nil, errors.New("only UTF-8 Codama strings are supported")
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return nil, errors.New("Codama string must be a JSON string")
	}
	if !utf8.ValidString(text) || len(text) > maxEncodedArgumentBytes {
		return nil, errors.New("Codama string is invalid or oversized")
	}
	return []byte(text), nil
}

func decodeRawCodamaString(encoding string, data []byte) (string, error) {
	if encoding != "utf8" || !utf8.Valid(data) {
		return "", errors.New("Codama string encoding or UTF-8 is invalid")
	}
	return string(data), nil
}

func encodeCodamaStruct(
	fields []codamaField,
	value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if fields == nil || len(fields) > maxArgumentItems {
		return nil, errors.New("Codama struct fields are invalid or oversized")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || len(object) != len(fields) {
		return nil, errors.New("Codama struct value does not match its fields")
	}
	var encoded []byte
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		folded := strings.ToLower(field.Name)
		if field.Kind != "structFieldTypeNode" || !validIdentifier(field.Name) || len(field.Type) == 0 {
			return nil, errors.New("Codama struct field is invalid")
		}
		if _, exists := seen[folded]; exists {
			return nil, errors.New("Codama struct fields are ambiguous")
		}
		item, ok := object[field.Name]
		if !ok {
			return nil, errors.New("Codama struct value does not match its fields")
		}
		part, err := encodeCodamaValue(field.Type, item, definitions, depth+1)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, part...)
		seen[folded] = struct{}{}
		delete(object, field.Name)
	}
	if len(object) != 0 || len(encoded) > maxEncodedArgumentBytes {
		return nil, errors.New("Codama struct value is invalid or oversized")
	}
	return encoded, nil
}

func (decoder *borshDecoder) decodeCodamaStruct(fields []codamaField, depth int) (any, error) {
	if fields == nil || len(fields) > maxArgumentItems {
		return nil, errors.New("Codama struct fields are invalid or oversized")
	}
	result := make(map[string]any, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		folded := strings.ToLower(field.Name)
		if field.Kind != "structFieldTypeNode" || !validIdentifier(field.Name) || len(field.Type) == 0 {
			return nil, errors.New("Codama struct field is invalid")
		}
		if _, exists := seen[folded]; exists {
			return nil, errors.New("Codama struct fields are ambiguous")
		}
		value, err := decoder.decodeCodamaValue(field.Type, depth+1)
		if err != nil {
			return nil, err
		}
		seen[folded] = struct{}{}
		result[field.Name] = value
	}
	return result, nil
}

func encodeCodamaTuple(
	items []json.RawMessage,
	value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	var values []json.RawMessage
	if items == nil || len(items) > maxArgumentItems || json.Unmarshal(value, &values) != nil || len(values) != len(items) {
		return nil, errors.New("Codama tuple value does not match its items")
	}
	var encoded []byte
	for index, item := range items {
		part, err := encodeCodamaValue(item, values[index], definitions, depth+1)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, part...)
	}
	return encoded, nil
}

func (decoder *borshDecoder) decodeCodamaTuple(items []json.RawMessage, depth int) (any, error) {
	if items == nil || len(items) > maxArgumentItems {
		return nil, errors.New("Codama tuple items are invalid or oversized")
	}
	result := make([]any, len(items))
	for index, item := range items {
		value, err := decoder.decodeCodamaValue(item, depth+1)
		if err != nil {
			return nil, err
		}
		result[index] = value
	}
	return result, nil
}

func encodeCodamaArray(
	item, rawCount, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	var values []json.RawMessage
	if len(item) == 0 || json.Unmarshal(value, &values) != nil || len(values) > maxArgumentItems {
		return nil, errors.New("Codama array value is invalid or oversized")
	}
	prefix, expected, remainder, err := encodeCodamaCount(rawCount, len(values))
	if err != nil || !remainder && expected != len(values) {
		return nil, errors.New("Codama array count does not match its value")
	}
	encoded := append([]byte(nil), prefix...)
	for _, value := range values {
		part, err := encodeCodamaValue(item, value, definitions, depth+1)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, part...)
	}
	return encoded, nil
}

func (decoder *borshDecoder) decodeCodamaArray(item, rawCount json.RawMessage, depth int) (any, error) {
	count, remainder, err := decoder.decodeCodamaCount(rawCount)
	if err != nil {
		return nil, err
	}
	result := make([]any, 0, count)
	for remainder && decoder.offset < len(decoder.data) || !remainder && len(result) < count {
		if len(result) >= maxArgumentItems {
			return nil, errors.New("Codama array exceeds 4096 items")
		}
		before := decoder.offset
		value, err := decoder.decodeCodamaValue(item, depth+1)
		if err != nil {
			return nil, err
		}
		if decoder.offset == before {
			return nil, errors.New("Codama remainder array item consumed no bytes")
		}
		result = append(result, value)
	}
	return result, nil
}

func encodeCodamaCount(raw json.RawMessage, actual int) ([]byte, int, bool, error) {
	var count struct {
		Kind   string          `json:"kind"`
		Value  int             `json:"value"`
		Prefix json.RawMessage `json:"prefix"`
	}
	if json.Unmarshal(raw, &count) != nil {
		return nil, 0, false, errors.New("Codama array count is invalid")
	}
	switch count.Kind {
	case "fixedCountNode":
		if count.Value < 0 || count.Value > maxArgumentItems {
			return nil, 0, false, errors.New("Codama fixed array count is invalid")
		}
		return nil, count.Value, false, nil
	case "prefixedCountNode":
		prefix, err := encodeCodamaUnsigned(count.Prefix, actual)
		return prefix, actual, false, err
	case "remainderCountNode":
		return nil, actual, true, nil
	default:
		return nil, 0, false, errors.New("Codama array count kind is unsupported")
	}
}

func (decoder *borshDecoder) decodeCodamaCount(raw json.RawMessage) (int, bool, error) {
	var count struct {
		Kind   string          `json:"kind"`
		Value  int             `json:"value"`
		Prefix json.RawMessage `json:"prefix"`
	}
	if json.Unmarshal(raw, &count) != nil {
		return 0, false, errors.New("Codama array count is invalid")
	}
	switch count.Kind {
	case "fixedCountNode":
		if count.Value < 0 || count.Value > maxArgumentItems {
			return 0, false, errors.New("Codama fixed array count is invalid")
		}
		return count.Value, false, nil
	case "prefixedCountNode":
		value, err := decoder.decodeCodamaUnsigned(count.Prefix)
		if err != nil || value > maxArgumentItems {
			return 0, false, errors.New("Codama prefixed array count is invalid or oversized")
		}
		return value, false, nil
	case "remainderCountNode":
		return 0, true, nil
	default:
		return 0, false, errors.New("Codama array count kind is unsupported")
	}
}

func encodeCodamaOption(
	inner, prefix, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	if len(prefix) == 0 {
		prefix = json.RawMessage(`{"kind":"numberTypeNode","format":"u8","endian":"le"}`)
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return encodeCodamaUnsigned(prefix, 0)
	}
	tag, err := encodeCodamaUnsigned(prefix, 1)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeCodamaValue(inner, value, definitions, depth+1)
	return append(tag, encoded...), err
}

func (decoder *borshDecoder) decodeCodamaOption(inner, prefix json.RawMessage, depth int) (any, error) {
	if len(prefix) == 0 {
		prefix = json.RawMessage(`{"kind":"numberTypeNode","format":"u8","endian":"le"}`)
	}
	tag, err := decoder.decodeCodamaUnsigned(prefix)
	if err != nil {
		return nil, err
	}
	switch tag {
	case 0:
		return nil, nil
	case 1:
		return decoder.decodeCodamaValue(inner, depth+1)
	default:
		return nil, errors.New("Codama option tag is invalid")
	}
}

func encodeCodamaFixed(
	inner, rawSize, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	size, err := codamaFixedSize(rawSize)
	if err != nil {
		return nil, err
	}
	encoded, err := encodeCodamaValue(inner, value, definitions, depth+1)
	if err != nil {
		return nil, err
	}
	if len(encoded) > size {
		return nil, errors.New("Codama fixed-size value would be truncated")
	}
	return append(encoded, make([]byte, size-len(encoded))...), nil
}

func (decoder *borshDecoder) decodeCodamaFixed(inner, rawSize json.RawMessage, depth int) (any, error) {
	size, err := codamaFixedSize(rawSize)
	if err != nil {
		return nil, err
	}
	data, err := decoder.read(size)
	if err != nil {
		return nil, err
	}
	var child codamaTypeNode
	if json.Unmarshal(inner, &child) == nil {
		switch child.Kind {
		case "stringTypeNode":
			return decodeRawCodamaString(child.Encoding, bytes.TrimRight(data, "\x00"))
		case "bytesTypeNode":
			return base64.StdEncoding.EncodeToString(data), nil
		}
	}
	sub := borshDecoder{data: data, definitions: decoder.definitions}
	value, err := sub.decodeCodamaValue(inner, depth+1)
	if err != nil {
		return nil, err
	}
	if !allZero(data[sub.offset:]) {
		return nil, errors.New("Codama fixed-size padding is not zero")
	}
	return value, nil
}

func codamaFixedSize(raw json.RawMessage) (int, error) {
	var size int
	if json.Unmarshal(raw, &size) != nil || size < 0 || size > maxEncodedArgumentBytes {
		return 0, errors.New("Codama fixed size is invalid or oversized")
	}
	return size, nil
}

func encodeCodamaSizePrefixed(
	inner, prefix, value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	encoded, err := encodeCodamaValue(inner, value, definitions, depth+1)
	if err != nil {
		return nil, err
	}
	size, err := encodeCodamaUnsigned(prefix, len(encoded))
	if err != nil {
		return nil, err
	}
	return append(size, encoded...), nil
}

func (decoder *borshDecoder) decodeCodamaSizePrefixed(inner, prefix json.RawMessage, depth int) (any, error) {
	size, err := decoder.decodeCodamaUnsigned(prefix)
	if err != nil || size > maxEncodedArgumentBytes {
		return nil, errors.New("Codama size prefix is invalid or oversized")
	}
	data, err := decoder.read(size)
	if err != nil {
		return nil, err
	}
	sub := borshDecoder{data: data, definitions: decoder.definitions}
	value, err := sub.decodeCodamaValue(inner, depth+1)
	if err != nil {
		return nil, err
	}
	if sub.offset != len(data) {
		return nil, errors.New("Codama size-prefixed value contains trailing bytes")
	}
	return value, nil
}

func encodeCodamaEnum(
	size json.RawMessage,
	variants []codamaVariant,
	value json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	tags, err := codamaVariantTags(variants)
	if err != nil {
		return nil, err
	}
	var selected map[string]json.RawMessage
	if json.Unmarshal(value, &selected) != nil || len(selected) != 1 {
		return nil, errors.New("Codama enum value must select one variant")
	}
	for index, variant := range variants {
		payload, ok := selected[variant.Name]
		if !ok {
			continue
		}
		discriminator, err := encodeCodamaUnsigned(size, tags[index])
		if err != nil {
			return nil, err
		}
		body, err := encodeCodamaVariant(variant, payload, definitions, depth+1)
		return append(discriminator, body...), err
	}
	return nil, errors.New("Codama enum value names no variant")
}

func (decoder *borshDecoder) decodeCodamaEnum(size json.RawMessage, variants []codamaVariant, depth int) (any, error) {
	tags, err := codamaVariantTags(variants)
	if err != nil {
		return nil, err
	}
	tag, err := decoder.decodeCodamaUnsigned(size)
	if err != nil {
		return nil, err
	}
	for index, variant := range variants {
		if tags[index] != tag {
			continue
		}
		payload, err := decoder.decodeCodamaVariant(variant, depth+1)
		if err != nil {
			return nil, err
		}
		return map[string]any{variant.Name: payload}, nil
	}
	return nil, errors.New("Codama enum tag is outside the pinned variants")
}

func codamaVariantTags(variants []codamaVariant) ([]int, error) {
	if len(variants) == 0 || len(variants) > maxArgumentItems {
		return nil, errors.New("Codama enum variants are invalid or oversized")
	}
	tags := make([]int, len(variants))
	names := make(map[string]struct{}, len(variants))
	seenTags := make(map[int]struct{}, len(variants))
	for index, variant := range variants {
		folded := strings.ToLower(variant.Name)
		if !validIdentifier(variant.Name) {
			return nil, errors.New("Codama enum variant name is invalid")
		}
		if _, exists := names[folded]; exists {
			return nil, errors.New("Codama enum variants are ambiguous")
		}
		tag, err := rawNonnegativeInt(variant.Discriminator)
		if err != nil {
			return nil, errors.New("Codama enum discriminator is invalid")
		}
		if _, exists := seenTags[tag]; exists {
			return nil, errors.New("Codama enum discriminators are ambiguous")
		}
		names[folded] = struct{}{}
		seenTags[tag] = struct{}{}
		tags[index] = tag
	}
	return tags, nil
}

func encodeCodamaVariant(
	variant codamaVariant,
	payload json.RawMessage,
	definitions map[string]TypeDefinition,
	depth int,
) ([]byte, error) {
	switch variant.Kind {
	case "enumEmptyVariantTypeNode":
		if !bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
			return nil, errors.New("Codama unit enum payload must be null")
		}
		return nil, nil
	case "enumStructVariantTypeNode":
		return encodeCodamaStruct(variant.Fields, payload, definitions, depth+1)
	case "enumTupleVariantTypeNode":
		return encodeCodamaTuple(variant.Items, payload, definitions, depth+1)
	default:
		return nil, errors.New("Codama enum variant kind is unsupported")
	}
}

func (decoder *borshDecoder) decodeCodamaVariant(variant codamaVariant, depth int) (any, error) {
	switch variant.Kind {
	case "enumEmptyVariantTypeNode":
		return nil, nil
	case "enumStructVariantTypeNode":
		return decoder.decodeCodamaStruct(variant.Fields, depth+1)
	case "enumTupleVariantTypeNode":
		return decoder.decodeCodamaTuple(variant.Items, depth+1)
	default:
		return nil, errors.New("Codama enum variant kind is unsupported")
	}
}

func encodeCodamaUnsigned(raw json.RawMessage, value int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode Codama integer")
	}
	return encodeCodamaUnsignedRaw(raw, encoded)
}

func encodeCodamaUnsignedRaw(rawType, value json.RawMessage) ([]byte, error) {
	var node codamaTypeNode
	if json.Unmarshal(rawType, &node) != nil {
		return nil, errors.New("Codama unsigned integer type is invalid")
	}
	format, err := codamaNumberFormat(node)
	if err != nil || !strings.HasPrefix(format, "u") {
		return nil, errors.New("Codama integer prefix must be unsigned and little-endian")
	}
	return encodePrimitive(format, value)
}

func (decoder *borshDecoder) decodeCodamaUnsigned(rawType json.RawMessage) (int, error) {
	var node codamaTypeNode
	if json.Unmarshal(rawType, &node) != nil {
		return 0, errors.New("Codama unsigned integer type is invalid")
	}
	format, err := codamaNumberFormat(node)
	if err != nil || !strings.HasPrefix(format, "u") {
		return 0, errors.New("Codama integer prefix must be unsigned and little-endian")
	}
	value, err := decoder.decodePrimitive(format)
	if err != nil {
		return 0, err
	}
	switch value := value.(type) {
	case uint64:
		if value > maxEncodedArgumentBytes {
			return 0, errors.New("Codama integer prefix is oversized")
		}
		return int(value), nil
	case string:
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed > maxEncodedArgumentBytes {
			return 0, errors.New("Codama integer prefix is oversized")
		}
		return int(parsed), nil
	default:
		return 0, errors.New("Codama integer prefix is invalid")
	}
}

func rawNonnegativeInt(raw json.RawMessage) (int, error) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return 0, errors.New("integer is invalid")
	}
	value, err := strconv.ParseUint(number.String(), 10, 32)
	if err != nil || value > maxEncodedArgumentBytes {
		return 0, errors.New("integer is invalid or oversized")
	}
	return int(value), nil
}
