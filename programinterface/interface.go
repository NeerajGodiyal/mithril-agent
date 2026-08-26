// Package programinterface validates and pins Solana program IDLs without
// loading a wallet or contacting a network.
package programinterface

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	MaxIDLBytes        = 1 << 20
	maxInstructions    = 1024
	maxInstructionArgs = 256
	maxAccounts        = 256
	maxAccountDepth    = 16
	maxTypeDefinitions = 1024
)

type Report struct {
	Program      string           `json:"program"`
	SHA256       string           `json:"sha256"`
	Name         string           `json:"name,omitempty"`
	Version      string           `json:"version,omitempty"`
	Spec         string           `json:"spec,omitempty"`
	Instructions []Instruction    `json:"instructions"`
	Types        []TypeDefinition `json:"types,omitempty"`
	Accounts     []DataDefinition `json:"accounts,omitempty"`
	Events       []DataDefinition `json:"events,omitempty"`
}

type Instruction struct {
	Name                     string     `json:"name"`
	Discriminator            string     `json:"discriminator"`
	Accounts                 []Account  `json:"accounts,omitempty"`
	Args                     []Argument `json:"args,omitempty"`
	DynamicRemainingAccounts bool       `json:"dynamic_remaining_accounts,omitempty"`
}

// Argument retains one pinned instruction argument's name and exact IDL type.
type Argument struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

// TypeDefinition retains one pinned user-defined IDL type for deterministic
// argument encoding and later decoding.
type TypeDefinition struct {
	Name          string            `json:"name"`
	Serialization json.RawMessage   `json:"serialization,omitempty"`
	Generics      []json.RawMessage `json:"generics,omitempty"`
	Type          json.RawMessage   `json:"type"`
}

// DataDefinition binds an account or event name to its exact discriminator.
type DataDefinition struct {
	Name          string `json:"name"`
	Discriminator string `json:"discriminator"`
	Size          *int   `json:"size,omitempty"`
}

type Account struct {
	Name       string `json:"name"`
	Writable   bool   `json:"writable,omitempty"`
	Signer     bool   `json:"signer,omitempty"`
	SignerMode string `json:"signer_mode,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
	Address    string `json:"address,omitempty"`
}

type PinResult struct {
	Report
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

type rawIDL struct {
	Kind         string              `json:"kind"`
	Standard     string              `json:"standard"`
	Address      string              `json:"address"`
	Metadata     *rawMetadata        `json:"metadata"`
	Instructions []rawInstruction    `json:"instructions"`
	Types        []rawTypeDefinition `json:"types"`
	Accounts     []rawDataDefinition `json:"accounts"`
	Events       []rawDataDefinition `json:"events"`
}

type rawMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Spec    string `json:"spec"`
}

type rawInstruction struct {
	Name          string       `json:"name"`
	Discriminator []byte       `json:"discriminator"`
	Accounts      []rawAccount `json:"accounts"`
	Args          []rawField   `json:"args"`
}

type rawAccount struct {
	Name     string        `json:"name"`
	Writable bool          `json:"writable"`
	Signer   bool          `json:"signer"`
	Optional bool          `json:"optional"`
	Address  string        `json:"address"`
	Accounts *[]rawAccount `json:"accounts"`
}

type rawField struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

type rawTypeDefinition struct {
	Name          string            `json:"name"`
	Serialization json.RawMessage   `json:"serialization"`
	Generics      []json.RawMessage `json:"generics"`
	Type          json.RawMessage   `json:"type"`
}

type rawDataDefinition struct {
	Name          string `json:"name"`
	Discriminator []byte `json:"discriminator"`
}

// Inspect validates a modern Solana IDL and binds its exact bytes to the
// expected program. Unknown fields are retained in the pinned bytes and
// deliberately tolerated so optional spec fields do not break inspection.
func Inspect(data []byte, expectedProgram string) (Report, error) {
	if len(data) == 0 || len(data) > MaxIDLBytes {
		return Report{}, errors.New("IDL is empty or exceeds 1 MiB")
	}
	if _, err := solana.Decode32(expectedProgram); err != nil {
		return Report{}, errors.New("expected program is not a canonical Solana address")
	}
	if err := strictjson.Validate(data); err != nil {
		return Report{}, fmt.Errorf("IDL JSON: %w", err)
	}
	var idl rawIDL
	if err := json.Unmarshal(data, &idl); err != nil {
		return Report{}, errors.New("IDL JSON does not match the Solana IDL shape")
	}
	if idl.Address == "" {
		if idl.Kind == "rootNode" && idl.Standard == "codama" {
			return inspectCodama(data, expectedProgram)
		}
		return Report{}, errors.New("IDL has no top-level address; convert the legacy Anchor IDL before pinning")
	}
	if _, err := solana.Decode32(idl.Address); err != nil {
		return Report{}, errors.New("IDL address is not a canonical Solana address")
	}
	if idl.Address != expectedProgram {
		return Report{}, errors.New("IDL address does not match the expected program")
	}
	if idl.Metadata == nil || idl.Metadata.Spec != "0.1.0" {
		return Report{}, errors.New("IDL metadata must declare Solana IDL spec 0.1.0")
	}
	if idl.Instructions == nil || len(idl.Instructions) > maxInstructions {
		return Report{}, errors.New("IDL instruction count is outside the supported range")
	}
	if len(idl.Types) > maxTypeDefinitions {
		return Report{}, errors.New("IDL type definition count is outside the supported range")
	}

	sum := sha256.Sum256(data)
	report := Report{
		Program:      expectedProgram,
		SHA256:       hex.EncodeToString(sum[:]),
		Name:         idl.Metadata.Name,
		Version:      idl.Metadata.Version,
		Spec:         idl.Metadata.Spec,
		Instructions: make([]Instruction, 0, len(idl.Instructions)),
		Types:        make([]TypeDefinition, 0, len(idl.Types)),
		Accounts:     make([]DataDefinition, 0, len(idl.Accounts)),
		Events:       make([]DataDefinition, 0, len(idl.Events)),
	}
	seenTypes := make(map[string]string, len(idl.Types))
	for _, definition := range idl.Types {
		if !validIdentifier(definition.Name) || len(definition.Type) == 0 {
			return Report{}, errors.New("IDL contains an invalid type definition")
		}
		folded := strings.ToLower(definition.Name)
		if _, exists := seenTypes[folded]; exists {
			return Report{}, errors.New("IDL contains duplicate type definition names")
		}
		seenTypes[folded] = definition.Name
		report.Types = append(report.Types, TypeDefinition{
			Name:          definition.Name,
			Serialization: append(json.RawMessage(nil), definition.Serialization...),
			Generics:      cloneRawMessages(definition.Generics),
			Type:          append(json.RawMessage(nil), definition.Type...),
		})
	}
	var err error
	report.Accounts, err = inspectDataDefinitions(idl.Accounts, seenTypes)
	if err != nil {
		return Report{}, fmt.Errorf("IDL accounts: %w", err)
	}
	report.Events, err = inspectDataDefinitions(idl.Events, seenTypes)
	if err != nil {
		return Report{}, fmt.Errorf("IDL events: %w", err)
	}
	seenNames := make(map[string]struct{}, len(idl.Instructions))
	seenDiscriminators := make(map[string]struct{}, len(idl.Instructions))
	for _, raw := range idl.Instructions {
		if !validIdentifier(raw.Name) {
			return Report{}, errors.New("IDL contains an invalid instruction name")
		}
		folded := strings.ToLower(raw.Name)
		if _, exists := seenNames[folded]; exists {
			return Report{}, errors.New("IDL contains duplicate instruction names")
		}
		seenNames[folded] = struct{}{}
		if raw.Discriminator == nil {
			return Report{}, fmt.Errorf("instruction %q has no discriminator", raw.Name)
		}
		if raw.Accounts == nil || raw.Args == nil {
			return Report{}, fmt.Errorf("instruction %q must declare accounts and arguments", raw.Name)
		}
		discriminator := hex.EncodeToString(raw.Discriminator)
		if _, exists := seenDiscriminators[discriminator]; exists {
			return Report{}, errors.New("IDL contains duplicate instruction discriminators")
		}
		seenDiscriminators[discriminator] = struct{}{}
		accounts, err := flattenAccounts(raw.Accounts, "", 0)
		if err != nil {
			return Report{}, fmt.Errorf("instruction %q accounts: %w", raw.Name, err)
		}
		if len(accounts) > maxAccounts {
			return Report{}, fmt.Errorf("instruction %q has too many accounts", raw.Name)
		}
		if len(raw.Args) > maxInstructionArgs {
			return Report{}, fmt.Errorf("instruction %q has too many arguments", raw.Name)
		}
		args := make([]Argument, 0, len(raw.Args))
		seenArgs := make(map[string]struct{}, len(raw.Args))
		for _, arg := range raw.Args {
			if !validIdentifier(arg.Name) {
				return Report{}, fmt.Errorf("instruction %q contains an invalid argument name", raw.Name)
			}
			folded := strings.ToLower(arg.Name)
			if _, exists := seenArgs[folded]; exists {
				return Report{}, fmt.Errorf("instruction %q contains duplicate argument names", raw.Name)
			}
			seenArgs[folded] = struct{}{}
			if len(arg.Type) == 0 {
				return Report{}, fmt.Errorf("instruction %q argument %q has no type", raw.Name, arg.Name)
			}
			args = append(args, Argument{Name: arg.Name, Type: append(json.RawMessage(nil), arg.Type...)})
		}
		report.Instructions = append(report.Instructions, Instruction{
			Name: raw.Name, Discriminator: discriminator, Accounts: accounts, Args: args,
		})
	}
	return report, nil
}

func inspectDataDefinitions(
	definitions []rawDataDefinition,
	knownTypes map[string]string,
) ([]DataDefinition, error) {
	if len(definitions) > maxTypeDefinitions {
		return nil, errors.New("definition count is outside the supported range")
	}
	result := make([]DataDefinition, 0, len(definitions))
	seenNames := make(map[string]struct{}, len(definitions))
	seenDiscriminators := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		folded := strings.ToLower(definition.Name)
		if !validIdentifier(definition.Name) || definition.Discriminator == nil {
			return nil, errors.New("definition name or discriminator is invalid")
		}
		if typeName, ok := knownTypes[folded]; !ok || typeName != definition.Name {
			return nil, errors.New("definition has no matching pinned type")
		}
		discriminator := hex.EncodeToString(definition.Discriminator)
		if _, exists := seenNames[folded]; exists {
			return nil, errors.New("definition names are duplicated")
		}
		if _, exists := seenDiscriminators[discriminator]; exists {
			return nil, errors.New("definition discriminators are duplicated")
		}
		seenNames[folded] = struct{}{}
		seenDiscriminators[discriminator] = struct{}{}
		result = append(result, DataDefinition{Name: definition.Name, Discriminator: discriminator})
	}
	return result, nil
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	if values == nil {
		return nil
	}
	result := make([]json.RawMessage, len(values))
	for index := range values {
		result[index] = append(json.RawMessage(nil), values[index]...)
	}
	return result
}

func flattenAccounts(accounts []rawAccount, prefix string, depth int) ([]Account, error) {
	if depth > maxAccountDepth {
		return nil, errors.New("account groups are nested too deeply")
	}
	result := make([]Account, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if !validIdentifier(account.Name) {
			return nil, errors.New("invalid account name")
		}
		folded := strings.ToLower(account.Name)
		if _, exists := seen[folded]; exists {
			return nil, errors.New("duplicate account names")
		}
		seen[folded] = struct{}{}
		name := account.Name
		if prefix != "" {
			name = prefix + "." + name
		}
		if account.Accounts != nil {
			if account.Writable || account.Signer {
				return nil, errors.New("account group declares leaf privileges")
			}
			nested, err := flattenAccounts(*account.Accounts, name, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, nested...)
			continue
		}
		if account.Address != "" {
			if _, err := solana.Decode32(account.Address); err != nil {
				return nil, errors.New("fixed account address is invalid")
			}
		}
		result = append(result, Account{
			Name: name, Writable: account.Writable, Signer: account.Signer,
			Optional: account.Optional, Address: account.Address,
		})
	}
	return result, nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := range len(value) {
		char := value[index]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' ||
			index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

// Read reads one stable regular file. IDLs are public artifacts, so ordinary
// read permissions are allowed, but symlinks and concurrent replacements are
// refused before the bytes can be pinned.
func Read(path string) ([]byte, error) {
	return readStableRegular(path, "IDL", MaxIDLBytes)
}

// ReadAccountData reads one stable regular account-data file within Solana's
// protocol limit. It is separate from Read so a large account cannot be used
// where a bounded IDL is expected.
func ReadAccountData(path string) ([]byte, error) {
	return readStableRegular(path, "account data", MaxAccountDataBytes)
}

// ReadInstructionData reads one stable raw instruction-data file within the
// Solana transaction packet limit.
func ReadInstructionData(path string) ([]byte, error) {
	return readStableRegular(path, "instruction data", MaxInstructionDataBytes)
}

func readStableRegular(path, label string, limit int) ([]byte, error) {
	if path == "" {
		return nil, errors.New(label + " path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("resolve " + label + " path")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("inspect " + label + " file")
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errors.New(label + " must be a regular file, not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open " + label + " file")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New(label + " changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("read " + label + " within its size limit")
	}
	final, err := file.Stat()
	if err != nil || final.Size() != after.Size() ||
		!final.ModTime().Equal(after.ModTime()) || final.Mode() != after.Mode() {
		return nil, errors.New(label + " changed while reading")
	}
	return data, nil
}

// Pin stores exact validated bytes at <registry>/<program>/<sha256>.json. The
// content address is immutable; pinning the same bytes again is idempotent.
func Pin(registry, expectedProgram string, data []byte) (PinResult, error) {
	report, err := Inspect(data, expectedProgram)
	if err != nil {
		return PinResult{}, err
	}
	if registry == "" {
		return PinResult{}, errors.New("registry path is required")
	}
	registry, err = filepath.Abs(registry)
	if err != nil {
		return PinResult{}, errors.New("resolve registry path")
	}
	programDirectory := filepath.Join(filepath.Clean(registry), expectedProgram)
	if err := os.MkdirAll(programDirectory, 0o700); err != nil {
		return PinResult{}, errors.New("create program interface registry")
	}
	path := filepath.Join(programDirectory, report.SHA256+".json")
	result := PinResult{Report: report, Path: path, Created: true}
	if err := securefile.CreatePrivate(path, data, MaxIDLBytes); err == nil {
		return result, nil
	}
	existing, readErr := securefile.ReadPrivate(path, MaxIDLBytes)
	if readErr != nil || !bytes.Equal(existing, data) {
		return PinResult{}, errors.New("pinned interface path already exists with different or unreadable content")
	}
	result.Created = false
	return result, nil
}

// Load revalidates an immutable pin and its requested content hash.
func Load(registry, expectedProgram, expectedSHA256 string) (Report, string, error) {
	if registry == "" {
		return Report{}, "", errors.New("registry path is required")
	}
	if _, err := solana.Decode32(expectedProgram); err != nil {
		return Report{}, "", errors.New("expected program is not a canonical Solana address")
	}
	if len(expectedSHA256) != sha256.Size*2 || strings.ToLower(expectedSHA256) != expectedSHA256 {
		return Report{}, "", errors.New("interface SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return Report{}, "", errors.New("interface SHA-256 must be 64 lowercase hexadecimal characters")
	}
	registry, err := filepath.Abs(registry)
	if err != nil {
		return Report{}, "", errors.New("resolve registry path")
	}
	path := filepath.Join(filepath.Clean(registry), expectedProgram, expectedSHA256+".json")
	data, err := securefile.ReadPrivate(path, MaxIDLBytes)
	if err != nil {
		return Report{}, "", errors.New("read pinned interface")
	}
	report, err := Inspect(data, expectedProgram)
	if err != nil {
		return Report{}, "", errors.New("pinned interface no longer validates")
	}
	if report.SHA256 != expectedSHA256 {
		return Report{}, "", errors.New("pinned interface hash does not match its requested identity")
	}
	return report, path, nil
}
