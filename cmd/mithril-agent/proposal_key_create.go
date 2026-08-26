package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

const proposalKeyCreateUsage = `Usage: mithril-agent proposal key-create [options]

Creates one non-wallet Mainnet boundary key offline. Run it independently on
the host that will own that boundary. It never creates a provider account,
contacts a network, signs a transaction, or prints the private key.

  --kind KIND  risk-authority, attestation, or submitter
  --out PATH   new absolute private key path; it must not already exist`

func runProposalKeyCreate(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("proposal key-create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	kind := flags.String("kind", "", "risk-authority, attestation, or submitter")
	outPath := flags.String("out", "", "new absolute private key path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, proposalKeyCreateUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !validProposalKeyKind(*kind) ||
		!filepath.IsAbs(*outPath) || filepath.Clean(*outPath) != *outPath {
		return errors.New("proposal key-create requires a supported kind and new absolute output path")
	}
	if _, err := os.Lstat(*outPath); err == nil {
		return errors.New("proposal key output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect proposal key output")
	}

	privateDocument, publicKey, err := createProposalKey(*kind)
	if err != nil {
		return err
	}
	defer clear(privateDocument)
	if err := securefile.CreatePrivate(*outPath, privateDocument, 4<<10); err != nil {
		return errors.New("write private proposal key")
	}
	return json.NewEncoder(output).Encode(struct {
		Status    string `json:"status"`
		Kind      string `json:"kind"`
		PublicKey string `json:"public_key"`
	}{Status: "key_created", Kind: *kind, PublicKey: publicKey})
}

func validProposalKeyKind(kind string) bool {
	return kind == "risk-authority" || kind == "attestation" || kind == "submitter"
}

func createProposalKey(kind string) ([]byte, string, error) {
	if kind == "submitter" {
		privateKey, publicKey, err := sealedtx.GenerateKey(rand.Reader)
		if err != nil {
			return nil, "", err
		}
		encoded, err := json.Marshal(submitter.KeyDocument{Version: 1, PrivateKey: privateKey})
		if err != nil {
			return nil, "", errors.New("encode submitter key")
		}
		return append(encoded, '\n'), publicKey, nil
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", errors.New("generate proposal key")
	}
	defer clear(privateKey)
	encoded, err := json.Marshal(keypairDocument(privateKey))
	if err != nil {
		return nil, "", errors.New("encode proposal key")
	}
	if kind == "attestation" {
		return append(encoded, '\n'), solana.Encode(publicKey), nil
	}
	riskPublic, err := riskgrant.PublicKeyHex(privateKey)
	if err != nil {
		return nil, "", errors.New("derive risk-authority identity")
	}
	return append(encoded, '\n'), riskPublic, nil
}
