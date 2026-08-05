package swapbuilder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestQuoteProcessErrorClassifiesTemporaryProviderFailure(t *testing.T) {
	for _, test := range []struct {
		exitCode int
		want     string
	}{
		{75, "temporarily unavailable"},
		{1, "quote process failed"},
	} {
		command := exec.Command(os.Args[0], "-test.run=TestQuoteExitHelper")
		command.Env = append(
			os.Environ(),
			"MITHRIL_AGENT_QUOTE_TEST_EXIT="+strconv.Itoa(test.exitCode),
		)
		err := command.Run()
		if err == nil || !strings.Contains(quoteProcessError(err).Error(), test.want) {
			t.Fatalf("exit %d error = %v", test.exitCode, err)
		}
	}
}

func TestNewRequiresExactlyOneQuoteTransport(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty quote transport was accepted")
	}
	if _, err := New(Config{
		NodeCommand: "/node", ScriptPath: "/quote.mjs",
		RPCURL: "https://quote.invalid/devnet", SocketPath: "/quote.sock",
	}); err == nil {
		t.Fatal("mixed quote transports were accepted")
	}
}

func TestSocketClientCanStartBeforeTheService(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{SocketPath: filepath.Join(directory, "quote.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrQuoteTemporarilyUnavailable) {
		t.Fatalf("absent quote service health = %v", err)
	}
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{SocketPath: filepath.Join(directory, "quote.sock")}); err == nil {
		t.Fatal("unsafe absent socket directory was accepted")
	}
}

func TestDecodeWireRequestIsStrict(t *testing.T) {
	valid := []byte(`{"owner":"owner","pool":"pool","input_mint":"mint","input_amount":"1","slippage_bps":1}`)
	request, err := decodeWireRequest(valid)
	if err != nil || request.InputAmount != 1 || request.SlippageBPS != 1 {
		t.Fatalf("valid request = %+v, %v", request, err)
	}
	for _, data := range [][]byte{
		[]byte(`{"owner":"owner","pool":"pool","input_mint":"mint","input_amount":"1","slippage_bps":1,"extra":true}`),
		[]byte(`{"owner":"owner","owner":"other","pool":"pool","input_mint":"mint","input_amount":"1","slippage_bps":1}`),
		[]byte(`{"owner":"owner","pool":"pool","input_mint":"mint","input_amount":"-1","slippage_bps":1}`),
	} {
		if _, err := decodeWireRequest(data); err == nil {
			t.Fatalf("invalid request %q was accepted", data)
		}
	}
}

func TestQuoteExitHelper(t *testing.T) {
	exit := os.Getenv("MITHRIL_AGENT_QUOTE_TEST_EXIT")
	if exit == "" {
		return
	}
	code, err := strconv.Atoi(exit)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}

func TestDecodeResultChecksAmountsAndSigner(t *testing.T) {
	request := Request{
		Owner:       "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
		Pool:        "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
		InputMint:   "So11111111111111111111111111111111111111112",
		InputAmount: 1_000_000,
		SlippageBPS: 100,
	}
	wire := wireResult{
		Instructions: []wireInstruction{{
			Program:    "11111111111111111111111111111111",
			Accounts:   []solana.AccountMeta{{Address: request.Owner, Signer: true, Writable: true}},
			DataBase64: base64.StdEncoding.EncodeToString([]byte{1}),
		}},
		TokenIn:              "1000000",
		TokenEstOut:          "1",
		TokenMinOut:          "1",
		TradeEnableTimestamp: "0",
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeResult(data, request); err != nil {
		t.Fatal(err)
	}
	wire.TokenIn = "999999"
	data, _ = json.Marshal(wire)
	if _, err := decodeResult(data, request); err == nil {
		t.Fatal("mismatched input amount was accepted")
	}
	wire.TokenIn = "1000000"
	wire.Instructions[0].Accounts[0].Address = request.Pool
	data, _ = json.Marshal(wire)
	if _, err := decodeResult(data, request); err == nil {
		t.Fatal("unexpected signer was accepted")
	}
}

func TestUnixQuoteServiceMatchesDirectBuilder(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "mithril-agent-quote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	directory, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Owner:       "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
		Pool:        "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
		InputMint:   "So11111111111111111111111111111111111111112",
		InputAmount: 1_000_000,
		SlippageBPS: 100,
	}
	wire := wireResult{
		Instructions: []wireInstruction{{
			Program: "11111111111111111111111111111111",
			Accounts: []solana.AccountMeta{{
				Address: request.Owner, Signer: true, Writable: true,
			}},
			DataBase64: base64.StdEncoding.EncodeToString([]byte{1}),
		}},
		TokenIn: "1000000", TokenEstOut: "2", TokenMinOut: "1",
		TradeEnableTimestamp: "0",
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(directory, "quote-test")
	script := "#!/bin/sh\nprintf '%s\\n' '" + string(encoded) + "'\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	quoteScript := filepath.Join(directory, "quote.mjs")
	if err := os.WriteFile(quoteScript, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct, err := New(Config{
		NodeCommand: command, ScriptPath: quoteScript, RPCURL: "https://quote.invalid/devnet",
	})
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(directory, "quote.sock")
	ctx, cancel := context.WithCancel(t.Context())
	serverErr := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		serverErr <- ServeUnix(ctx, socketPath, direct, func() error {
			close(ready)
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-serverErr:
		t.Fatalf("quote service exited before startup: %v", err)
	case <-time.After(time.Second):
		t.Fatal("quote service was not ready")
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o660 {
		t.Fatalf("quote socket mode = %04o", info.Mode().Perm())
	}
	client, err := New(Config{SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatalf("quote service health = %v", err)
	}
	result, err := client.Quote(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenIn != request.InputAmount || result.TokenMinOut != 1 ||
		len(result.Instructions) != 1 {
		t.Fatalf("socket quote = %+v", result)
	}
	cancel()
	if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quote socket remained after shutdown: %v", err)
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrQuoteTemporarilyUnavailable) {
		t.Fatalf("stopped quote service health = %v", err)
	}
}

func TestDirectHealthDetectsProtectedFileDrift(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(directory, "node")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(directory, "quote.mjs")
	if err := os.WriteFile(script, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		NodeCommand: command, ScriptPath: script, RPCURL: "https://quote.invalid/devnet",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatalf("initial health = %v", err)
	}
	if err := os.Chmod(script, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrQuoteTemporarilyUnavailable) {
		t.Fatalf("unprotected script health = %v", err)
	}
}

func TestDirectSelfTestRequiresExactBoundedResponse(t *testing.T) {
	for _, response := range []string{
		`{"status":"ok"}`,
		`{"status":"bad"}`,
		`{"status":"ok","extra":true}`,
	} {
		t.Run(response, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			command := filepath.Join(directory, "node")
			contents := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(response) + "\n"
			if err := os.WriteFile(command, []byte(contents), 0o700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(directory, "quote.mjs")
			if err := os.WriteFile(script, []byte("test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			client, err := New(Config{
				NodeCommand: command,
				ScriptPath:  script,
				RPCURL:      "https://quote.invalid/devnet",
			})
			if err != nil {
				t.Fatal(err)
			}
			err = client.SelfTest(t.Context())
			if response == `{"status":"ok"}` && err != nil {
				t.Fatalf("valid self-test = %v", err)
			}
			if response != `{"status":"ok"}` && err == nil {
				t.Fatal("invalid self-test response was accepted")
			}
		})
	}
}

func TestLiveOrcaAdapterMatchesIndependentValidator(t *testing.T) {
	rpcURL := os.Getenv("MITHRIL_AGENT_ORCA_LIVE_RPC_URL")
	owner := os.Getenv("MITHRIL_AGENT_ORCA_LIVE_OWNER")
	if rpcURL == "" || owner == "" {
		if os.Getenv("MITHRIL_AGENT_ORCA_LIVE_REQUIRED") == "1" {
			t.Fatal("required live Orca RPC and owner are not configured")
		}
		t.Skip("set the live Devnet RPC and wallet owner to run the read-only contract test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../adapters/orca/quote.mjs")
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{NodeCommand: node, ScriptPath: script, RPCURL: rpcURL})
	if err != nil {
		t.Fatal(err)
	}
	inputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.WrappedSOLMint)
	if err != nil {
		t.Fatal(err)
	}
	outputTokenAccount, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	policy := orcaswap.Policy{
		Owner:              owner,
		Pool:               "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
		InputMint:          orcaswap.WrappedSOLMint,
		OutputMint:         "BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k",
		InputTokenAccount:  inputTokenAccount,
		OutputTokenAccount: outputTokenAccount,
		TokenVaultA:        "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
		TokenVaultB:        "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
		Oracle:             "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
		ProgramData:        orcaswap.WhirlpoolProgramData,
		UpgradeAuthority:   orcaswap.WhirlpoolUpgradeAuth,
		DeploymentSlot:     orcaswap.WhirlpoolDeploySlot,
		MaxInputLamports:   1_000_000, MinOutputAmount: 1, MaxSlippageBPS: 100,
		MaxOutputAccountRentLamports: orcaswap.DefaultMaxOutputAccountRentLamports,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.Quote(ctx, Request{
		Owner: policy.Owner, Pool: policy.Pool, InputMint: policy.InputMint,
		InputAmount: 1_000_000, SlippageBPS: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orcaswap.ValidateInstructions(policy, orcaswap.Quote{
		InputAmount: result.TokenIn, EstimatedOutput: result.TokenEstOut,
		MinimumOutput: result.TokenMinOut, SlippageBPS: 100,
	}, result.Instructions); err != nil {
		for _, instruction := range result.Instructions {
			if instruction.Program != orcaswap.WhirlpoolProgram {
				continue
			}
			for index, account := range instruction.Accounts {
				t.Logf("swap account %d = %s signer=%t writable=%t", index, account.Address, account.Signer, account.Writable)
			}
		}
		t.Fatal(err)
	}

	reverse, err := client.Quote(ctx, Request{
		Owner: policy.Owner, Pool: policy.Pool, InputMint: orcaswap.DevnetUSDCMint,
		InputAmount: 1_000, SlippageBPS: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reverse.TokenIn == 0 || reverse.TokenEstOut == 0 || reverse.TokenMinOut == 0 {
		t.Fatal("reverse quote returned an empty amount")
	}
	foundSwap := false
	for _, instruction := range reverse.Instructions {
		if instruction.Program == orcaswap.WhirlpoolProgram {
			foundSwap = true
			if len(instruction.Accounts) < 5 || instruction.Accounts[4].Address != policy.Pool {
				t.Fatal("reverse quote changed the pinned pool")
			}
		}
		for _, account := range instruction.Accounts {
			if account.Signer && account.Address != policy.Owner {
				t.Fatal("reverse quote requires an unexpected signer")
			}
		}
	}
	if !foundSwap {
		t.Fatal("reverse quote did not contain a Whirlpool swap")
	}
	if _, err := orcaswap.DiscoverPolicy(policy.Owner, orcaswap.Quote{
		InputAmount: reverse.TokenIn, EstimatedOutput: reverse.TokenEstOut,
		MinimumOutput: reverse.TokenMinOut, SlippageBPS: 100,
	}, reverse.Instructions); err == nil {
		t.Fatal("sell-only policy accepted a reverse quote")
	}
}

// The sidecar's 15s bound is enforced by killing the child, but Run also waits
// for the output pipes to close. A grandchild that outlives its parent holds
// them open, so without WaitDelay the call blocks for the grandchild's whole
// lifetime.
func TestQuoteDirectReturnsWhenGrandchildHoldsStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("bounded-timeout test needs ~20s")
	}
	directory := t.TempDir()
	command := filepath.Join(directory, "quote-orphan")
	// Fork a grandchild that inherits stdout and outlives the 15s deadline.
	script := "#!/bin/sh\nsleep 120 &\nsleep 60\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	quoteScript := filepath.Join(directory, "quote.mjs")
	if err := os.WriteFile(quoteScript, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		NodeCommand: command, ScriptPath: quoteScript, RPCURL: "https://quote.invalid/devnet",
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, quoteErr := client.Quote(t.Context(), Request{
			Owner:       "3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh",
			Pool:        "3KBZiL2g8C7tiJ32hTv5v3KM7aK9htpqTw4cTXz1HvPt",
			InputMint:   "So11111111111111111111111111111111111111112",
			InputAmount: 1_000_000,
			SlippageBPS: 100,
		})
		done <- quoteErr
	}()

	select {
	case quoteErr := <-done:
		if quoteErr == nil {
			t.Fatal("orphaned sidecar returned a usable quote")
		}
		if elapsed := time.Since(start); elapsed > 25*time.Second {
			t.Fatalf("quote returned after %v; the 15s bound is not enforced", elapsed)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("quote did not return within 25s; a grandchild holding stdout defeats the deadline")
	}
}
