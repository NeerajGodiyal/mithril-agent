package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
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
		*instruction == "" || *dataPath == "" {
		return errors.New("program decode-instruction requires the registry, program, interface SHA-256, instruction, and data file")
	}
	report, _, err := programinterface.Load(*registry, *program, *sha256)
	if err != nil {
		return err
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
