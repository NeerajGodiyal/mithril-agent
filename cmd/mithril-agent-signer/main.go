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

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-signer:", err)
		os.Exit(1)
	}
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
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}

func decodeStrictJSON(data []byte, out any) error {
	return strictjson.Decode(data, out)
}
