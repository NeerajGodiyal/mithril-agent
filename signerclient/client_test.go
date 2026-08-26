package signerclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/signertransport"
	"github.com/Overclock-Validator/mithril-agent/solana"
	gossh "golang.org/x/crypto/ssh"
)

func TestBoundedOperationContext(t *testing.T) {
	ctx, cancel, err := boundedOperationContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > operationTimeout || time.Until(deadline) < operationTimeout-time.Second {
		t.Fatalf("default signer deadline = %v", deadline)
	}

	short, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	shortDeadline, _ := short.Deadline()
	bounded, release, err := boundedOperationContext(short)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if deadline, ok := bounded.Deadline(); !ok || !deadline.Equal(shortDeadline) {
		t.Fatal("signer operation replaced the caller's shorter deadline")
	}
	var nilContext context.Context
	if _, _, err := boundedOperationContext(nilContext); err == nil {
		t.Fatal("accepted a nil signer operation context")
	}
}

func TestValidateResponseBindsExactMessage(t *testing.T) {
	request, response := clientFixture(t)
	if err := validateResponse(request, response); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*signer.Response){
		"action": func(value *signer.Response) {
			value.ActionID = strings.Repeat("0", 64)
		},
		"request hash": func(value *signer.Response) {
			value.RequestSHA256 = strings.Repeat("0", 64)
		},
		"height": func(value *signer.Response) {
			value.LastValidBlockHeight++
		},
		"blockhash context": func(value *signer.Response) {
			value.BlockhashContextSlot++
		},
		"sealed blockhash context": func(value *signer.Response) {
			value.SealedTransaction.Metadata.BlockhashContextSlot++
		},
		"hash": func(value *signer.Response) {
			value.MessageSHA256 = strings.Repeat("0", 64)
		},
		"fee": func(value *signer.Response) {
			value.FeeLamports++
		},
		"signature": func(value *signer.Response) {
			value.Signature = solana.Encode(bytes.Repeat([]byte{1}, 64))
		},
		"transaction hash": func(value *signer.Response) {
			value.TransactionSHA256 = strings.Repeat("0", 64)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := response
			mutate(&value)
			if err := validateResponse(request, value); err == nil {
				t.Fatal("mutated signer response was accepted")
			}
		})
	}
}

func TestValidateResponseAcceptsAValidVersionZeroEnvelope(t *testing.T) {
	seed := sha256.Sum256([]byte("version-zero signer client"))
	key := ed25519.NewKeyFromSeed(seed[:])
	publicKey := key.Public().(ed25519.PublicKey)
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildV0Message(
		solana.Encode(publicKey), blockhash,
		[]solana.Instruction{{Program: solana.ComputeBudgetProgram, Data: []byte{1}}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignV0Message(key, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain: "test/version-zero", Cluster: "mainnet-beta", Profile: "test",
		ProfileVersion: 1, ProfileFingerprint: strings.Repeat("1", 64),
		ActionID: strings.Repeat("2", 64), MessageBase64: base64.StdEncoding.EncodeToString(message),
		BlockhashContextSlot: 100, FeeLamports: 5_000, LastValidBlockHeight: 200,
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	_, submitterPublicKey := clientSubmitterKeys(t)
	response := signer.Response{
		ActionID: request.ActionID, RequestSHA256: binding.RequestSHA256,
		Signature: solana.Encode(signature[:]), MessageSHA256: hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: request.BlockhashContextSlot, FeeLamports: request.FeeLamports,
		LastValidBlockHeight: request.LastValidBlockHeight,
	}
	response.SealedTransaction, err = sealedtx.Seal(
		submitterPublicKey,
		sealedtx.Metadata{
			Version: sealedtx.Version, Domain: sealedtx.Domain, ActionID: response.ActionID,
			MessageSHA256: response.MessageSHA256, TransactionSHA256: response.TransactionSHA256,
			Signature: response.Signature, BlockhashContextSlot: response.BlockhashContextSlot,
			FeeLamports: response.FeeLamports, LastValidBlockHeight: response.LastValidBlockHeight,
		},
		transaction, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response.SignerAttestation, err = signer.AttestResponse(key, submitterPublicKey, response)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateResponse(request, response); err != nil {
		t.Fatal(err)
	}

	changed := response
	changed.Signature = solana.Encode(bytes.Repeat([]byte{1}, 64))
	if err := validateResponse(request, changed); err == nil {
		t.Fatal("invalid version-zero signature was accepted")
	}

	changed = response
	changed.TransactionSHA256 = strings.Repeat("0", 64)
	changed.SealedTransaction.Metadata.TransactionSHA256 = changed.TransactionSHA256
	changed.SignerAttestation, err = signer.AttestResponse(key, submitterPublicKey, changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateResponse(request, changed); err == nil {
		t.Fatal("re-attested wrong version-zero transaction hash was accepted")
	}
}

func TestValidateResponseKeepsMainnetSignatureConfidential(t *testing.T) {
	walletSeed := sha256.Sum256([]byte("confidential wallet"))
	walletKey := ed25519.NewKeyFromSeed(walletSeed[:])
	walletPublic := solana.Encode(walletKey.Public().(ed25519.PublicKey))
	message, err := solana.BuildV0Message(
		walletPublic,
		solana.Encode(bytes.Repeat([]byte{7}, 32)),
		[]solana.Instruction{{Program: solana.ComputeBudgetProgram, Data: []byte{1}}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction, _, err := solana.SignV0Message(walletKey, message, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate := proposalcheck.Candidate{
		Version:       proposalcheck.CandidateVersion,
		Policy:        jupiterswap.Policy{Owner: walletPublic},
		MessageBase64: base64.StdEncoding.EncodeToString(message),
	}
	providers := proposalcheck.ProviderBindings{}
	request := signer.Request{
		Domain: jupiterswap.RequestDomain, Cluster: "mainnet-beta",
		Profile: jupiterswap.ProfileName, ProfileVersion: jupiterswap.ProfileVersion,
		ProfileFingerprint: strings.Repeat("1", 64), ActionID: strings.Repeat("2", 64),
		MessageBase64: candidate.MessageBase64, BlockhashContextSlot: 100,
		FeeLamports: 5_000, LastValidBlockHeight: 200,
		JupiterCandidate: &candidate, JupiterProviders: &providers,
	}
	messageHash := sha256.Sum256(message)
	transactionHash := sha256.Sum256(transaction)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	submitterPrivate, submitterPublic := clientSubmitterKeys(t)
	response := signer.Response{
		ActionID: request.ActionID, RequestSHA256: binding.RequestSHA256,
		MessageSHA256:        hex.EncodeToString(messageHash[:]),
		TransactionSHA256:    hex.EncodeToString(transactionHash[:]),
		BlockhashContextSlot: request.BlockhashContextSlot, FeeLamports: request.FeeLamports,
		LastValidBlockHeight: request.LastValidBlockHeight,
	}
	response.SealedTransaction, err = sealedtx.SealConfidential(
		submitterPublic,
		sealedtx.Metadata{
			Version: sealedtx.Version, Domain: sealedtx.Domain, ActionID: response.ActionID,
			MessageSHA256: response.MessageSHA256, TransactionSHA256: response.TransactionSHA256,
			BlockhashContextSlot: response.BlockhashContextSlot, FeeLamports: response.FeeLamports,
			LastValidBlockHeight: response.LastValidBlockHeight,
		},
		transaction,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	attestorSeed := sha256.Sum256([]byte("confidential attestor"))
	attestorKey := ed25519.NewKeyFromSeed(attestorSeed[:])
	attestorPublic := solana.Encode(attestorKey.Public().(ed25519.PublicKey))
	response.SignerAttestation, err = signer.AttestResponseWith(
		attestorPublic,
		submitterPublic,
		response,
		func(message []byte) ([]byte, error) { return ed25519.Sign(attestorKey, message), nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		ExpectedWalletPublicKey: walletPublic, ExpectedAttestationPublicKey: attestorPublic,
		ExpectedSubmitterPublicKey: submitterPublic,
	}
	if err := validateResponseWithConfig(request, response, config); err != nil {
		t.Fatal(err)
	}
	if response.Signature != "" || response.SealedTransaction.Metadata.Signature != "" {
		t.Fatal("Mainnet signature escaped the sealed transaction")
	}
	opened, err := sealedtx.OpenConfidential(submitterPrivate, response.SealedTransaction)
	if err != nil || !bytes.Equal(opened, transaction) {
		t.Fatal("submitter could not recover the exact confidential transaction")
	}
	if err := validateResponse(request, response); err == nil {
		t.Fatal("unpinned caller accepted a confidential signer response")
	}
	changed := response
	changed.Signature = solana.Encode(bytes.Repeat([]byte{3}, 64))
	if err := validateResponseWithConfig(request, changed, config); err == nil {
		t.Fatal("public Mainnet signature was accepted")
	}
}

func TestValidateResponsePinsSeparateMainnetIdentities(t *testing.T) {
	request, response := clientFixture(t)
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := solana.VerifySignedTransactionEnvelope(append(
		append([]byte{1}, mustDecode64(t, response.Signature)...), message...,
	))
	if err != nil {
		t.Fatal(err)
	}
	attestationSeed := sha256.Sum256([]byte("separate signer attestor"))
	attestationKey := ed25519.NewKeyFromSeed(attestationSeed[:])
	attestationPublic := solana.Encode(attestationKey.Public().(ed25519.PublicKey))
	response.SignerAttestation, err = signer.AttestResponseWith(
		attestationPublic, response.SignerAttestation.SubmitterPublicKey, response,
		func(message []byte) ([]byte, error) {
			return ed25519.Sign(attestationKey, message), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		ExpectedWalletPublicKey:      solana.Encode(envelope.FeePayer[:]),
		ExpectedAttestationPublicKey: attestationPublic,
		ExpectedSubmitterPublicKey:   response.SignerAttestation.SubmitterPublicKey,
	}
	if err := validateResponseWithConfig(request, response, config); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"wallet": func(value *Config) {
			value.ExpectedWalletPublicKey = solana.Encode(bytes.Repeat([]byte{9}, 32))
		},
		"attestor": func(value *Config) {
			value.ExpectedAttestationPublicKey = solana.Encode(bytes.Repeat([]byte{8}, 32))
		},
		"submitter": func(value *Config) {
			value.ExpectedSubmitterPublicKey = strings.Repeat("7", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := config
			mutate(&changed)
			if err := validateResponseWithConfig(request, response, changed); err == nil {
				t.Fatal("response verified under a substituted protected identity")
			}
		})
	}
}

func TestDecodeResponseIsStrict(t *testing.T) {
	_, response := clientFixture(t)
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeResponse(encoded); err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)-1] = ','
	encoded = append(encoded, []byte(`"unexpected":true}`)...)
	if _, err := decodeResponse(encoded); err == nil {
		t.Fatal("unknown signer response field was accepted")
	}
	if _, err := decodeResponse([]byte(
		`{"action_id":"first","Action_ID":"second"}`,
	)); err == nil {
		t.Fatal("duplicate signer response field was accepted")
	}
}

func TestNewRejectsUnsafePaths(t *testing.T) {
	command := filepath.Join(t.TempDir(), "signer")
	if err := os.WriteFile(command, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		Command:     command,
		PolicyPath:  "relative",
		KeypairPath: "/private/key",
	}); err == nil {
		t.Fatal("relative policy path was accepted")
	}
	if _, err := New(Config{
		SocketPath: "/tmp/signer.sock", ExpectedWalletPublicKey: testPublicKey(t),
	}); err == nil {
		t.Fatal("partially pinned signer identities were accepted")
	}
}

func TestClientUsesPinnedSSHSignerProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH transport test")
	}
	request, expected := clientFixture(t)
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := solana.DecodeTransferMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	attestationPublic := solana.Encode(transfer.Source[:])
	profileSHA256 := request.ProfileFingerprint
	identityResponse, err := json.Marshal(signertransport.Response{
		Version: signertransport.Version,
		Status:  signertransport.StatusOK,
		Identity: &signertransport.Identity{
			PublicKey:            solana.Encode(transfer.Source[:]),
			AttestationPublicKey: attestationPublic,
			SubmitterPublicKey:   expected.SignerAttestation.SubmitterPublicKey,
			ProfileSHA256:        profileSHA256,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	signResponse, err := json.Marshal(signertransport.Response{
		Version: signertransport.Version,
		Status:  signertransport.StatusOK,
		Signed:  &expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(directory, "transport-key")
	knownHostsPath := filepath.Join(directory, "known-hosts")
	commandPath := filepath.Join(directory, "ssh")
	if err := os.WriteFile(identityPath, []byte("test identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHostsPath, []byte("signer.example ssh-ed25519 test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"IFS= read -r request || exit 20\n" +
		"case \"$request\" in\n" +
		"*'\"operation\":\"identity\"'*) printf '%s\\n' " + shellLiteral(string(identityResponse)) + ";;\n" +
		"*'\"operation\":\"sign\"'*) printf '%s\\n' " + shellLiteral(string(signResponse)) + ";;\n" +
		"*) exit 21;;\n" +
		"esac\n"
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	client, err := New(Config{
		SSH: &SSHTransport{
			Command: commandPath, Host: "signer.example", User: "mithril-signer",
			IdentityPath: identityPath, KnownHostsPath: knownHostsPath,
		},
		ExpectedWalletPublicKey:      solana.Encode(transfer.Source[:]),
		ExpectedAttestationPublicKey: attestationPublic,
		ExpectedSubmitterPublicKey:   expected.SignerAttestation.SubmitterPublicKey,
		ExpectedProfileSHA256:        profileSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := client.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if identity.PublicKey != solana.Encode(transfer.Source[:]) ||
		identity.ProfileSHA256 != profileSHA256 {
		t.Fatalf("remote signer identity = %+v", identity)
	}
	response, err := client.Sign(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.TransactionSHA256 != expected.TransactionSHA256 {
		t.Fatal("remote signer returned a different transaction")
	}
	wrongPolicy := client.config
	wrongPolicy.ExpectedProfileSHA256 = strings.Repeat("f", 64)
	mismatched, err := New(wrongPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mismatched.Identity(t.Context()); err == nil {
		t.Fatal("remote signer policy drift was accepted")
	}

	args, err := sshTransportArgs(*client.config.SSH)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\x00" + strings.Join(args, "\x00") + "\x00"
	for _, required := range []string{
		"-F", "none", "BatchMode=yes", "IdentitiesOnly=yes", "IdentityAgent=none",
		"CertificateFile=none",
		"PreferredAuthentications=publickey", "StrictHostKeyChecking=yes",
		"GlobalKnownHostsFile=none", "ClearAllForwardings=yes", "ProxyCommand=none",
		"mithril-signer@signer.example", "mithril-agent-signer-protocol-v1",
	} {
		if !strings.Contains(joined, "\x00"+required+"\x00") {
			t.Fatalf("hardened SSH argument %q is missing from %v", required, args)
		}
	}
	if !strings.Contains(joined, "\x00UserKnownHostsFile="+knownHostsPath+"\x00") ||
		!strings.Contains(joined, "\x00"+identityPath+"\x00") ||
		!strings.Contains(joined, "\x0022\x00") {
		t.Fatalf("pinned SSH files or default port are missing from %v", args)
	}

	if err := os.WriteFile(identityPath+"-cert.pub", []byte("unexpected certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Identity(t.Context()); err == nil {
		t.Fatal("automatic SSH identity certificate was accepted after startup")
	}
}

func TestClientUsesRealOpenSSHHandshake(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH transport test")
	}
	command, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client is unavailable")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	identityPath := filepath.Join(directory, "transport-key")
	clientKey := newSSHTestSigner(t, identityPath)
	hostKey := newSSHTestSigner(t, "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	knownHostsPath := filepath.Join(directory, "known-hosts")
	knownHost := "[" + address.IP.String() + "]:" + strconv.Itoa(address.Port) + " " +
		strings.TrimSpace(string(gossh.MarshalAuthorizedKey(hostKey.PublicKey()))) + "\n"
	if err := os.WriteFile(knownHostsPath, []byte(knownHost), 0o600); err != nil {
		t.Fatal(err)
	}

	signRequest, signResponse := clientFixture(t)
	message, err := base64.StdEncoding.Strict().DecodeString(signRequest.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := solana.VerifySignedTransactionEnvelope(append(
		append([]byte{1}, mustDecode64(t, signResponse.Signature)...), message...,
	))
	if err != nil {
		t.Fatal(err)
	}
	wallet := solana.Encode(envelope.FeePayer[:])
	attestorSeed := sha256.Sum256([]byte("real OpenSSH attestor"))
	attestorKey := ed25519.NewKeyFromSeed(attestorSeed[:])
	attestor := solana.Encode(attestorKey.Public().(ed25519.PublicKey))
	submitter := signResponse.SignerAttestation.SubmitterPublicKey
	profileSHA256 := signRequest.ProfileFingerprint
	signResponse.SignerAttestation, err = signer.AttestResponseWith(
		attestor, submitter, signResponse,
		func(message []byte) ([]byte, error) {
			return ed25519.Sign(attestorKey, message), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundIdentity := signertransport.Identity{
		PublicKey: wallet, AttestationPublicKey: attestor,
		SubmitterPublicKey: submitter, ProfileSHA256: profileSHA256,
	}
	serverErrors := make(chan error, 1)
	go func() {
		err := serveSSHRequest(
			listener, hostKey, clientKey.PublicKey(), "mithril-signer",
			signertransport.Request{
				Version: signertransport.Version, Operation: signertransport.OperationIdentity,
			},
			signertransport.Response{
				Version: signertransport.Version, Status: signertransport.StatusOK,
				Identity: &boundIdentity,
			},
		)
		if err == nil {
			err = serveSSHRequest(
				listener, hostKey, clientKey.PublicKey(), "mithril-signer",
				signertransport.Request{
					Version: signertransport.Version, Operation: signertransport.OperationSign,
					Sign: &signRequest,
				},
				signertransport.Response{
					Version: signertransport.Version, Status: signertransport.StatusOK,
					Signed: &signResponse,
				},
			)
		}
		serverErrors <- err
	}()

	client, err := New(Config{
		SSH: &SSHTransport{
			Command: command, Host: address.IP.String(), User: "mithril-signer",
			Port: uint16(address.Port), IdentityPath: identityPath,
			KnownHostsPath: knownHostsPath,
		},
		ExpectedWalletPublicKey: wallet, ExpectedAttestationPublicKey: attestor,
		ExpectedSubmitterPublicKey: submitter, ExpectedProfileSHA256: profileSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	identity, err := client.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if identity.PublicKey != wallet || identity.AttestationPublicKey != attestor ||
		identity.SubmitterPublicKey != submitter || identity.ProfileSHA256 != profileSHA256 {
		t.Fatal("OpenSSH signer returned different pinned identities")
	}
	signed, err := client.Sign(ctx, signRequest)
	if err != nil {
		t.Fatal(err)
	}
	if signed.TransactionSHA256 != signResponse.TransactionSHA256 {
		t.Fatal("OpenSSH signer returned a different transaction")
	}
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("OpenSSH test server did not finish")
	}
}

func newSSHTestSigner(t *testing.T, path string) gossh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		return signer
	}
	block, err := gossh.MarshalPrivateKey(privateKey, "mithril-agent transport test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return signer
}

func serveSSHRequest(
	listener net.Listener,
	hostKey gossh.Signer,
	allowedClientKey gossh.PublicKey,
	user string,
	expected signertransport.Request,
	response signertransport.Response,
) error {
	config := &gossh.ServerConfig{
		PublicKeyCallback: func(metadata gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if metadata.User() != user || !bytes.Equal(key.Marshal(), allowedClientKey.Marshal()) {
				return nil, errors.New("unexpected SSH client identity")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostKey)
	connection, err := listener.Accept()
	if err != nil {
		return err
	}
	server, channels, requests, err := gossh.NewServerConn(connection, config)
	if err != nil {
		return err
	}
	defer server.Close()
	go gossh.DiscardRequests(requests)
	newChannel, ok := <-channels
	if !ok || newChannel.ChannelType() != "session" {
		return errors.New("OpenSSH client did not open a signer session")
	}
	channel, channelRequests, err := newChannel.Accept()
	if err != nil {
		return err
	}
	defer channel.Close()
	for request := range channelRequests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var command struct{ Command string }
		if err := gossh.Unmarshal(request.Payload, &command); err != nil ||
			command.Command != "mithril-agent-signer-protocol-v1" {
			_ = request.Reply(false, nil)
			return errors.New("OpenSSH client requested an unexpected command")
		}
		if err := request.Reply(true, nil); err != nil {
			return err
		}
		var protocolRequest signertransport.Request
		if err := json.NewDecoder(io.LimitReader(channel, maxSocketRequestBytes+1)).Decode(
			&protocolRequest,
		); err != nil {
			return errors.New("OpenSSH signer request is invalid")
		}
		got, gotErr := json.Marshal(protocolRequest)
		want, wantErr := json.Marshal(expected)
		if gotErr != nil || wantErr != nil || !bytes.Equal(got, want) {
			return errors.New("OpenSSH signer request changed in transport")
		}
		if err := json.NewEncoder(channel).Encode(response); err != nil {
			return err
		}
		_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{0}))
		return channel.CloseWrite()
	}
	return errors.New("OpenSSH signer command was not requested")
}

func TestSSHSignerRequiresAnExclusivePinnedConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH transport test")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(directory, "ssh")
	identityPath := filepath.Join(directory, "transport-key")
	knownHostsPath := filepath.Join(directory, "known-hosts")
	for path, mode := range map[string]os.FileMode{
		commandPath: 0o700, identityPath: 0o600, knownHostsPath: 0o600,
	} {
		if err := os.WriteFile(path, []byte("test"), mode); err != nil {
			t.Fatal(err)
		}
	}
	transport := &SSHTransport{
		Command: commandPath, Host: "127.0.0.1", User: "signer", Port: 22,
		IdentityPath: identityPath, KnownHostsPath: knownHostsPath,
	}
	if _, err := New(Config{SSH: transport}); err == nil {
		t.Fatal("unpinned SSH signer was accepted")
	}
	pinned := Config{
		SSH: transport, ExpectedWalletPublicKey: testPublicKey(t),
		ExpectedAttestationPublicKey: testPublicKey(t),
		ExpectedSubmitterPublicKey:   strings.Repeat("1", 64),
		ExpectedProfileSHA256:        strings.Repeat("2", 64),
	}
	pinned.SocketPath = "/run/signer.sock"
	if _, err := New(pinned); err == nil {
		t.Fatal("ambiguous SSH and socket signer configuration was accepted")
	}
	pinned.SocketPath = ""
	if err := os.Chmod(identityPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := New(pinned); err == nil {
		t.Fatal("group-readable SSH identity was accepted")
	}
	if err := os.Chmod(identityPath, 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(directory, "%h-known-hosts")
	if err := os.WriteFile(tokenPath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport.KnownHostsPath = tokenPath
	if _, err := New(pinned); err == nil {
		t.Fatal("SSH configuration expansion token in a protected path was accepted")
	}
}

func TestSSHSignerTransportBoundsAndSanitizesFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH transport test")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(directory, "ssh")
	identityPath := filepath.Join(directory, "transport-key")
	knownHostsPath := filepath.Join(directory, "known-hosts")
	if err := os.WriteFile(identityPath, []byte("test identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHostsPath, []byte("signer.example ssh-ed25519 test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commandPath, []byte(
		"#!/bin/sh\nprintf '%s' "+shellLiteral(strings.Repeat("x", maxSocketResponseBytes+1))+"\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		SSH: &SSHTransport{
			Command: commandPath, Host: "signer.example", User: "signer",
			IdentityPath: identityPath, KnownHostsPath: knownHostsPath,
		},
		ExpectedWalletPublicKey:      testPublicKey(t),
		ExpectedAttestationPublicKey: testPublicKey(t),
		ExpectedSubmitterPublicKey:   strings.Repeat("1", 64),
		ExpectedProfileSHA256:        strings.Repeat("2", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Identity(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("oversized remote signer response = %v", err)
	}

	const secret = "/private/wallet/keypair.json"
	if err := os.WriteFile(commandPath, []byte(
		"#!/bin/sh\nprintf '%s\\n' "+shellLiteral(secret)+" >&2\nexit 7\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Identity(t.Context()); err == nil ||
		strings.Contains(err.Error(), secret) ||
		err.Error() != "remote signer transport failed" {
		t.Fatalf("remote signer process failure = %v", err)
	}
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestValidateSocketRejectsUserControlledDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket test")
	}
	realDirectory, err := os.MkdirTemp("/tmp", "signerclient-validation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realDirectory) })
	if err := os.Chmod(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realDirectory, "signer.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := validateSocket(path); err != nil {
		t.Fatalf("protected socket rejected: %v", err)
	}

	linkDirectory := t.TempDir()
	linkedParent := filepath.Join(linkDirectory, "runtime")
	if err := os.Symlink(realDirectory, linkedParent); err != nil {
		t.Fatal(err)
	}
	if err := validateSocket(filepath.Join(linkedParent, "signer.sock")); err == nil {
		t.Fatal("signer socket below a user-controlled directory symlink was accepted")
	}
}

func TestClientUsesBoundedSignerSocket(t *testing.T) {
	request, expected := clientFixture(t)
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := solana.DecodeTransferMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("/tmp", "signerclient-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "signer.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}

	serverErrors := make(chan error, 5)
	go func() {
		for range 5 {
			connection, err := listener.Accept()
			if err != nil {
				serverErrors <- err
				return
			}
			var requestEnvelope signertransport.Request
			if err := json.NewDecoder(connection).Decode(&requestEnvelope); err != nil {
				_ = connection.Close()
				serverErrors <- err
				return
			}
			response := signertransport.Response{Version: signertransport.Version}
			switch {
			case requestEnvelope.Operation == signertransport.OperationIdentity:
				response.Status = signertransport.StatusOK
				response.Identity = &signertransport.Identity{
					PublicKey: solana.Encode(transfer.Source[:]),
				}
			case requestEnvelope.Sign == nil:
				response.Status = signertransport.StatusFailed
			case requestEnvelope.Sign.ScheduleWindowStartUnix < request.ScheduleWindowStartUnix:
				response.Status = signertransport.StatusRefused
				response.Reason = "signing request schedule window does not include current UTC time"
			case requestEnvelope.Sign.ActionID != request.ActionID:
				response.Status = signertransport.StatusFailed
			default:
				response.Status = signertransport.StatusOK
				response.Signed = &expected
			}
			if err := json.NewEncoder(connection).Encode(response); err != nil {
				_ = connection.Close()
				serverErrors <- err
				return
			}
			if err := connection.Close(); err != nil {
				serverErrors <- err
				return
			}
			serverErrors <- nil
		}
	}()

	client, err := New(Config{SocketPath: path})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := client.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if identity.PublicKey != solana.Encode(transfer.Source[:]) {
		t.Fatalf("socket signer identity = %s", identity.PublicKey)
	}
	response, err := client.Sign(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.TransactionSHA256 != expected.TransactionSHA256 {
		t.Fatal("socket signer returned a different transaction")
	}

	closed := request
	closed.ScheduleWindowStartUnix -= 7200
	closed.ScheduleWindowEndUnix -= 7200
	if _, err := client.Sign(t.Context(), closed); !errors.Is(err, ErrSignerRefused) {
		t.Fatalf("socket refusal = %v, want ErrSignerRefused", err)
	}
	broken := request
	broken.ActionID = strings.Repeat("b", len(request.ActionID))
	if _, err := client.Sign(t.Context(), broken); err == nil || errors.Is(err, ErrSignerRefused) {
		t.Fatalf("socket fault = %v", err)
	}
	large := request
	large.JupiterCandidate = &proposalcheck.Candidate{
		AddressTables: largeAddressTableEvidence(),
	}
	if _, err := client.socketRoundTrip(t.Context(), signertransport.Request{
		Version: signertransport.Version, Operation: signertransport.OperationSign,
		Sign: &large,
	}); err != nil {
		t.Fatalf("large portable request did not cross signer socket: %v", err)
	}
	for range 5 {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}
}

func largeAddressTableEvidence() []jupiterswap.AddressTableEvidence {
	contents := base64.StdEncoding.EncodeToString(make([]byte, 256*32))
	tables := make([]jupiterswap.AddressTableEvidence, 32)
	for index := range tables {
		tables[index] = jupiterswap.AddressTableEvidence{
			Address: strings.Repeat("1", 44), AddressesBase64: contents,
		}
	}
	return tables
}

func TestLimitedWriterStopsAtBound(t *testing.T) {
	var output bytes.Buffer
	writer := limitedWriter{writer: &output, remaining: 3}
	n, err := writer.Write([]byte("abcdef"))
	if err == nil || n != 3 || output.String() != "abc" {
		t.Fatalf("limited write = %d, %v, %q", n, err, output.String())
	}
}

func TestClientInvokesSignerBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the signer command")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the production signer requires Unix ownership checks")
	}
	request, expected := clientFixture(t)
	message, err := base64.StdEncoding.Strict().DecodeString(request.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := solana.DecodeTransferMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := signer.Policy{
		Cluster:                 request.Cluster,
		Profile:                 request.Profile,
		ProfileVersion:          request.ProfileVersion,
		ProfileFingerprint:      request.ProfileFingerprint,
		Source:                  solana.Encode(transfer.Source[:]),
		Destination:             solana.Encode(transfer.Destination[:]),
		MaxLamports:             transfer.Lamports,
		MaxFeeLamports:          request.FeeLamports,
		DailyDebitCapLamports:   transfer.Lamports + request.FeeLamports,
		AuthorizationLedgerPath: filepath.Join(temp, "authorization.jsonl"),
		ScheduleWindowSeconds:   uint64(request.ScheduleWindowEndUnix - request.ScheduleWindowStartUnix),
		ScheduleAnchorUnix:      request.ScheduleWindowStartUnix - request.ScheduleWindowStartUnix%86_400,
		MaxBlockHeightWindow:    200,
	}
	authoritySeed := sha256.Sum256([]byte("risk-authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	policy.RiskAuthorityKeyID = "test-risk-authority"
	policy.RiskAuthorityPublicKey = authorityPublic
	_, policy.SubmitterPublicKey = clientSubmitterKeys(t)

	binary := filepath.Join(temp, "mithril-agent-signer")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "../cmd/mithril-agent-signer")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build signer: %v\n%s", err, output)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(temp, "policy.json")
	keyPath := filepath.Join(temp, "keypair.json")
	writePrivateJSON(t, policyPath, policy)
	writePrivateJSON(t, keyPath, keypairValues(key))

	client, err := New(Config{
		Command:     binary,
		PolicyPath:  policyPath,
		KeypairPath: keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Sign(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Signature != expected.Signature ||
		response.MessageSHA256 != expected.MessageSHA256 ||
		response.TransactionSHA256 != expected.TransactionSHA256 {
		t.Fatal("signer process returned a different signed transaction")
	}

	// A refusal has to arrive as a REFUSAL. Collapsed into "signer process
	// failed" it was indistinguishable from a missing binary: the proposer held
	// its built transaction, the blockhash aged out about a minute later, and
	// the operator was told the blockhash had expired. That cost hours of
	// looking at the wrong subsystem on Devnet on 2026-08-06.
	//
	// A schedule window that has already closed is a genuine refusal — the bound
	// working — and the right operator response is to wait for the next window.
	second := request
	second.ScheduleWindowStartUnix = request.ScheduleWindowStartUnix - 7200
	second.ScheduleWindowEndUnix = request.ScheduleWindowStartUnix - 3600
	_, err = client.Sign(t.Context(), second)
	if !errors.Is(err, ErrSignerRefused) {
		t.Fatalf("a closed schedule window = %v, want ErrSignerRefused", err)
	}
	// The signer's own sentence has to survive, or the operator knows a bound
	// was hit but not which one, and so not whether to wait or to reconfigure.
	reason := strings.TrimPrefix(err.Error(), ErrSignerRefused.Error()+": ")
	if reason == "" || reason == err.Error() {
		t.Errorf("refusal lost the signer's reason: %v", err)
	}
	// It is the signer's message, not the client's prefix re-stated.
	if strings.Contains(reason, "mithril-agent-signer:") {
		t.Errorf("refusal kept the child's program prefix: %q", reason)
	}
	// Whatever crosses the boundary stays printable and bounded.
	if len(err.Error()) > maxRefusalBytes {
		t.Errorf("refusal text is unbounded: %d bytes", len(err.Error()))
	}
	for _, b := range []byte(err.Error()) {
		if b < 0x20 || b >= 0x7f {
			t.Fatalf("refusal text carries a non-printable byte %#x: %q", b, err.Error())
		}
	}

	// The other half of the distinction, and the reason the marker exists: a
	// FAULT must not wear the refusal's clothes. An action ID the policy cannot
	// have produced means something is broken, not that a budget is spent — and
	// reported as a refusal it would tell the operator to wait until tomorrow
	// for a condition that will never clear on its own.
	broken := request
	broken.ActionID = strings.Repeat("b", len(request.ActionID))
	_, err = client.Sign(t.Context(), broken)
	if err == nil {
		t.Fatal("a request outside the policy was signed")
	}
	if errors.Is(err, ErrSignerRefused) {
		t.Errorf("a fault was reported as a policy refusal: %v", err)
	}
}

// A signer that fails for any OTHER reason must not have its output read: those
// messages can name the policy or keypair path.
func TestNonRefusalFailuresStayOpaque(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a stub signer")
	}
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	// Exits 1 — a fault, not a refusal — while printing something path-like.
	source := filepath.Join(temp, "stub.go")
	stub := "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n" +
		"func main() {\n\tfmt.Fprintln(os.Stderr, \"read policy: open /private/key.json: denied\")\n" +
		"\tos.Exit(1)\n}\n"
	if err := os.WriteFile(source, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(temp, "stub-signer")
	if output, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, output)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(temp, "policy.json")
	keyPath := filepath.Join(temp, "keypair.json")
	writePrivateJSON(t, policyPath, signer.Policy{})
	writePrivateJSON(t, keyPath, []uint16{})

	client, err := New(Config{Command: binary, PolicyPath: policyPath, KeypairPath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := clientFixture(t)
	_, err = client.Sign(t.Context(), request)
	if err == nil {
		t.Fatal("a failing signer was accepted")
	}
	if errors.Is(err, ErrSignerRefused) {
		t.Fatalf("a fault was reported as a policy refusal: %v", err)
	}
	if strings.Contains(err.Error(), "/private/key.json") ||
		strings.Contains(err.Error(), "denied") {
		t.Fatalf("a non-refusal failure leaked the signer's output: %v", err)
	}
}

func writePrivateJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func keypairValues(key []byte) []uint16 {
	values := make([]uint16, len(key))
	for index, value := range key {
		values[index] = uint16(value)
	}
	return values
}

func mustDecode64(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := solana.Decode64(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded[:]
}

func testPublicKey(t *testing.T) string {
	t.Helper()
	seed := sha256.Sum256([]byte("test public key"))
	return solana.Encode(ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey))
}

func clientFixture(t *testing.T) (signer.Request, signer.Response) {
	t.Helper()
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	source := solana.Encode(key.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destination := solana.Encode(ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{7}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 5)
	if err != nil {
		t.Fatal(err)
	}
	profileHash := sha256.Sum256([]byte("profile"))
	profileFingerprint := hex.EncodeToString(profileHash[:])
	now := time.Now().UTC()
	scheduleAnchor := time.Date(
		now.Year(),
		now.Month(),
		now.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	).Unix()
	scheduleStart := now.Truncate(time.Hour).Unix()
	actionID, err := agent.ComputeActionID(profileFingerprint, scheduleStart)
	if err != nil {
		t.Fatal(err)
	}
	request := signer.Request{
		Domain:                  signer.RequestDomain,
		Cluster:                 "devnet",
		Profile:                 "treasury_sweep_v1",
		ProfileVersion:          1,
		ProfileFingerprint:      profileFingerprint,
		ActionID:                actionID,
		ScheduleWindowStartUnix: scheduleStart,
		ScheduleWindowEndUnix:   scheduleStart + 3_600,
		MessageBase64:           base64.StdEncoding.EncodeToString(message),
		BlockhashContextSlot:    90,
		FeeLamports:             5_000,
		FeeMinContextSlot:       90,
		PrimaryFeeContextSlot:   90,
		SecondaryFeeContextSlot: 91,
		RecentBlockhash:         blockhash,
		ObservedBlockHeight:     100,
		LastValidBlockHeight:    200,
	}
	authoritySeed := sha256.Sum256([]byte("risk-authority"))
	authorityKey := ed25519.NewKeyFromSeed(authoritySeed[:])
	authorityPublic, err := riskgrant.PublicKeyHex(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := signer.Policy{
		Cluster:                request.Cluster,
		Profile:                request.Profile,
		ProfileVersion:         request.ProfileVersion,
		ProfileFingerprint:     request.ProfileFingerprint,
		Source:                 source,
		Destination:            destination,
		MaxLamports:            10,
		MaxFeeLamports:         5_000,
		DailyDebitCapLamports:  100_000,
		ScheduleWindowSeconds:  3_600,
		ScheduleAnchorUnix:     scheduleAnchor,
		MaxBlockHeightWindow:   200,
		RiskAuthorityKeyID:     "test-risk-authority",
		RiskAuthorityPublicKey: authorityPublic,
	}
	ledgerDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy.AuthorizationLedgerPath = filepath.Join(ledgerDir, "authorization.jsonl")
	_, policy.SubmitterPublicKey = clientSubmitterKeys(t)
	messageHash := sha256.Sum256(message)
	binding, err := signer.RiskBinding(request, hex.EncodeToString(messageHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	grantAt := now
	request.RiskGrant, err = riskgrant.Sign(
		authorityKey,
		policy.RiskAuthorityKeyID,
		binding,
		grantAt,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := signer.AuthorizeAndSign(policy, key, request, grantAt)
	if err != nil {
		t.Fatal(err)
	}
	return request, response
}

func clientSubmitterKeys(t *testing.T) (string, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("submitter"))
	privateKey := hex.EncodeToString(seed[:])
	publicKey, err := sealedtx.PublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, publicKey
}
