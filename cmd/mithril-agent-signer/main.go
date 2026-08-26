package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signertransport"
	"github.com/Overclock-Validator/mithril-agent/turnkeycustody"
)

const (
	maxPolicyBytes  = 64 << 10
	maxRequestBytes = signer.MaxRequestBytes
	maxSocketBytes  = maxRequestBytes + 1024
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

type signingBackend struct {
	walletKey       ed25519.PrivateKey
	remoteSource    string
	signTransaction func(context.Context, signer.TransactionCustodyRequest) ([]byte, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := runContext(ctx, os.Args[1:], os.Stdin, os.Stdout)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "mithril-agent-signer:", err)
	if signer.IsRefusal(err) {
		os.Exit(refusalExitCode)
	}
	os.Exit(1)
}

func runContext(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	return runAtContext(ctx, args, input, output, time.Now)
}

func runAt(
	args []string,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	return runAtContext(context.Background(), args, input, output, now)
}

func runAtContext(
	ctx context.Context,
	args []string,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("signer operation was canceled")
	}
	flags := flag.NewFlagSet("mithril-agent-signer", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "private signer policy JSON")
	keypairPath := flags.String("keypair", "", "private Solana file-key JSON")
	attestationKeypairPath := flags.String("attestation-keypair", "", "private signer attestation keypair JSON")
	turnkeyAPIKeyPath := flags.String("turnkey-api-key", "", "protected Turnkey API private-key file")
	turnkeyAPIPublicKey := flags.String("turnkey-api-public-key", "", "registered Turnkey API public key")
	turnkeyOrganizationID := flags.String("turnkey-organization", "", "Turnkey organization ID")
	turnkeySignWith := flags.String("turnkey-sign-with", "", "Turnkey Solana signing address or private-key ID")
	identity := flags.Bool("identity", false, "print the bound public identity")
	socket := flags.Bool("socket", false, "serve one systemd-activated socket request on stdin/stdout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, "Usage: mithril-agent-signer --policy PATH (--keypair PATH | --turnkey-api-key PATH --turnkey-api-public-key KEY --turnkey-organization ID --turnkey-sign-with SIGNING_RESOURCE) [--attestation-keypair PATH] [--identity|--socket]")
			return writeErr
		}
		return err
	}
	turnkeyFields := 0
	for _, value := range []string{
		*turnkeyAPIKeyPath, *turnkeyAPIPublicKey, *turnkeyOrganizationID, *turnkeySignWith,
	} {
		if value != "" {
			turnkeyFields++
		}
	}
	if flags.NArg() != 0 || *policyPath == "" {
		return errors.New("--policy and exactly one custody backend are required")
	}
	if *keypairPath == "" && turnkeyFields == 0 {
		return errors.New("--keypair or the complete Turnkey custody configuration is required")
	}
	if *keypairPath != "" && turnkeyFields != 0 {
		return errors.New("file-key and Turnkey custody are mutually exclusive")
	}
	if turnkeyFields != 0 && turnkeyFields != 4 {
		return errors.New("Turnkey custody requires API key file, public key, organization, and signing resource")
	}
	if *identity && *socket {
		return errors.New("--identity and --socket are mutually exclusive")
	}
	policyData, err := securefile.ReadPrivate(*policyPath, maxPolicyBytes)
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	var policy signer.Policy
	if err := decodeStrictJSON(policyData, &policy); err != nil {
		return fmt.Errorf("decode policy: %w", err)
	}
	var policyErr error
	if policy.Jupiter != nil {
		policyErr = signer.ValidateJupiterPolicy(policy)
	} else {
		policyErr = policy.ValidateAuthorizationPolicy()
	}
	if policyErr != nil {
		return policyErr
	}
	var request signer.Request
	var requestTime time.Time
	if !*socket && !*identity {
		requestData, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
		if err != nil {
			return err
		}
		defer clear(requestData)
		if len(requestData) > maxRequestBytes {
			return errors.New("signing request exceeds size limit")
		}
		if err := decodeStrictJSON(requestData, &request); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		requestTime = now()
		if err := signer.ValidateScheduleWindowAt(request, requestTime); err != nil {
			return err
		}
		if policy.Jupiter != nil {
			_, err = signer.ValidateJupiterRequest(policy, request)
		} else {
			_, err = signer.ValidateRequest(policy, request)
		}
		if err != nil {
			return err
		}
	}
	backend, err := loadSigningBackend(
		ctx,
		policy,
		*keypairPath,
		*turnkeyAPIKeyPath,
		*turnkeyAPIPublicKey,
		*turnkeyOrganizationID,
		*turnkeySignWith,
	)
	if err != nil {
		return err
	}
	defer clear(backend.walletKey)
	var attestationKey ed25519.PrivateKey
	if *attestationKeypairPath != "" {
		attestationKey, err = signer.LoadKeypair(*attestationKeypairPath)
		if err != nil {
			return fmt.Errorf("load attestation keypair: %w", err)
		}
		defer clear(attestationKey)
	}
	if *socket {
		return runSocketWithBackendContext(ctx, policy, backend, attestationKey, input, output, now)
	}
	if *identity {
		bound, err := signerIdentityWithBackend(policy, backend, attestationKey)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(bound)
	}
	response, err := authorizeAndSignWithBackendContext(
		ctx, policy, backend, attestationKey, request, requestTime,
	)
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

func loadSigningBackend(
	ctx context.Context,
	policy signer.Policy,
	keypairPath, turnkeyAPIKeyPath, turnkeyAPIPublicKey, organizationID, signWith string,
) (signingBackend, error) {
	return loadSigningBackendWithTurnkeyLoader(
		ctx, policy, keypairPath, turnkeyAPIKeyPath, turnkeyAPIPublicKey,
		organizationID, signWith, loadVerifiedTurnkeySigner,
	)
}

type custodySignFunc func(
	context.Context, signer.TransactionCustodyRequest,
) ([]byte, error)

type turnkeySignerLoader func(
	context.Context, string, string, turnkeycustody.Config, string,
) (custodySignFunc, error)

func loadVerifiedTurnkeySigner(
	ctx context.Context,
	path, publicKey string,
	config turnkeycustody.Config,
	expectedAddress string,
) (custodySignFunc, error) {
	custody, err := turnkeycustody.NewVerifiedFromAPIKeyFile(
		ctx, path, publicKey, config, expectedAddress,
	)
	if err != nil {
		return nil, err
	}
	return custody.Sign, nil
}

func loadSigningBackendWithTurnkeyLoader(
	ctx context.Context,
	policy signer.Policy,
	keypairPath, turnkeyAPIKeyPath, turnkeyAPIPublicKey, organizationID, signWith string,
	loadTurnkey turnkeySignerLoader,
) (signingBackend, error) {
	if keypairPath != "" {
		privateKey, err := signer.LoadKeypair(keypairPath)
		if err != nil {
			return signingBackend{}, fmt.Errorf("load keypair: %w", err)
		}
		return signingBackend{walletKey: privateKey}, nil
	}
	if policy.Jupiter == nil {
		return signingBackend{}, errors.New("Turnkey custody requires a Jupiter policy")
	}
	if err := signer.ValidateJupiterPolicy(policy); err != nil {
		return signingBackend{}, err
	}
	config := turnkeycustody.Config{OrganizationID: organizationID, SignWith: signWith}
	if loadTurnkey == nil {
		return signingBackend{}, errors.New("Turnkey signing-resource verification is unavailable")
	}
	signTransaction, err := loadTurnkey(
		ctx, turnkeyAPIKeyPath, turnkeyAPIPublicKey, config, policy.Source,
	)
	if err != nil {
		return signingBackend{}, err
	}
	return signingBackend{
		remoteSource: policy.Source, signTransaction: signTransaction,
	}, nil
}

func runSocketAt(
	policy signer.Policy,
	privateKey ed25519.PrivateKey,
	attestationKey ed25519.PrivateKey,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	return runSocketWithBackend(
		policy, signingBackend{walletKey: privateKey}, attestationKey,
		input, output, now,
	)
}

func runSocketWithBackend(
	policy signer.Policy,
	backend signingBackend,
	attestationKey ed25519.PrivateKey,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	return runSocketWithBackendContext(
		context.Background(), policy, backend, attestationKey, input, output, now,
	)
}

func runSocketWithBackendContext(
	ctx context.Context,
	policy signer.Policy,
	backend signingBackend,
	attestationKey ed25519.PrivateKey,
	input io.Reader,
	output io.Writer,
	now func() time.Time,
) error {
	if ctx == nil || ctx.Err() != nil {
		return writeSocketFailure(output, errors.New("signer operation was canceled"))
	}
	data, err := io.ReadAll(io.LimitReader(input, maxSocketBytes+1))
	if err != nil {
		return writeSocketFailure(output, err)
	}
	if len(data) > maxSocketBytes {
		return writeSocketFailure(output, errors.New("signer socket request exceeds size limit"))
	}
	var request signertransport.Request
	if err := decodeStrictJSON(data, &request); err != nil {
		return writeSocketFailure(output, errors.New("decode signer socket request"))
	}
	if request.Version != signertransport.Version {
		return writeSocketFailure(output, errors.New("signer socket protocol version is invalid"))
	}

	switch request.Operation {
	case signertransport.OperationIdentity:
		if request.Sign != nil {
			return writeSocketFailure(output, errors.New("signer identity request contains signing data"))
		}
		identity, err := signerIdentityWithBackend(policy, backend, attestationKey)
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, signertransport.Response{
			Version:  signertransport.Version,
			Status:   signertransport.StatusOK,
			Identity: &identity,
		})
	case signertransport.OperationSign:
		if request.Sign == nil {
			return writeSocketFailure(output, errors.New("signer socket request is missing signing data"))
		}
		encoded, err := json.Marshal(request.Sign)
		if err != nil || len(encoded) > maxRequestBytes {
			return writeSocketFailure(output, errors.New("signing request exceeds size limit"))
		}
		response, err := authorizeAndSignWithBackendContext(
			ctx, policy, backend, attestationKey, *request.Sign, now(),
		)
		if signer.IsRefusal(err) {
			return writeSocketResponse(output, signertransport.Response{
				Version: signertransport.Version,
				Status:  signertransport.StatusRefused,
				Reason:  socketRefusalReason(err.Error()),
			})
		}
		if err != nil {
			return writeSocketFailure(output, err)
		}
		return writeSocketResponse(output, signertransport.Response{
			Version: signertransport.Version,
			Status:  signertransport.StatusOK,
			Signed:  &response,
		})
	default:
		return writeSocketFailure(output, errors.New("signer socket operation is invalid"))
	}
}

func signerIdentityWithBackend(
	policy signer.Policy,
	backend signingBackend,
	attestationKey ed25519.PrivateKey,
) (signertransport.Identity, error) {
	if policy.Jupiter == nil {
		if backend.signTransaction != nil {
			return signertransport.Identity{}, errors.New("Turnkey custody is only valid for Jupiter policy")
		}
		if len(attestationKey) != 0 {
			return signertransport.Identity{}, errors.New("attestation keypair is only valid for Jupiter policy")
		}
		if err := policy.Validate(); err != nil {
			return signertransport.Identity{}, err
		}
		publicKey, err := signer.PublicKey(backend.walletKey)
		if err != nil || publicKey != policy.Source {
			return signertransport.Identity{}, errors.New("signer key does not match policy")
		}
		return signertransport.Identity{
			PublicKey: publicKey, ProfileSHA256: policy.ProfileFingerprint,
		}, nil
	}

	if len(attestationKey) == 0 {
		return signertransport.Identity{}, errors.New("Jupiter policy requires an attestation keypair")
	}
	if err := signer.ValidateJupiterPolicy(policy); err != nil {
		return signertransport.Identity{}, err
	}
	walletPublic := backend.remoteSource
	if backend.signTransaction == nil {
		var err error
		walletPublic, err = signer.PublicKey(backend.walletKey)
		if err != nil {
			return signertransport.Identity{}, errors.New("Jupiter custody key does not match policy")
		}
	}
	if walletPublic != policy.Source {
		return signertransport.Identity{}, errors.New("Jupiter custody key does not match policy")
	}
	attestationPublic, err := signer.PublicKey(attestationKey)
	if err != nil || attestationPublic != policy.AttestationPublicKey {
		return signertransport.Identity{}, errors.New("Jupiter attestation key does not match policy")
	}
	return signertransport.Identity{
		PublicKey:            walletPublic,
		AttestationPublicKey: attestationPublic,
		SubmitterPublicKey:   policy.SubmitterPublicKey,
		ProfileSHA256:        policy.ProfileFingerprint,
	}, nil
}

func authorizeAndSign(
	policy signer.Policy,
	walletKey ed25519.PrivateKey,
	attestationKey ed25519.PrivateKey,
	request signer.Request,
	now time.Time,
) (signer.Response, error) {
	return authorizeAndSignWithBackend(
		policy, signingBackend{walletKey: walletKey}, attestationKey, request, now,
	)
}

func authorizeAndSignWithBackend(
	policy signer.Policy,
	backend signingBackend,
	attestationKey ed25519.PrivateKey,
	request signer.Request,
	now time.Time,
) (signer.Response, error) {
	return authorizeAndSignWithBackendContext(
		context.Background(), policy, backend, attestationKey, request, now,
	)
}

func authorizeAndSignWithBackendContext(
	ctx context.Context,
	policy signer.Policy,
	backend signingBackend,
	attestationKey ed25519.PrivateKey,
	request signer.Request,
	now time.Time,
) (signer.Response, error) {
	if ctx == nil || ctx.Err() != nil {
		return signer.Response{}, errors.New("signer operation was canceled")
	}
	if policy.Jupiter == nil {
		if backend.signTransaction != nil {
			return signer.Response{}, errors.New("Turnkey custody is only valid for Jupiter policy")
		}
		if len(attestationKey) != 0 {
			return signer.Response{}, errors.New("attestation keypair is only valid for Jupiter policy")
		}
		return signer.AuthorizeAndSign(policy, backend.walletKey, request, now)
	}
	if len(attestationKey) == 0 {
		return signer.Response{}, errors.New("Jupiter policy requires an attestation keypair")
	}
	if backend.signTransaction == nil {
		return signer.AuthorizeAndSignJupiterFileKey(
			ctx, policy, backend.walletKey, attestationKey, request, now,
		)
	}
	if _, err := signerIdentityWithBackend(policy, backend, attestationKey); err != nil {
		return signer.Response{}, err
	}
	return signer.AuthorizeAndSignJupiterWith(
		ctx, policy, request, now, backend.signTransaction,
		func(ctx context.Context, message []byte) ([]byte, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return ed25519.Sign(attestationKey, message), nil
		},
	)
}

func writeSocketFailure(output io.Writer, cause error) error {
	if err := writeSocketResponse(output, signertransport.Response{
		Version: signertransport.Version,
		Status:  signertransport.StatusFailed,
	}); err != nil {
		return err
	}
	return cause
}

func writeSocketResponse(output io.Writer, response signertransport.Response) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}

func socketRefusalReason(reason string) string {
	clean := make([]byte, 0, len(reason))
	for _, char := range []byte(reason) {
		if char >= 0x20 && char < 0x7f && len(clean) < 256 {
			clean = append(clean, char)
		}
	}
	return string(clean)
}

func decodeStrictJSON(data []byte, out any) error {
	return strictjson.Decode(data, out)
}
