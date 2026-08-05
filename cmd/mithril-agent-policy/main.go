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
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const (
	maxPolicyBytes  = 64 << 10
	maxRequestBytes = 64 << 10
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-policy:", err)
		os.Exit(1)
	}
}

func run(
	args []string,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("mithril-agent-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "private risk policy JSON")
	keypairPath := flags.String("keypair", "", "private risk authority keypair JSON")
	identity := flags.Bool("identity", false, "print the bound public identity")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent-policy --policy PATH --keypair PATH")
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
	var policy policyauthority.Policy
	if err := strictjson.Decode(policyData, &policy); err != nil {
		return errors.New("decode risk policy")
	}
	privateKey, err := signer.LoadKeypair(*keypairPath)
	if err != nil {
		return fmt.Errorf("load risk authority keypair: %w", err)
	}
	defer clear(privateKey)
	if *identity {
		if err := policy.Validate(); err != nil {
			return err
		}
		publicKey, err := riskgrant.PublicKeyHex(privateKey)
		if err != nil || publicKey != policy.TransactionPolicy.RiskAuthorityPublicKey {
			return errors.New("risk authority key does not match policy")
		}
		return json.NewEncoder(output).Encode(struct {
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		}{
			KeyID:     policy.TransactionPolicy.RiskAuthorityKeyID,
			PublicKey: publicKey,
		})
	}
	requestData, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if len(requestData) > maxRequestBytes {
		return errors.New("risk request exceeds 64 KiB")
	}
	var request signer.Request
	if err := strictjson.Decode(requestData, &request); err != nil {
		return errors.New("decode risk request")
	}
	grant, err := policyauthority.Authorize(policy, privateKey, request, now().UTC())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(grant)
}
