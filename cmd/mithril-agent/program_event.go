package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/programinterface"
	"github.com/Overclock-Validator/mithril-agent/rootedindex"
)

type decodedProgramEvent struct {
	Signature  string             `json:"signature"`
	Cursor     rootedindex.Cursor `json:"cursor"`
	LogIndex   int                `json:"log_index"`
	Provenance string             `json:"provenance"`
	Finality   string             `json:"finality"`
	programinterface.DecodedData
}

func runProgramDecodeEvent(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("program decode-event", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspacePath := flags.String("workspace", "", "program workspace.json path")
	registry := flags.String("registry", "", "local program interface registry")
	program := flags.String("program", "", "expected Solana program address")
	sha256 := flags.String("sha256", "", "pinned interface SHA-256")
	eventType := flags.String("event-type", "", "exact pinned event type name")
	indexDir := flags.String("index-dir", "", "private rooted index directory")
	signature := flags.String("signature", "", "exact rooted transaction signature")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, programUsage)
			return writeErr
		}
		return err
	}
	if err := applyLocalProgramWorkspace(*workspacePath, registry, program); err != nil {
		return fmt.Errorf("program decode-event: %w", err)
	}
	if flags.NArg() != 0 || *registry == "" || *program == "" || *sha256 == "" ||
		*eventType == "" || *indexDir == "" || *signature == "" {
		return errors.New("program decode-event requires the registry, program, interface SHA-256, event type, index directory, and transaction signature")
	}
	report, _, err := programinterface.Load(*registry, *program, *sha256)
	if err != nil {
		return err
	}
	transaction, indexStatus, err := readExactRootedTransaction(*workspacePath, *indexDir, *signature)
	if err != nil {
		return err
	}
	decoded, err := decodeProgramEvents(
		report, *eventType, transaction, indexStatus.Provenance, indexStatus.Finality,
	)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(output).Encode(decoded)
	}
	if _, err := fmt.Fprintf(output,
		"Program events decoded: %d\nProgram: %s\nInterface SHA-256: %s\nTransaction: %s\nRooted cursor: %s\n",
		len(decoded), report.Program, report.SHA256, transaction.Signature, transaction.Cursor); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	for _, event := range decoded {
		if _, err := fmt.Fprintf(output, "Event %s at log %d:\n", event.Name, event.LogIndex); err != nil {
			return err
		}
		if err := encoder.Encode(event.Value); err != nil {
			return err
		}
	}
	return nil
}

func readExactRootedTransaction(workspacePath, indexDir, signature string) (rootedindex.TransactionResult, rootedindex.Status, error) {
	var status rootedindex.Status
	var err error
	if workspacePath != "" {
		status, err = validateProgramWorkspaceIndex(workspacePath, indexDir, "activity")
	} else {
		status, err = rootedindex.ReadCompleteStatus(indexDir)
	}
	if err != nil {
		return rootedindex.TransactionResult{}, rootedindex.Status{}, err
	}
	transactions, err := rootedindex.QueryTransactions(indexDir, rootedindex.TransactionQuery{
		Signature: signature, Limit: 2, IncludePayload: true,
	})
	if err != nil {
		return rootedindex.TransactionResult{}, rootedindex.Status{}, err
	}
	if len(transactions) != 1 {
		return rootedindex.TransactionResult{}, rootedindex.Status{}, errors.New("rooted index must contain exactly one transaction with that signature")
	}
	return transactions[0], status, nil
}

func decodeProgramEvents(
	report programinterface.Report,
	eventType string,
	transaction rootedindex.TransactionResult,
	provenance, finality string,
) ([]decodedProgramEvent, error) {
	if !transaction.Succeeded {
		return nil, errors.New("rooted transaction failed; event decoding refuses reverted execution")
	}
	discriminator, err := eventDiscriminator(report, eventType)
	if err != nil {
		return nil, err
	}
	if len(discriminator) == 0 {
		return nil, errors.New("indexed event decoding requires a non-empty pinned discriminator")
	}
	if transaction.LogsTruncated {
		return nil, errors.New("rooted transaction logs are truncated; event decoding cannot prove a complete result")
	}
	stack := make([]string, 0, 8)
	decoded := make([]decodedProgramEvent, 0)
	for logIndex, log := range transaction.Logs {
		if program, ok := invokedProgram(log); ok {
			stack = append(stack, program)
			continue
		}
		if program, ok := completedProgram(log); ok {
			if len(stack) == 0 || stack[len(stack)-1] != program {
				return nil, errors.New("rooted transaction program log stack is inconsistent")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		encoded, ok := strings.CutPrefix(log, "Program data: ")
		if !ok || len(stack) == 0 || stack[len(stack)-1] != report.Program {
			continue
		}
		fields := strings.Fields(encoded)
		if len(fields) == 0 {
			return nil, errors.New("requested program emitted invalid base64 event data")
		}
		for _, encodedField := range fields {
			data, err := base64.StdEncoding.DecodeString(encodedField)
			if err != nil {
				return nil, errors.New("requested program emitted invalid base64 event data")
			}
			if !bytes.HasPrefix(data, discriminator) {
				continue
			}
			value, err := programinterface.DecodeEvent(report, eventType, data)
			if err != nil {
				return nil, err
			}
			decoded = append(decoded, decodedProgramEvent{
				Signature: transaction.Signature, Cursor: transaction.Cursor,
				LogIndex: logIndex, Provenance: provenance,
				Finality: finality, DecodedData: value,
			})
		}
	}
	if len(stack) != 0 {
		return nil, errors.New("rooted transaction program log stack is incomplete")
	}
	if len(decoded) == 0 {
		return nil, errors.New("transaction contains no matching event from the requested program")
	}
	return decoded, nil
}

func eventDiscriminator(report programinterface.Report, name string) ([]byte, error) {
	for _, event := range report.Events {
		if event.Name == name {
			value, err := hex.DecodeString(event.Discriminator)
			if err != nil {
				return nil, errors.New("pinned event discriminator is invalid")
			}
			return value, nil
		}
	}
	return nil, errors.New("program interface has no event definition with that exact name")
}

func invokedProgram(log string) (string, bool) {
	fields := strings.Fields(log)
	if len(fields) != 4 || fields[0] != "Program" || fields[2] != "invoke" ||
		len(fields[3]) < 3 || fields[3][0] != '[' || fields[3][len(fields[3])-1] != ']' {
		return "", false
	}
	return fields[1], true
}

func completedProgram(log string) (string, bool) {
	fields := strings.Fields(log)
	if len(fields) < 3 || fields[0] != "Program" ||
		(fields[2] != "success" && fields[2] != "failed:") {
		return "", false
	}
	return fields[1], true
}
