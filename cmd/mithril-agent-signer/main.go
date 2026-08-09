package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const (
	maxPolicyBytes  = 64 << 10
	maxRequestBytes = 64 << 10
)

// refusalExitCode marks "the policy said no" as distinct from "the signer could
// not run". Both used to exit 1, so a daily cap reached at noon was reported to
// the operator exactly like a crashed binary: the caller discards this process's
// stderr, and the proposer then sat on a built transaction until its blockhash
// aged out and reported THAT instead. One exit code carries the distinction
// without the caller having to trust anything this process writes.
//
// Only genuine REFUSALS use it — a spent daily cap, a closed schedule window.
// AuthorizeAndSign also returns faults: a malformed policy, an unwritable
// authorization ledger, a lock it could not take. Those are not "wait until
// tomorrow", they are "something is broken", and they must not wear the same
// exit code. Errors raised before AuthorizeAndSign can additionally name a
// policy or keypair PATH, and those must stay unread by the caller entirely.
const refusalExitCode = 3

func main() {
	err := run(os.Args[1:], os.Stdin, os.Stdout)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "mithril-agent-signer:", err)
	if signer.IsRefusal(err) {
		os.Exit(refusalExitCode)
	}
	os.Exit(1)
}

func run(args []string, input io.Reader, output io.Writer) error {
	return runAt(args, input, output, time.Now)
}

func runAt(
	args []string,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("mithril-agent-signer", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "private signer policy JSON")
	keypairPath := flags.String("keypair", "", "private Solana keypair JSON")
	identity := flags.Bool("identity", false, "print the bound public identity")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent-signer --policy PATH --keypair PATH")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *keypairPath == "" {
		return errors.New("--policy and --keypair are required")
	}
	policyData, err := securefile.ReadPrivate(*policyPath, maxPolicyBytes)
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	var policy signer.Policy
	if err := decodeStrictJSON(policyData, &policy); err != nil {
		return fmt.Errorf("decode policy: %w", err)
	}
	privateKey, err := signer.LoadKeypair(*keypairPath)
	if err != nil {
		return fmt.Errorf("load keypair: %w", err)
	}
	defer clear(privateKey)
	if *identity {
		if err := policy.Validate(); err != nil {
			return err
		}
		publicKey, err := signer.PublicKey(privateKey)
		if err != nil || publicKey != policy.Source {
			return errors.New("signer key does not match policy")
		}
		return json.NewEncoder(output).Encode(struct {
			PublicKey string `json:"public_key"`
		}{PublicKey: publicKey})
	}
	requestData, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if len(requestData) > maxRequestBytes {
		return errors.New("signing request exceeds 64 KiB")
	}
	var request signer.Request
	if err := decodeStrictJSON(requestData, &request); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	response, err := signer.AuthorizeAndSign(policy, privateKey, request, now())
	if err != nil {
		// Returned unchanged: signer.IsRefusal tells main whether this was the
		// policy declining or the signer failing, and only the former earns the
		// refusal exit code.
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}

func decodeStrictJSON(data []byte, out any) error {
	return strictjson.Decode(data, out)
}
