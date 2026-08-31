package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
	solanago "github.com/solana-foundation/solana-go/v2"
)

const (
	programLocalProvenance = "local_file"
	programLocalFinality   = "unverified"
)

type programDecodedOutput struct {
	Provenance string              `json:"provenance"`
	Finality   string              `json:"finality"`
	Scope      string              `json:"scope"`
	Current    bool                `json:"current"`
	Cursor     *rootedindex.Cursor `json:"cursor,omitempty"`
	programinterface.DecodedData
}

type decodedProgramInstruction struct {
	Signature   string                         `json:"signature"`
	Cursor      rootedindex.Cursor             `json:"cursor"`
	Version     rootedindex.TransactionVersion `json:"version"`
	MessageHash string                         `json:"message_hash"`
	Location    string                         `json:"location"`
	OuterIndex  int                            `json:"outer_index"`
	InnerIndex  *int                           `json:"inner_index,omitempty"`
	Signed      bool                           `json:"signed"`
	Accounts    []string                       `json:"accounts"`
	Succeeded   bool                           `json:"succeeded"`
	Failure     string                         `json:"failure,omitempty"`
	Provenance  string                         `json:"provenance"`
	Finality    string                         `json:"finality"`
	Scope       string                         `json:"scope"`
	Current     bool                           `json:"current"`
	programinterface.DecodedData
}

func runProgramDecodeAccount(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program decode-account", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
	sha256 := flags.String("sha256", "", "pinned interface SHA-256")
	accountType := flags.String("account-type", "", "exact pinned account type name")
	dataPath := flags.String("data", "", "raw account data file")
	indexDir := flags.String("index-dir", "", "private rooted index directory")
	account := flags.String("account", "", "account address in the rooted index")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program decode-account: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" || *sha256 == "" ||
		*accountType == "" {
		return errors.New("program decode-account requires the registry, program, interface SHA-256, and account type")
	}
	fromFile := *dataPath != "" && *indexDir == "" && *account == ""
	fromIndex := *dataPath == "" && *indexDir != "" && *account != ""
	if !fromFile && !fromIndex {
		return errors.New("program decode-account requires either --data or both --index-dir and --account")
	}
	report, _, err := programinterface.Load(*registry, *program, *sha256)
	if err != nil {
		return err
	}
	var data []byte
	provenance, finality := programLocalProvenance, programLocalFinality
	scope, current := "local_file", false
	var cursor *rootedindex.Cursor
	if fromFile {
		data, err = programinterface.ReadAccountData(*dataPath)
		if err != nil {
			return err
		}
	} else {
		var indexStatus rootedindex.Status
		if *workspacePath != "" {
			indexStatus, err = validateProgramWorkspaceIndex(*workspacePath, *indexDir, "state")
			if err != nil {
				return err
			}
		} else {
			indexStatus, err = rootedindex.ReadCompleteStatus(*indexDir)
			if err != nil {
				return err
			}
		}
		scope, current = programAccountIndexScope(indexStatus.Filter, *account)
		results, queryErr := rootedindex.QueryAccounts(*indexDir, rootedindex.Query{
			Account: *account, Limit: 1, IncludeData: true,
		})
		if queryErr != nil {
			return queryErr
		}
		if len(results) != 1 {
			return errors.New("rooted index has no matching account data for that exact address")
		}
		if results[0].Tombstone {
			return errors.New("newest matching rooted account record is a tombstone")
		}
		if results[0].Owner != report.Program {
			return errors.New("rooted account owner does not match the pinned program")
		}
		data = results[0].Data
		provenance, finality = indexStatus.Provenance, indexStatus.Finality
		cursor = &results[0].Cursor
	}
	decoded, err := programinterface.DecodeAccount(report, *accountType, data)
	if err != nil {
		return err
	}
	view := programDecodedOutput{
		Provenance: provenance, Finality: finality, Scope: scope, Current: current,
		Cursor: cursor, DecodedData: decoded,
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(view)
	}
	if _, err := fmt.Fprintf(output,
		"Account data decoded\nProgram: %s\nInterface SHA-256: %s\nProvenance: %s\nFinality: %s\nScope: %s\nCurrent-state proof: %t\nAccount type: %s\nData SHA-256: %s\nBytes: %d\nValue:\n",
		report.Program, report.SHA256, view.Provenance, view.Finality,
		view.Scope, view.Current, decoded.Name, decoded.SHA256, decoded.Bytes); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(decoded.Value)
}

func programAccountIndexScope(filter rootedindex.Filter, account string) (string, bool) {
	if filter.Owner != "" {
		return "owner_matching_history", false
	}
	if filter.Mention != "" || filter.Account != "" && filter.Account != account {
		return "filtered_history", false
	}
	return "current_account_state", true
}

func runProgramDecodeInstruction(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program decode-instruction", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
	sha256 := flags.String("sha256", "", "pinned interface SHA-256")
	instruction := flags.String("instruction", "", "exact pinned instruction name")
	dataPath := flags.String("data", "", "raw instruction data file")
	indexDir := flags.String("index-dir", "", "private rooted activity index directory")
	signature := flags.String("signature", "", "exact rooted transaction signature")
	outerIndex := flags.Int("outer-index", -1, "zero-based outer instruction index")
	innerGroup := flags.Int("inner-group", -1, "zero-based parent outer instruction index for a CPI")
	innerIndex := flags.Int("inner-index", -1, "zero-based instruction index inside the CPI group")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program decode-instruction: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" || *sha256 == "" ||
		*instruction == "" {
		return errors.New("program decode-instruction requires the registry, program, interface SHA-256, and instruction")
	}
	fromFile := *dataPath != "" && *indexDir == "" && *signature == "" &&
		*outerIndex == -1 && *innerGroup == -1 && *innerIndex == -1
	fromOuter := *dataPath == "" && *indexDir != "" && *signature != "" &&
		*outerIndex >= 0 && *innerGroup == -1 && *innerIndex == -1
	fromInner := *dataPath == "" && *indexDir != "" && *signature != "" &&
		*outerIndex == -1 && *innerGroup >= 0 && *innerIndex >= 0
	fromIndex := fromOuter || fromInner
	if !fromFile && !fromIndex {
		return errors.New("program decode-instruction requires either --data, a rooted --outer-index, or rooted --inner-group and --inner-index")
	}
	report, _, err := programinterface.Load(*registry, *program, *sha256)
	if err != nil {
		return err
	}
	if fromIndex {
		transaction, status, err := readExactRootedTransaction(*workspacePath, *indexDir, *signature)
		if err != nil {
			return err
		}
		var decoded decodedProgramInstruction
		if fromOuter {
			decoded, err = decodeRootedProgramInstruction(report, *instruction, transaction, *outerIndex, status.Provenance, status.Finality)
		} else {
			decoded, err = decodeRootedProgramInnerInstruction(report, *instruction, transaction, *innerGroup, *innerIndex, status.Provenance, status.Finality)
		}
		if err != nil {
			return err
		}
		if *jsonOutput {
			return json.NewEncoder(output).Encode(decoded)
		}
		outcome := "succeeded"
		if !decoded.Succeeded {
			outcome = "failed"
			if decoded.Failure != "" {
				outcome = fmt.Sprintf("failed: %q", decoded.Failure)
			}
		}
		path := fmt.Sprintf("outer %d", decoded.OuterIndex)
		if decoded.InnerIndex != nil {
			path = fmt.Sprintf("CPI group %d, inner %d", decoded.OuterIndex, *decoded.InnerIndex)
		}
		if _, err := fmt.Fprintf(output,
			"Rooted instruction decoded\nProgram: %s\nInterface SHA-256: %s\nTransaction: %s\nVersion: %s\nMessage hash: %s\nOutcome: %s\nRooted cursor: %s\nLocation: %s\nSigned outer message: %t\nProvenance: %s\nFinality: %s\nInstruction: %s\nData SHA-256: %s\nBytes: %d\nAccounts:\n",
			report.Program, report.SHA256, decoded.Signature, decoded.Version,
			decoded.MessageHash, outcome, decoded.Cursor, path, decoded.Signed,
			decoded.Provenance, decoded.Finality, decoded.Name, decoded.SHA256,
			decoded.Bytes); err != nil {
			return err
		}
		for _, account := range decoded.Accounts {
			if _, err := fmt.Fprintf(output, "  - %s\n", account); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(output, "Arguments:"); err != nil {
			return err
		}
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(decoded.Value)
	}
	data, err := programinterface.ReadInstructionData(*dataPath)
	if err != nil {
		return err
	}
	decoded, err := programinterface.DecodeInstruction(report, *instruction, data)
	if err != nil {
		return err
	}
	view := programDecodedOutput{
		Provenance: programLocalProvenance, Finality: programLocalFinality,
		DecodedData: decoded,
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(view)
	}
	if _, err := fmt.Fprintf(output,
		"Instruction data decoded\nProgram: %s\nInterface SHA-256: %s\nProvenance: %s\nFinality: %s\nInstruction: %s\nData SHA-256: %s\nBytes: %d\nArguments:\n",
		report.Program, report.SHA256, view.Provenance, view.Finality,
		decoded.Name, decoded.SHA256, decoded.Bytes); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(decoded.Value)
}

func decodeRootedProgramInstruction(
	report programinterface.Report,
	name string,
	transaction rootedindex.TransactionResult,
	outerIndex int,
	provenance, finality string,
) (decodedProgramInstruction, error) {
	decodedTransaction, err := solanago.TransactionFromBytes(transaction.Transaction)
	if err != nil {
		return decodedProgramInstruction{}, errors.New("rooted transaction wire is invalid")
	}
	if outerIndex < 0 || outerIndex >= len(decodedTransaction.Message.Instructions) {
		return decodedProgramInstruction{}, errors.New("rooted outer instruction index is out of range")
	}
	instruction := decodedTransaction.Message.Instructions[outerIndex]
	return decodeRootedCompiledInstruction(
		report, name, transaction, "outer", outerIndex, nil, true,
		instruction.ProgramIDIndex, instruction.Accounts, []byte(instruction.Data), provenance, finality,
	)
}

func decodeRootedProgramInnerInstruction(
	report programinterface.Report,
	name string,
	transaction rootedindex.TransactionResult,
	innerGroup, innerIndex int,
	provenance, finality string,
) (decodedProgramInstruction, error) {
	if innerGroup < 0 || innerGroup > 255 || innerIndex < 0 {
		return decodedProgramInstruction{}, errors.New("rooted inner instruction index is out of range")
	}
	for _, group := range transaction.Inner {
		if int(group.Index) != innerGroup {
			continue
		}
		if innerIndex >= len(group.Instructions) {
			return decodedProgramInstruction{}, errors.New("rooted inner instruction index is out of range")
		}
		instruction := group.Instructions[innerIndex]
		return decodeRootedCompiledInstruction(
			report, name, transaction, "inner", innerGroup, &innerIndex, false,
			instruction.ProgramIDIndex, instruction.Accounts, instruction.Data, provenance, finality,
		)
	}
	return decodedProgramInstruction{}, errors.New("rooted inner instruction group is absent")
}

func decodeRootedCompiledInstruction(
	report programinterface.Report,
	name string,
	transaction rootedindex.TransactionResult,
	location string,
	outerIndex int,
	innerIndex *int,
	signed bool,
	programIDIndex uint16,
	accountIndexes []uint16,
	data []byte,
	provenance, finality string,
) (decodedProgramInstruction, error) {
	if int(programIDIndex) >= len(transaction.AccountKeys) ||
		transaction.AccountKeys[programIDIndex] != report.Program {
		return decodedProgramInstruction{}, errors.New("rooted instruction does not invoke the pinned program")
	}
	accounts := make([]string, len(accountIndexes))
	for index, accountIndex := range accountIndexes {
		if int(accountIndex) >= len(transaction.AccountKeys) {
			return decodedProgramInstruction{}, errors.New("rooted instruction account index is invalid")
		}
		accounts[index] = transaction.AccountKeys[accountIndex]
	}
	decode := programinterface.DecodeInstruction
	if innerIndex != nil {
		decode = programinterface.DecodeCPIInstruction
	}
	decoded, err := decode(report, name, data)
	if err != nil {
		return decodedProgramInstruction{}, err
	}
	return decodedProgramInstruction{
		Signature: transaction.Signature, Cursor: transaction.Cursor, Version: transaction.Version,
		MessageHash: transaction.MessageHash, Location: location, OuterIndex: outerIndex,
		InnerIndex: innerIndex, Signed: signed, Accounts: accounts,
		Succeeded: transaction.Succeeded, Failure: transaction.Failure,
		Provenance: provenance, Finality: finality, Scope: "rooted_" + location + "_instruction",
		DecodedData: decoded,
	}, nil
}
