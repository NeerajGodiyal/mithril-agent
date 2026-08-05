package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
	"github.com/Overclock-Validator/mithril-agent/submitterclient"
)

const (
	maxPolicyBytes  = 64 << 10
	maxRequestBytes = 64 << 10
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-submitter:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("mithril-agent-submitter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "private submitter policy JSON")
	keyPath := flags.String("key", "", "private submitter key JSON")
	identity := flags.Bool("identity", false, "print the bound public identity")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent-submitter --policy PATH --key PATH")
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *keyPath == "" {
		return errors.New("--policy and --key are required")
	}
	policyData, err := securefile.ReadPrivate(*policyPath, maxPolicyBytes)
	if err != nil {
		return fmt.Errorf("read submitter policy: %w", err)
	}
	var policy submitter.Policy
	if err := strictjson.Decode(policyData, &policy); err != nil {
		return errors.New("decode submitter policy")
	}
	privateKey, err := submitter.LoadPrivateKey(*keyPath)
	if err != nil {
		return errors.New("load submitter key")
	}
	if *identity {
		if err := policy.Validate(); err != nil {
			return err
		}
		publicKey, err := sealedtx.PublicKey(privateKey)
		if err != nil || publicKey != policy.SubmitterPublicKey {
			return errors.New("submitter key does not match policy")
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
		return errors.New("submitter request exceeds 64 KiB")
	}
	var request submitterclient.Request
	if err := strictjson.Decode(requestData, &request); err != nil {
		return errors.New("decode submitter request")
	}
	endpoint := os.Getenv("MITHRIL_AGENT_MITHRIL_RPC_URL")
	if endpoint == "" {
		return errors.New("Mithril RPC is not configured")
	}
	node, err := solanarpc.NewMithrilNode(endpoint, nil)
	if err != nil {
		return errors.New("Mithril RPC configuration is invalid")
	}
	gate, err := control.NewStateFile(
		policy.ControlStatePath,
		policy.ProfileFingerprint,
		false,
	)
	if err != nil {
		return errors.New("submitter control configuration is invalid")
	}
	submission, err := submitter.Submit(
		ctx,
		policy,
		privateKey,
		node,
		gate,
		request.SignerResponse,
		request.MinContextSlot,
	)
	if err != nil {
		if errors.Is(err, submitter.ErrControlBlocked) {
			if encodeErr := json.NewEncoder(output).Encode(struct {
				Error string `json:"error"`
			}{Error: "control_blocked"}); encodeErr != nil {
				return encodeErr
			}
		}
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(submission)
}
