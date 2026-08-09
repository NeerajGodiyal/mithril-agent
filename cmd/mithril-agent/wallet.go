package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// The wallet model is deliberately two-tier. A user's own wallet — Phantom,
// Solflare, a hardware key — stays with the user and is never handed to the
// agent. It funds a separate, dedicated agent account with an explicitly
// chosen amount of risk, and the signer enforces route, programs, assets,
// amounts, budget and schedule on everything that account does.
//
// That dedicated account is NOT "disposable": it is a permanent, limited-risk
// application wallet whose owner and funding source remain the user's real
// wallet.
//
// `new` therefore exists for DEVNET ONLY, so a reviewer can obtain a test
// account without installing extra tooling. It is not a custody solution and
// must not be carried over to Mainnet, where a policy-based remote signer or a
// smart account with spending limits is the appropriate design.
const (
	walletUsage = `Usage:
  mithril-agent wallet check --file PATH    check the account the agent will use
  mithril-agent wallet new   --file PATH    create a DEVNET-ONLY agent account
  mithril-agent wallet verify --session SESSION
                                             verify a payout wallet from your Mac

The agent uses a dedicated account, never your own wallet. Fund that account
from your own wallet with only the amount you are willing to put at risk; the
signer then enforces route, amounts, budget and schedule on everything it does.

"new" is for Devnet testing only. For Mainnet, hold the key with tooling you
already trust (solana-keygen, a hardware wallet, an HSM) or a policy-based
signer. This tool never asks for, imports, or transmits an existing wallet key.`

	walletDevnetRPC   = "https://api.devnet.solana.com"
	walletMaxResponse = 64 << 10
	// A 64-byte key encodes to well under this; the bound stops a swapped-in
	// large file being read as a keypair.
	maxKeypairFileBytes = 8 << 10
	lamportsPerSOL      = 1_000_000_000
)

var walletHTTP = &http.Client{Timeout: 30 * time.Second}

func runWallet(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Fprintln(output, walletUsage)
		return err
	}
	switch args[0] {
	case "check":
		return runWalletCheck(ctx, args[1:], output)
	case "new":
		return runWalletNew(args[1:], output)
	case "verify":
		return runWalletVerify(ctx, args[1:], output)
	default:
		return fmt.Errorf("unknown wallet command %q; run mithril-agent wallet --help", args[0])
	}
}

// runWalletNew creates a Devnet-only agent account. Every condition here is
// load-bearing: secure randomness, refusing to overwrite, private permissions,
// never printing or transmitting the secret, and labelling the result Devnet.
func runWalletNew(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("wallet new", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "absolute path for the new Devnet keypair")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, walletUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *file == "" {
		return errors.New("wallet new requires --file PATH")
	}
	if !filepath.IsAbs(*file) || filepath.Clean(*file) != *file {
		return errors.New("wallet file must be an absolute clean path")
	}
	// Overwriting would destroy funds or break a configuration already bound
	// to the old account.
	if _, err := os.Lstat(*file); err == nil {
		return errors.New("a file already exists at that path; choose another or remove it deliberately")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect the wallet path")
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate account key")
	}
	defer clear(private)

	values := make([]uint16, ed25519.PrivateKeySize)
	for index, b := range private {
		values[index] = uint16(b)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return errors.New("encode account key")
	}
	defer clear(encoded)
	if err := securefile.ReplacePrivate(*file, encoded, maxKeypairFileBytes); err != nil {
		return err
	}

	address := solana.Encode(public)
	fmt.Fprintf(output, "Created a DEVNET-ONLY agent account.\n\n")
	fmt.Fprintf(output, "  address : %s\n", address)
	fmt.Fprintf(output, "  file    : %s (readable only by you)\n\n", *file)
	fmt.Fprintf(output, "This account is what the agent trades with. It is separate from your own\n")
	fmt.Fprintf(output, "wallet on purpose: fund it with only what you are willing to put at risk,\n")
	fmt.Fprintf(output, "and your own wallet stays the owner and funding source.\n\n")
	fmt.Fprintf(output, "Devnet SOL is test-only and has no value. Do not reuse this key on Mainnet.\n\n")
	fmt.Fprintf(output, "Fund it at https://faucet.solana.com using the address above, then run:\n")
	fmt.Fprintf(output, "  mithril-agent wallet check --file %s\n", *file)
	return nil
}

func runWalletCheck(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("wallet check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "absolute path to an existing keypair file")
	cluster := flags.String("cluster", "devnet", "cluster to read the balance from (devnet only)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, walletUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *file == "" {
		return errors.New("wallet check requires --file PATH")
	}
	if !filepath.IsAbs(*file) || filepath.Clean(*file) != *file {
		return errors.New("wallet file must be an absolute clean path")
	}
	// Refusing every other cluster keeps this from being pointed at a
	// Mainnet wallet, which this pilot must never touch.
	if *cluster != "devnet" {
		return errors.New("wallet check supports devnet only")
	}

	address, err := walletAddress(*file)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "  wallet file : %s\n", *file)
	fmt.Fprintf(output, "  address     : %s\n", address)

	// securefile.ReadPrivate already refused the file if it were group- or
	// world-readable, so reaching here proves the permissions are private.
	fmt.Fprintf(output, "  permissions : private to you (checked)\n")

	var result struct {
		Result struct {
			Value uint64 `json:"value"`
		} `json:"result"`
	}
	if err := walletRPC(ctx, "getBalance", []any{address}, &result); err != nil {
		fmt.Fprintf(output, "  balance     : could not be read (%v)\n", err)
		return nil
	}
	lamports := result.Result.Value
	fmt.Fprintf(output, "  balance     : %d.%09d SOL on devnet\n",
		lamports/lamportsPerSOL, lamports%lamportsPerSOL)
	if lamports == 0 {
		fmt.Fprintf(output, "\nThis wallet is empty. Fund it at https://faucet.solana.com\n")
		fmt.Fprintf(output, "using the address above. Devnet SOL is test-only and has no value.\n")
	}
	fmt.Fprintf(output, "\nThis command did not create, modify, or transmit the key.\n")
	return nil
}

// walletAddress derives the public address and discards the private key. The
// key is never returned to a caller, printed, or sent anywhere.
func walletAddress(path string) (string, error) {
	data, err := securefile.ReadPrivate(path, maxKeypairFileBytes)
	if err != nil {
		return "", err
	}
	defer clear(data)
	var values []uint16
	if err := json.Unmarshal(data, &values); err != nil {
		return "", errors.New("wallet file must be a JSON byte array, as written by solana-keygen")
	}
	defer clear(values)
	if len(values) != ed25519.PrivateKeySize {
		return "", errors.New("wallet file must contain exactly 64 bytes")
	}
	private := make(ed25519.PrivateKey, ed25519.PrivateKeySize)
	defer clear(private)
	for index, value := range values {
		if value > 255 {
			return "", errors.New("wallet values must be bytes")
		}
		private[index] = byte(value)
	}
	public, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("wallet key is invalid")
	}
	return solana.Encode(public), nil
}

// walletRPC reads from the public Devnet endpoint. The endpoint is fixed rather
// than configurable so this helper can never be aimed at Mainnet, and it only
// ever sends a public address.
func walletRPC(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return errors.New("encode wallet request")
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, walletDevnetRPC, strings.NewReader(string(body)))
	if err != nil {
		return errors.New("build wallet request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := walletHTTP.Do(request)
	if err != nil {
		return errors.New("the Devnet endpoint could not be reached")
	}
	defer response.Body.Close()
	body, err = io.ReadAll(io.LimitReader(response.Body, walletMaxResponse))
	if err != nil {
		return errors.New("the Devnet endpoint returned an unreadable response")
	}
	return decodeWalletResponse(body, out)
}

// decodeWalletResponse rejects a JSON-RPC error before decoding the result.
// Decoding only the result made a provider failure — a rate limit, most often,
// and the public endpoint rate-limits readily — indistinguishable from a
// successful "no such account": the caller saw a nil value and no error. That
// is how the sweep setup told an operator their destination did not exist on
// Devnet while it held 8.55 SOL. An unknown answer must stay unknown.
func decodeWalletResponse(body []byte, out any) error {
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errors.New("the Devnet endpoint returned an unreadable response")
	}
	if envelope.Error != nil {
		// The provider's own text is echoed, bounded; the endpoint is not, so
		// nothing about the operator's configuration leaks into a log.
		message := envelope.Error.Message
		if len(message) > 120 {
			message = message[:120]
		}
		return fmt.Errorf("the Devnet endpoint refused the read (%d): %s", envelope.Error.Code, message)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("the Devnet endpoint returned an unreadable response")
	}
	return nil
}

// walletLamports reads the native balance from the pinned public Devnet
// endpoint. It is advisory: nothing authorizes on it, so it is deliberately not
// routed through the agent's own node or its evidence gates.
func walletLamports(ctx context.Context, address string) (uint64, error) {
	var result struct {
		Result struct {
			Value uint64 `json:"value"`
		} `json:"result"`
	}
	if err := walletRPC(ctx, "getBalance", []any{address}, &result); err != nil {
		return 0, err
	}
	return result.Result.Value, nil
}

// walletTokenBalance reads the wallet's canonical associated token account.
// Exists stays separate from amount because an empty account is still usable
// by a buy profile, while a missing account is not.
func walletTokenBalance(ctx context.Context, owner, mint string) (amount uint64, exists bool, err error) {
	account, err := orcaswap.AssociatedTokenAddress(owner, mint)
	if err != nil {
		return 0, false, errors.New("derive the wallet's token account")
	}
	var result struct {
		Result struct {
			Value struct {
				Amount string `json:"amount"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := walletRPC(ctx, "getTokenAccountBalance", []any{account}, &result); err != nil {
		// An account that has never existed is the ordinary starting state and
		// reads as zero. Anything else is a provider failure and must NOT: this
		// is the same trap that made the sweep setup announce that every
		// destination was absent, because it could not tell a rate limit from
		// an empty answer. Reporting zero here would tell an operator their
		// round trip cannot fund its buy leg when in fact nothing was read.
		if isAccountNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if result.Result.Value.Amount == "" {
		return 0, false, errors.New("the Devnet endpoint returned a token balance with no amount")
	}
	amount, err = strconv.ParseUint(result.Result.Value.Amount, 10, 64)
	if err != nil {
		return 0, false, errors.New("the token balance is not a whole number of base units")
	}
	return amount, true, nil
}

// walletTokenAmount keeps balance callers simple. A missing account is zero,
// which is the right answer for funding and status checks.
func walletTokenAmount(ctx context.Context, owner, mint string) (uint64, error) {
	amount, _, err := walletTokenBalance(ctx, owner, mint)
	return amount, err
}

// isAccountNotFound distinguishes "this account has never existed" from every
// other failure. The endpoint reports an unknown account as a JSON-RPC error
// rather than an empty result, so without this a rate limit and an empty wallet
// are the same answer — and the safe reading of the two is opposite.
func isAccountNotFound(err error) bool {
	if err == nil {
		return false
	}
	// ONLY this phrase. Matching bare "not found" or "invalid param" swallowed
	// "-32601 Method not found" and reported a broken endpoint as an empty
	// wallet, which is the very confusion this function exists to prevent.
	return strings.Contains(strings.ToLower(err.Error()), "could not find account")
}
