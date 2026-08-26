package signerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signertransport"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const (
	maxResponseBytes       = 64 << 10
	maxSocketRequestBytes  = signer.MaxRequestBytes + 1024
	maxSocketResponseBytes = maxResponseBytes + 1024
	operationTimeout       = 30 * time.Second
	// maxRefusalBytes bounds what is read back from a refusing signer. Its
	// messages are single short sentences; anything longer is not one.
	maxRefusalBytes = 512
	// signerRefusalExitCode must match cmd/mithril-agent-signer's own constant.
	// It means the policy declined, not that the process broke.
	signerRefusalExitCode = 3
)

// ErrSignerRefused is the signer declining under its own policy — a daily cap
// reached, a window closed, a request outside the bound route. It is not a
// fault: the caller should report it as a refusal and try in the next window,
// rather than holding a built transaction until its blockhash expires.
var ErrSignerRefused = errors.New("signer refused under its policy")

// boundedBuffer keeps the first bytes and drops the rest without erroring, so a
// chatty child can neither fail the write nor grow this process's heap.
type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		if len(data) < room {
			room = len(data)
		}
		b.data = append(b.data, data[:room]...)
	}
	return len(data), nil
}

// refusalReason is the signer's first line, printable ASCII only. Read solely
// on the refusal exit code, where every message is one of our own constants.
func refusalReason(data []byte) string {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	line = bytes.TrimPrefix(bytes.TrimSpace(line), []byte("mithril-agent-signer: "))
	clean := make([]byte, 0, len(line))
	for _, b := range line {
		if b >= 0x20 && b < 0x7f {
			clean = append(clean, b)
		}
	}
	return string(bytes.TrimSpace(clean))
}

type Config struct {
	Command                      string
	PolicyPath                   string
	KeypairPath                  string
	SocketPath                   string
	SSH                          *SSHTransport
	ExpectedWalletPublicKey      string
	ExpectedAttestationPublicKey string
	ExpectedSubmitterPublicKey   string
	ExpectedProfileSHA256        string
	Env                          []string
}

// SSHTransport runs the signer protocol through a dedicated OpenSSH identity.
// The server must bind that identity to one forced signer command with the
// authorized_keys "restrict" option. The wallet key never leaves that server.
type SSHTransport struct {
	Command        string
	Host           string
	User           string
	Port           uint16
	IdentityPath   string
	KnownHostsPath string
}

type Client struct {
	config Config
}

type Identity struct {
	PublicKey            string `json:"public_key"`
	AttestationPublicKey string `json:"attestation_public_key,omitempty"`
	SubmitterPublicKey   string `json:"submitter_public_key,omitempty"`
	ProfileSHA256        string `json:"profile_sha256,omitempty"`
}

func New(config Config) (*Client, error) {
	if err := validateExpectedIdentities(config); err != nil {
		return nil, err
	}
	if config.SSH != nil {
		if config.Command != "" || config.PolicyPath != "" || config.KeypairPath != "" ||
			config.SocketPath != "" || len(config.Env) != 0 {
			return nil, errors.New("SSH signer transport cannot be combined with another signer mode")
		}
		if config.ExpectedWalletPublicKey == "" || config.ExpectedProfileSHA256 == "" {
			return nil, errors.New("SSH signer transport requires pinned signer identities and policy")
		}
		if !validSHA256(config.ExpectedProfileSHA256) {
			return nil, errors.New("expected signer policy fingerprint is invalid")
		}
		ssh := *config.SSH
		if ssh.Port == 0 {
			ssh.Port = 22
		}
		if err := validateSSHTransport(ssh); err != nil {
			return nil, err
		}
		config.SSH = &ssh
		return &Client{config: config}, nil
	}
	if config.SocketPath != "" {
		if !filepath.IsAbs(config.SocketPath) || filepath.Clean(config.SocketPath) != config.SocketPath {
			return nil, errors.New("signer socket path must be absolute and clean")
		}
		if err := secureexec.ValidateEnvironment(config.Env); err != nil {
			return nil, err
		}
		config.Env = append([]string(nil), config.Env...)
		return &Client{config: config}, nil
	}
	if err := secureexec.ValidateExecutable(config.Command); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.PolicyPath) || !filepath.IsAbs(config.KeypairPath) {
		return nil, errors.New("signer policy and keypair paths must be absolute")
	}
	if err := secureexec.ValidateEnvironment(config.Env); err != nil {
		return nil, err
	}
	config.Env = append([]string(nil), config.Env...)
	return &Client{config: config}, nil
}

func (c *Client) Identity(ctx context.Context) (Identity, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer cancel()
	if c.usesProtocolTransport() {
		response, err := c.protocolRoundTrip(ctx, signertransport.Request{
			Version: signertransport.Version, Operation: signertransport.OperationIdentity,
		})
		if err != nil {
			return Identity{}, err
		}
		if response.Status != signertransport.StatusOK || response.Identity == nil ||
			response.Signed != nil || response.Reason != "" {
			return Identity{}, errors.New("signer identity response is invalid")
		}
		if _, err := solana.Decode32(response.Identity.PublicKey); err != nil {
			return Identity{}, errors.New("signer identity is invalid")
		}
		if response.Identity.ProfileSHA256 != "" &&
			!validSHA256(response.Identity.ProfileSHA256) {
			return Identity{}, errors.New("signer policy fingerprint is invalid")
		}
		if c.config.ExpectedWalletPublicKey != "" {
			if response.Identity.PublicKey != c.config.ExpectedWalletPublicKey ||
				response.Identity.AttestationPublicKey != c.config.ExpectedAttestationPublicKey ||
				response.Identity.SubmitterPublicKey != c.config.ExpectedSubmitterPublicKey ||
				(c.config.ExpectedProfileSHA256 != "" &&
					response.Identity.ProfileSHA256 != c.config.ExpectedProfileSHA256) {
				return Identity{}, errors.New("signer identity or policy does not match protected configuration")
			}
		} else if response.Identity.AttestationPublicKey != "" ||
			response.Identity.SubmitterPublicKey != "" {
			return Identity{}, errors.New("unpinned signer returned protected identities")
		}
		return Identity{
			PublicKey:            response.Identity.PublicKey,
			AttestationPublicKey: response.Identity.AttestationPublicKey,
			SubmitterPublicKey:   response.Identity.SubmitterPublicKey,
			ProfileSHA256:        response.Identity.ProfileSHA256,
		}, nil
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return Identity{}, err
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--keypair", c.config.KeypairPath,
		"--identity",
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(nil)
	command.Stderr = &secureexec.DiscardCounter{}
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return Identity{}, errors.New("signer identity check failed")
	}
	if output.Len() > maxResponseBytes {
		return Identity{}, errors.New("signer identity response exceeds size limit")
	}
	var identity Identity
	if err := strictjson.Decode(output.Bytes(), &identity); err != nil {
		return Identity{}, errors.New("decode signer identity")
	}
	if _, err := solana.Decode32(identity.PublicKey); err != nil {
		return Identity{}, errors.New("signer identity is invalid")
	}
	if identity.ProfileSHA256 != "" && !validSHA256(identity.ProfileSHA256) {
		return Identity{}, errors.New("signer policy fingerprint is invalid")
	}
	if c.config.ExpectedWalletPublicKey != "" &&
		identity.PublicKey != c.config.ExpectedWalletPublicKey {
		return Identity{}, errors.New("signer identity does not match expected wallet")
	}
	return identity, nil
}

func (c *Client) Sign(ctx context.Context, request signer.Request) (signer.Response, error) {
	ctx, cancel, err := boundedOperationContext(ctx)
	if err != nil {
		return signer.Response{}, err
	}
	defer cancel()
	if c.usesProtocolTransport() {
		response, err := c.protocolRoundTrip(ctx, signertransport.Request{
			Version: signertransport.Version, Operation: signertransport.OperationSign,
			Sign: &request,
		})
		if err != nil {
			return signer.Response{}, err
		}
		switch response.Status {
		case signertransport.StatusRefused:
			if response.Identity != nil || response.Signed != nil {
				return signer.Response{}, errors.New("signer refusal response is invalid")
			}
			if reason := refusalReason([]byte(response.Reason)); reason != "" {
				return signer.Response{}, fmt.Errorf("%w: %s", ErrSignerRefused, reason)
			}
			return signer.Response{}, ErrSignerRefused
		case signertransport.StatusFailed:
			if response.Identity != nil || response.Signed != nil || response.Reason != "" {
				return signer.Response{}, errors.New("signer failure response is invalid")
			}
			return signer.Response{}, errors.New("signer service failed")
		case signertransport.StatusOK:
			if response.Identity != nil || response.Signed == nil || response.Reason != "" {
				return signer.Response{}, errors.New("signer response is invalid")
			}
			if err := validateResponseWithConfig(request, *response.Signed, c.config); err != nil {
				return signer.Response{}, err
			}
			return *response.Signed, nil
		default:
			return signer.Response{}, errors.New("signer service status is invalid")
		}
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return signer.Response{}, err
	}
	input, err := json.Marshal(request)
	if err != nil || len(input) > signer.MaxRequestBytes {
		return signer.Response{}, errors.New("encode signer request")
	}
	command := exec.CommandContext(
		ctx,
		c.config.Command,
		"--policy", c.config.PolicyPath,
		"--keypair", c.config.KeypairPath,
	)
	command.Env = secureexec.MinimalEnvironment(c.config.Env)
	command.Stdin = bytes.NewReader(input)
	// Kept bounded and only ever read on a REFUSAL exit. The signer's other
	// failures can name a policy or keypair path; its refusals are our own
	// constant strings and name nothing.
	stderr := &boundedBuffer{limit: maxRefusalBytes}
	command.Stderr = stderr
	var stdout bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxResponseBytes + 1}
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		// Every signer failure used to collapse to one message, so "the daily cap
		// is spent, try tomorrow" and "the binary is missing" were the same line.
		// The proposer then held its built transaction until the blockhash aged
		// out and reported an expired blockhash — a symptom, one minute and one
		// wrong diagnosis away from the cause.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == signerRefusalExitCode {
			if reason := refusalReason(stderr.data); reason != "" {
				return signer.Response{}, fmt.Errorf("%w: %s", ErrSignerRefused, reason)
			}
			return signer.Response{}, ErrSignerRefused
		}
		return signer.Response{}, errors.New("signer process failed")
	}
	if stdout.Len() > maxResponseBytes {
		return signer.Response{}, errors.New("signer response exceeds size limit")
	}
	response, err := decodeResponse(stdout.Bytes())
	if err != nil {
		return signer.Response{}, err
	}
	if err := validateResponseWithConfig(request, response, c.config); err != nil {
		return signer.Response{}, err
	}
	return response, nil
}

func boundedOperationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("signer operation context is unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, operationTimeout)
	return bounded, cancel, nil
}

func (c *Client) usesProtocolTransport() bool {
	return c.config.SocketPath != "" || c.config.SSH != nil
}

func (c *Client) protocolRoundTrip(
	ctx context.Context,
	request signertransport.Request,
) (signertransport.Response, error) {
	if c.config.SSH != nil {
		return c.sshRoundTrip(ctx, request)
	}
	return c.socketRoundTrip(ctx, request)
}

func (c *Client) sshRoundTrip(
	ctx context.Context,
	request signertransport.Request,
) (signertransport.Response, error) {
	args, err := sshTransportArgs(*c.config.SSH)
	if err != nil {
		return signertransport.Response{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxSocketRequestBytes {
		return signertransport.Response{}, errors.New("encode remote signer request")
	}
	command := exec.CommandContext(ctx, c.config.SSH.Command, args...)
	command.Env = secureexec.MinimalEnvironment(nil)
	command.Stdin = bytes.NewReader(append(encoded, '\n'))
	command.Stderr = &secureexec.DiscardCounter{}
	var output bytes.Buffer
	command.Stdout = &limitedWriter{writer: &output, remaining: maxSocketResponseBytes + 1}
	command.WaitDelay = time.Second
	runErr := command.Run()
	if output.Len() > maxSocketResponseBytes {
		return signertransport.Response{}, errors.New("remote signer response exceeds size limit")
	}
	if runErr != nil {
		return signertransport.Response{}, errors.New("remote signer transport failed")
	}
	return decodeProtocolResponse(output.Bytes(), "remote signer")
}

func (c *Client) socketRoundTrip(
	ctx context.Context,
	request signertransport.Request,
) (signertransport.Response, error) {
	if err := validateSocket(c.config.SocketPath); err != nil {
		return signertransport.Response{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxSocketRequestBytes {
		return signertransport.Response{}, errors.New("encode signer socket request")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.config.SocketPath)
	if err != nil {
		return signertransport.Response{}, errors.New("connect to signer service")
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return signertransport.Response{}, errors.New("set signer service deadline")
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stop()
	if _, err := io.Copy(connection, bytes.NewReader(append(encoded, '\n'))); err != nil {
		return signertransport.Response{}, errors.New("write signer service request")
	}
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		return signertransport.Response{}, errors.New("signer service is not a Unix socket")
	}
	if err := unix.CloseWrite(); err != nil {
		return signertransport.Response{}, errors.New("finish signer service request")
	}
	data, err := io.ReadAll(io.LimitReader(connection, maxSocketResponseBytes+1))
	if err != nil {
		return signertransport.Response{}, errors.New("read signer service response")
	}
	if len(data) > maxSocketResponseBytes {
		return signertransport.Response{}, errors.New("signer service response exceeds size limit")
	}
	return decodeProtocolResponse(data, "signer service")
}

func decodeProtocolResponse(data []byte, name string) (signertransport.Response, error) {
	var response signertransport.Response
	if err := strictjson.Decode(data, &response); err != nil {
		return signertransport.Response{}, fmt.Errorf("decode %s response", name)
	}
	if response.Version != signertransport.Version {
		return signertransport.Response{}, fmt.Errorf("%s protocol version is invalid", name)
	}
	return response, nil
}

func sshTransportArgs(config SSHTransport) ([]string, error) {
	if err := validateSSHTransport(config); err != nil {
		return nil, err
	}
	return []string{
		"-F", "none", "-T", "-x",
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "CertificateFile=none",
		"-o", "PreferredAuthentications=publickey",
		"-o", "PubkeyAuthentication=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "HostbasedAuthentication=no",
		"-o", "GSSAPIAuthentication=no",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + config.KnownHostsPath,
		"-o", "GlobalKnownHostsFile=none",
		"-o", "UpdateHostKeys=no",
		"-o", "VerifyHostKeyDNS=no",
		"-o", "CheckHostIP=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "PermitLocalCommand=no",
		"-o", "ProxyCommand=none",
		"-o", "ConnectTimeout=10",
		"-i", config.IdentityPath,
		"-p", strconv.FormatUint(uint64(config.Port), 10),
		config.User + "@" + config.Host,
		"mithril-agent-signer-protocol-v1",
	}, nil
}

func validateSSHTransport(config SSHTransport) error {
	if err := secureexec.ValidateExecutable(config.Command); err != nil {
		return fmt.Errorf("SSH command: %w", err)
	}
	if !validSSHName(config.User) {
		return errors.New("SSH signer user is invalid")
	}
	if !validSSHHost(config.Host) {
		return errors.New("SSH signer host is invalid")
	}
	if config.Port == 0 {
		return errors.New("SSH signer port is invalid")
	}
	if !validSSHPath(config.IdentityPath) || !validSSHPath(config.KnownHostsPath) {
		return errors.New("SSH signer paths must not contain configuration expansion tokens")
	}
	if err := secureexec.ValidateProtectedFile(config.IdentityPath); err != nil {
		return errors.New("SSH signer identity file is unsafe")
	}
	identity, err := os.Lstat(config.IdentityPath)
	if err != nil || identity.Mode().Perm()&0o077 != 0 {
		return errors.New("SSH signer identity file is unsafe")
	}
	if _, err := os.Lstat(config.IdentityPath + "-cert.pub"); err == nil || !os.IsNotExist(err) {
		return errors.New("SSH signer identity must not have an automatic certificate")
	}
	if err := secureexec.ValidateProtectedFile(config.KnownHostsPath); err != nil {
		return errors.New("SSH signer known-hosts file is unsafe")
	}
	knownHosts, err := os.Lstat(config.KnownHostsPath)
	if err != nil || knownHosts.Size() == 0 {
		return errors.New("SSH signer known-hosts file is empty")
	}
	return nil
}

func validSSHPath(value string) bool {
	return !strings.ContainsAny(value, " \t\r\n%~$")
}

func validSSHName(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && !strings.ContainsRune("._-", char) {
			return false
		}
	}
	return true
}

func validSSHHost(value string) bool {
	if value == "" || len(value) > 255 || value[0] == '-' ||
		strings.ContainsAny(value, "@/[]") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '.' && char != '-' {
			return false
		}
	}
	return true
}

func validateSocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("signer socket path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("signer socket is unavailable")
	}
	if !fileowner.Trusted(info) || info.Mode().Perm()&0o007 != 0 {
		return errors.New("signer socket is unsafe")
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Lstat(parentPath)
	if err != nil || parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() ||
		!fileowner.Trusted(parent) || parent.Mode().Perm()&0o022 != 0 {
		return errors.New("signer socket directory is unsafe")
	}
	if err := secureexec.ValidateProtectedDirectory(parentPath); err != nil {
		return errors.New("signer socket directory is unsafe")
	}
	return nil
}

func decodeResponse(data []byte) (signer.Response, error) {
	var response signer.Response
	if err := strictjson.Decode(data, &response); err != nil {
		return signer.Response{}, errors.New("decode signer response")
	}
	return response, nil
}

func validateResponse(request signer.Request, response signer.Response) error {
	return validateResponseWithConfig(request, response, Config{})
}

func validateResponseWithConfig(
	request signer.Request,
	response signer.Response,
	config Config,
) error {
	if response.ActionID != request.ActionID ||
		response.BlockhashContextSlot != request.BlockhashContextSlot ||
		response.LastValidBlockHeight != request.LastValidBlockHeight ||
		response.FeeLamports != request.FeeLamports {
		return errors.New("signer response does not match request identity")
	}
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		return errors.New("request message is not canonical base64")
	}
	messageHash := sha256.Sum256(message)
	if response.MessageSHA256 != hex.EncodeToString(messageHash[:]) {
		return errors.New("signer response has the wrong message hash")
	}
	binding, err := signer.RiskBinding(request, response.MessageSHA256)
	if err != nil || response.RequestSHA256 != binding.RequestSHA256 {
		return errors.New("signer response has the wrong request hash")
	}
	metadata := sealedtx.Metadata{
		Version:              sealedtx.Version,
		Domain:               sealedtx.Domain,
		ActionID:             response.ActionID,
		MessageSHA256:        response.MessageSHA256,
		TransactionSHA256:    response.TransactionSHA256,
		Signature:            response.Signature,
		BlockhashContextSlot: response.BlockhashContextSlot,
		FeeLamports:          response.FeeLamports,
		LastValidBlockHeight: response.LastValidBlockHeight,
	}
	if response.SealedTransaction.Metadata != metadata ||
		response.SealedTransaction.EphemeralPublicKeyBase64 == "" ||
		response.SealedTransaction.NonceBase64 == "" ||
		response.SealedTransaction.CiphertextBase64 == "" {
		return errors.New("sealed signer transaction does not match response")
	}
	if response.Signature == "" {
		return validateConfidentialResponse(request, response, message, config)
	}
	signature, err := solana.Decode64(response.Signature)
	if err != nil {
		return errors.New("signer signature is invalid")
	}
	transaction := make([]byte, 0, 1+len(signature)+len(message))
	transaction = append(transaction, 1)
	transaction = append(transaction, signature[:]...)
	transaction = append(transaction, message...)
	envelope, err := solana.VerifySignedTransactionEnvelope(transaction)
	if err != nil || !bytes.Equal(envelope.Message, message) {
		return errors.New("signer signature does not match its message")
	}
	transactionHash := sha256.Sum256(transaction)
	if response.TransactionSHA256 != hex.EncodeToString(transactionHash[:]) {
		return errors.New("signer transaction hash does not match its transaction")
	}
	wallet := solana.Encode(envelope.FeePayer[:])
	attestor := wallet
	submitter := response.SignerAttestation.SubmitterPublicKey
	if config.ExpectedWalletPublicKey != "" {
		if wallet != config.ExpectedWalletPublicKey {
			return errors.New("signer response wallet does not match protected identity")
		}
		attestor = config.ExpectedAttestationPublicKey
		submitter = config.ExpectedSubmitterPublicKey
		if response.SignerAttestation.SubmitterPublicKey != submitter {
			return errors.New("signer response submitter does not match protected identity")
		}
	}
	if err := signer.VerifyResponseAttestation(attestor, submitter, response); err != nil {
		return err
	}
	return nil
}

func validateConfidentialResponse(
	request signer.Request,
	response signer.Response,
	message []byte,
	config Config,
) error {
	if config.ExpectedWalletPublicKey == "" || request.Cluster != "mainnet-beta" ||
		request.Domain != jupiterswap.RequestDomain ||
		request.Profile != jupiterswap.ProfileName ||
		request.ProfileVersion != jupiterswap.ProfileVersion ||
		request.JupiterCandidate == nil ||
		request.JupiterCandidate.Version != proposalcheck.CandidateVersion ||
		request.JupiterProviders == nil {
		return errors.New("confidential signer response requires pinned Mainnet identities")
	}
	if request.JupiterCandidate.MessageBase64 != request.MessageBase64 ||
		request.JupiterCandidate.Policy.Owner != config.ExpectedWalletPublicKey {
		return errors.New("confidential signer request does not match protected wallet")
	}
	tables, err := jupiterswap.DecodeAddressTables(request.JupiterCandidate.AddressTables)
	if err != nil {
		return errors.New("confidential signer request has invalid address tables")
	}
	decoded, err := solana.DecodeV0Message(message, tables)
	if err != nil || len(decoded.AccountKeys) == 0 ||
		solana.Encode(decoded.AccountKeys[0][:]) != config.ExpectedWalletPublicKey {
		return errors.New("confidential signer request has the wrong wallet")
	}
	transactionHash, err := hex.DecodeString(response.TransactionSHA256)
	if err != nil || len(transactionHash) != sha256.Size ||
		hex.EncodeToString(transactionHash) != response.TransactionSHA256 {
		return errors.New("confidential signer transaction hash is invalid")
	}
	if response.SignerAttestation.SubmitterPublicKey != config.ExpectedSubmitterPublicKey {
		return errors.New("signer response submitter does not match protected identity")
	}
	return signer.VerifyResponseAttestation(
		config.ExpectedAttestationPublicKey,
		config.ExpectedSubmitterPublicKey,
		response,
	)
}

func validateExpectedIdentities(config Config) error {
	values := []string{
		config.ExpectedWalletPublicKey,
		config.ExpectedAttestationPublicKey,
		config.ExpectedSubmitterPublicKey,
	}
	set := 0
	for _, value := range values {
		if value != "" {
			set++
		}
	}
	if set == 0 {
		return nil
	}
	if set != len(values) {
		return errors.New("expected signer identities must be configured together")
	}
	if _, err := solana.Decode32(config.ExpectedWalletPublicKey); err != nil {
		return errors.New("expected signer wallet is invalid")
	}
	if _, err := solana.Decode32(config.ExpectedAttestationPublicKey); err != nil {
		return errors.New("expected signer attestation identity is invalid")
	}
	decoded, err := hex.DecodeString(config.ExpectedSubmitterPublicKey)
	if err != nil || len(decoded) != 32 ||
		hex.EncodeToString(decoded) != config.ExpectedSubmitterPublicKey {
		return errors.New("expected submitter identity is invalid")
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	originalLength := len(data)
	if w.remaining == 0 {
		return 0, errors.New("signer output limit reached")
	}
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	n, err := w.writer.Write(data)
	w.remaining -= n
	if err == nil && n < originalLength {
		return n, errors.New("signer output limit reached")
	}
	return n, err
}
