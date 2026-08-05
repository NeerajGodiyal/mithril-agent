package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/offchainmsg"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/policyauthority"
	"github.com/Overclock-Validator/mithril-agent/riskgrant"
	"github.com/Overclock-Validator/mithril-agent/sealedtx"
	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

// Sweep moves accumulated funds from the agent account to the operator's own
// wallet. It is the existing treasury_sweep_v1 transfer engine, unchanged;
// what this wizard adds is the configuration ceremony a fund-moving
// destination deserves:
//
//   - the destination must PROVE it is the operator's: a signature over a
//     challenge, made with the destination's own key. An existence check or a
//     balance check cannot tell the operator's wallet from an attacker's
//     lookalike; only a signature can. It also proves the address is a real
//     spendable key and not a typo, since neither can sign.
//   - the schedule anchor is set to the first UTC midnight at least the
//     activation delay after proof, so a newly registered destination cannot
//     receive funds immediately. The proposer and the signer each enforce
//     the anchor independently.
//   - the floor keeps enough behind for rent, fees, and — when a swap setup
//     exists beside this one — the swap's own spending needs, so a sweep
//     cannot starve an armed trade.
//
// Changing the destination later is deliberately a full re-setup: the
// destination lives inside the fingerprinted profile, so a change re-keys the
// control binding and the signer ledger and lands STOPPED until re-enabled.
const sweepSetupUsage = `Usage: mithril-agent setup sweep --wallet PATH --to ADDRESS [options]

Configures automatic sweeping of the agent account's excess balance to YOUR
wallet. The agent can never send funds anywhere but the proven destination:
the signer refuses any other target at signing time.

  --wallet PATH          the agent account keypair (from setup / wallet new)
  --to ADDRESS           YOUR wallet address; you will prove control of it
  --floor SOL            keep at least this much in the agent account
  --max SOL              largest single sweep (default 1.0)
  --min SOL              smallest sweep worth making (default 0.1)
  --daily SOL            daily sweep cap (default 2.0)
  --activation-delay DUR wait before the first sweep (default 24h, min 1h)
  --dir PATH             setup directory (default ~/.mithril-agent/sweep)
  --mithril-command PATH Mithril node executable for observations
  --mithril-config PATH  Mithril node config.toml
  --yes                  accept defaults without asking

After setup: enable with devnet-enable and run with devnet-run, both pointed
at this directory's config.json. Sweeping obeys the same control state,
signer caps, and durable daily ledger as every other action.`

const (
	// sweepChallengePrefix begins the signed text. The purpose is inside the
	// signature so it cannot be honestly mistaken for approving anything
	// else.
	sweepChallengePrefix = "mithril-agent sweep destination proof v1"
	// rentExemptFloorLamports is a safe over-approximation of the
	// rent-exempt minimum for a 0-data system account (890,880 lamports on
	// current clusters). The floor arithmetic uses a round million so the
	// printed numbers stay legible.
	rentExemptFloorLamports = 1_000_000
	defaultSweepWindow      = 3_600
	minActivationDelay      = time.Hour
	defaultActivationDelay  = 24 * time.Hour
	defaultSweepFee         = 100_000           // 0.0001 SOL
	maxSweepValue           = 1_000_000_000_000 // 1000 SOL: beyond a pilot's ambit
)

// sweepChallenge is the exact text the operator signs, single-line printable
// ASCII so `solana sign-offchain-message` and hardware wallets accept it.
func sweepChallenge(agentAccount, destination, nonce, issuedAt string) string {
	return sweepChallengePrefix +
		"|agent:" + agentAccount +
		"|destination:" + destination +
		"|nonce:" + nonce +
		"|issued:" + issuedAt
}

// destinationProof is the durable record of the ceremony, written beside the
// config. The engine does not read it — the schedule anchor enforces the
// delay — but it lets `strategy show` and any reviewer re-verify the
// registration from the file alone.
type destinationProof struct {
	Version         uint32 `json:"version"`
	AgentAccount    string `json:"agent_account"`
	Destination     string `json:"destination"`
	Nonce           string `json:"nonce"`
	IssuedAt        string `json:"issued_at"`
	SignatureBase58 string `json:"signature_base58"`
	ActiveAfterUnix int64  `json:"active_after_unix"`
}

func runSweepSetup(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("setup sweep", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	walletPath := flags.String("wallet", "", "agent account keypair path")
	destination := flags.String("to", "", "destination wallet address")
	floorText := flags.String("floor", "", "lamports to keep behind, in SOL")
	maxText := flags.String("max", "1", "largest single sweep in SOL")
	minText := flags.String("min", "0.1", "smallest sweep in SOL")
	dailyText := flags.String("daily", "2", "daily sweep cap in SOL")
	activationDelay := flags.Duration("activation-delay", defaultActivationDelay,
		"wait before the first sweep")
	dir := flags.String("dir", "", "setup directory")
	mithrilCommand := flags.String("mithril-command", "", "Mithril node executable")
	mithrilConfig := flags.String("mithril-config", "", "Mithril node config.toml")
	assumeYes := flags.Bool("yes", false, "accept defaults without asking")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, sweepSetupUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("setup sweep takes no positional arguments")
	}
	if *walletPath == "" || *destination == "" {
		return errors.New("setup sweep requires --wallet PATH and --to ADDRESS; run with --help for the ceremony")
	}
	if *activationDelay < minActivationDelay {
		return fmt.Errorf("the activation delay must be at least %s: it is the window in which an unwanted destination change is caught", minActivationDelay)
	}

	source, err := walletAddress(*walletPath)
	if err != nil {
		return fmt.Errorf("agent account: %w", err)
	}
	if _, err := solana.Decode32(*destination); err != nil {
		return errors.New("the destination is not a valid address")
	}
	if *destination == source {
		return errors.New("the destination must be YOUR wallet, not the agent account itself")
	}

	prompt := newPrompter(os.Stdin, output, !*assumeYes)

	// Ceremony part 1: the operator re-types the middle of the address.
	// Lookalike grinding and clipboard clippers target the ends, which is
	// exactly why the ends are not what gets re-typed.
	digest := sha256.Sum256([]byte(*destination))
	prompt.sayf("Destination: %s\n", *destination)
	prompt.sayf("Fingerprint: %s..%s · sha256:%s\n",
		(*destination)[:8], (*destination)[len(*destination)-8:], hex.EncodeToString(digest[:6]))
	middle := (*destination)[8:16]
	typed, err := prompt.ask(
		"Type characters 9-16 of the destination address to confirm it survived copy-paste", "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(typed) != middle {
		return errors.New("the re-typed characters do not match; check the address end to end and start again")
	}

	// Ceremony part 2: proof of control. No bypass flag exists on purpose.
	nonce, err := newSweepNonce()
	if err != nil {
		return err
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	challenge := sweepChallenge(source, *destination, nonce, issuedAt)
	prompt.sayf("\nProve the destination is yours. Sign this exact text with the DESTINATION's key:\n\n")
	prompt.sayf("  %s\n\n", challenge)
	prompt.sayf("With the Solana CLI:\n  solana sign-offchain-message -k YOUR_WALLET_KEYPAIR '%s'\n\n", challenge)
	signatureText, err := prompt.ask("Paste the base58 signature", "")
	if err != nil {
		return err
	}
	if err := verifySweepDestinationProof(
		source, *destination, nonce, issuedAt, strings.TrimSpace(signatureText),
	); err != nil {
		return err
	}
	prompt.sayf("Signature verified: the destination key approved sweeps from this agent.\n")

	// Best-effort existence read on the pinned Devnet endpoint. The proof
	// already established control; this catches signing for a wallet that
	// has never touched Devnet, which would strand swept funds unspendably
	// until its owner ever used the cluster.
	if info, err := destinationAccountInfo(ctx, *destination); err == nil && !info {
		confirmed, confirmErr := prompt.confirm(
			"The destination does not exist on Devnet yet; sweeps will still land, continue", false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return errors.New("setup sweep stopped at the operator's request")
		}
	}

	// Money bounds.
	maxLamports, err := parseDecimalUnitsLamports(*maxText, "largest single sweep")
	if err != nil {
		return err
	}
	minLamports, err := parseDecimalUnitsLamports(*minText, "smallest sweep")
	if err != nil {
		return err
	}
	dailyLamports, err := parseDecimalUnitsLamports(*dailyText, "daily sweep cap")
	if err != nil {
		return err
	}

	// The floor: rent + fee headroom + whatever a sibling swap setup needs.
	// Printed as arithmetic so the operator can check it, not trust it.
	siblingReserve, siblingNote := siblingSwapReserve()
	floorMinimum := uint64(rentExemptFloorLamports) + 3*defaultSweepFee + siblingReserve
	floorLamports := floorMinimum
	if *floorText != "" {
		floorLamports, err = parseDecimalUnitsLamports(*floorText, "floor")
		if err != nil {
			return err
		}
		if floorLamports < floorMinimum {
			return fmt.Errorf(
				"the floor must be at least %s: rent %d + fees %d%s",
				formatUnits(floorMinimum, 9)+" SOL",
				rentExemptFloorLamports, 3*defaultSweepFee, siblingNote)
		}
	}
	prompt.sayf("\nFloor: %s SOL = rent %d + fee headroom %d%s\n",
		formatUnits(floorLamports, 9), rentExemptFloorLamports, 3*defaultSweepFee, siblingNote)

	// The anchor: first UTC midnight at least the delay after proof. The
	// signer requires midnight alignment, which makes the anchor auditable
	// at a glance and pushes the true delay up, never down.
	proofTime := time.Now().UTC()
	anchor := firstMidnightAfter(proofTime.Add(*activationDelay))
	prompt.sayf("First possible sweep: %s (proof time + %s, rounded up to UTC midnight)\n",
		anchor.Format(time.RFC3339), *activationDelay)

	profile := agent.Profile{
		Name: agent.ProfileTreasurySweepV1, Version: 1, Cluster: "devnet",
		Source: source, Destination: *destination,
		ReserveLamports:     floorLamports,
		MinTransferLamports: minLamports, MaxTransferLamports: maxLamports,
		DailyCapLamports: dailyLamports, MaxFeeLamports: defaultSweepFee,
		ScheduleWindowSeconds: defaultSweepWindow, ScheduleAnchorUnix: anchor.Unix(),
		MaxClockUncertaintyMillis: 500, MaxObservationAgeSeconds: 60,
		MinHealthyObservationSeconds: 30, MinHealthySlotAdvance: 8,
		MaxNodeLagSlots: 150, MaxReconciliationSeconds: 120,
	}
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("sweep profile: %w", err)
	}
	fingerprint, err := profile.Fingerprint()
	if err != nil {
		return err
	}

	root := *dir
	if root == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return errors.New("resolve home directory for the sweep setup")
		}
		root = filepath.Join(home, ".mithril-agent", "sweep")
	}
	proof := destinationProof{
		Version: 1, AgentAccount: source, Destination: *destination,
		Nonce: nonce, IssuedAt: issuedAt,
		SignatureBase58: strings.TrimSpace(signatureText),
		ActiveAfterUnix: anchor.Unix(),
	}
	if err := installSweepSetup(
		root, fingerprint, profile, *walletPath, *mithrilCommand, *mithrilConfig, proof,
	); err != nil {
		return err
	}

	configPath := filepath.Join(root, "config.json")
	prompt.sayf("\nSweep configured. Nothing moves until you enable it:\n")
	prompt.sayf("  mithril-agent devnet-enable --config %s --duration 24h --max-actions 24 --reason 'daily sweep'\n", configPath)
	prompt.sayf("  mithril-agent devnet-run --config %s --metrics-address 127.0.0.1:9192\n", configPath)
	prompt.sayf("\nThe agent can only ever sweep to %s — the signer refuses any other destination.\n", *destination)
	return nil
}

func newSweepNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := swapSetupRandom.Read(raw); err != nil {
		return "", errors.New("generate destination challenge nonce")
	}
	return hex.EncodeToString(raw), nil
}

// verifySweepDestinationProof rebuilds the challenge and verifies the
// signature against the DESTINATION key. Success proves control, on-curve
// validity, and address integrity at once.
func verifySweepDestinationProof(
	agentAccount, destination, nonce, issuedAt, signatureBase58 string,
) error {
	if signatureBase58 == "" {
		return errors.New("no signature was provided; setup sweep cannot continue without proof of control")
	}
	destinationKey, err := solana.Decode32(destination)
	if err != nil {
		return errors.New("the destination is not a valid address")
	}
	signature, err := solana.Decode64(signatureBase58)
	if err != nil {
		return errors.New("the signature is not valid base58 for 64 bytes")
	}
	challenge := sweepChallenge(agentAccount, destination, nonce, issuedAt)
	ok, err := offchainmsg.Verify(destinationKey, challenge, signature)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(
			"the signature does not prove control of the destination; " +
				"sign the exact challenge text with the destination's own key")
	}
	return nil
}

// siblingSwapReserve reads the discovered swap setup, if any, and returns the
// balance an armed swap needs: its input, fee, and rent headroom. A sweep
// floor below that would let a sweep starve a trade the operator armed.
func siblingSwapReserve() (uint64, string) {
	pointer := discoverCurrentConfig()
	if pointer == "" {
		return 0, ""
	}
	cfg, err := readSwapConfig(pointer)
	if err != nil || cfg.Swap == nil {
		return 0, ""
	}
	reserve := cfg.Swap.InputLamports + cfg.Swap.MaxFeeLamports
	if !cfg.Swap.IsBuy() {
		reserve += cfg.Swap.Route.MaxOutputAccountRentLamports
	}
	return reserve, fmt.Sprintf(" + swap needs %d (from %s)", reserve, pointer)
}

func firstMidnightAfter(at time.Time) time.Time {
	at = at.UTC()
	midnight := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	if midnight.Before(at) {
		midnight = midnight.Add(24 * time.Hour)
	}
	return midnight
}

func parseDecimalUnitsLamports(value, description string) (uint64, error) {
	lamports, err := parseDecimalUnits9(value, description)
	if err != nil {
		return 0, err
	}
	if lamports > maxSweepValue {
		return 0, fmt.Errorf("%s is beyond this pilot's range", description)
	}
	return lamports, nil
}

// parseDecimalUnits9 is parseDecimalUnits for 9-decimal SOL amounts.
func parseDecimalUnits9(value, description string) (uint64, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "eE+-") {
		return 0, fmt.Errorf("%s must be a positive SOL amount with at most nine decimals", description)
	}
	whole, fraction, found := strings.Cut(value, ".")
	if !found {
		fraction = ""
	}
	if whole == "" || len(whole) > 10 || len(fraction) > 9 {
		return 0, fmt.Errorf("%s must be a positive SOL amount with at most nine decimals", description)
	}
	for _, part := range []string{whole, fraction} {
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0, fmt.Errorf("%s must be a positive SOL amount with at most nine decimals", description)
			}
		}
	}
	var lamports uint64
	for _, char := range whole {
		lamports = lamports*10 + uint64(char-'0')
	}
	lamports *= 1_000_000_000
	padded := fraction + strings.Repeat("0", 9)
	var fractionValue uint64
	for _, char := range padded[:9] {
		fractionValue = fractionValue*10 + uint64(char-'0')
	}
	lamports += fractionValue
	if lamports == 0 {
		return 0, fmt.Errorf("%s must be positive", description)
	}
	return lamports, nil
}

// destinationAccountInfo reports whether the destination exists on Devnet,
// via the same compile-time-pinned endpoint the wallet commands use. It can
// never be aimed at another cluster by configuration.
func destinationAccountInfo(ctx context.Context, address string) (bool, error) {
	var result struct {
		Value *struct {
			Owner string `json:"owner"`
		} `json:"value"`
	}
	err := walletRPC(ctx, "getAccountInfo", []any{address, map[string]string{"encoding": "base64"}}, &result)
	if err != nil {
		return false, err
	}
	return result.Value != nil, nil
}

// installSweepSetup writes the coherent file set for the sweep profile into
// root with the same staged, private-permission installation the swap setup
// uses. The state directory is keyed by the profile fingerprint, so a
// destination change can never inherit the previous ledger.
func installSweepSetup(
	root, fingerprint string,
	profile agent.Profile,
	walletPath, mithrilCommand, mithrilConfig string,
	proof destinationProof,
) error {
	stateDir := "state-" + fingerprint[:8]
	ledgerPath := filepath.Join(root, stateDir, "signer-authorizations.jsonl")
	controlPath := filepath.Join(root, stateDir, "control.json")
	journalPath := filepath.Join(root, stateDir, "events.jsonl")

	_, riskPrivate, err := ed25519.GenerateKey(swapSetupRandom)
	if err != nil {
		return errors.New("generate risk authority key")
	}
	defer clear(riskPrivate)
	riskPublic, err := riskgrant.PublicKeyHex(riskPrivate)
	if err != nil {
		return err
	}
	riskIDHash := sha256.Sum256([]byte(riskPublic))
	riskID := "risk-" + hex.EncodeToString(riskIDHash[:8])
	submitterPrivate, submitterPublic, err := sealedtx.GenerateKey(swapSetupRandom)
	if err != nil {
		return err
	}

	signerPolicy := signer.Policy{
		Cluster: profile.Cluster, Profile: profile.Name, ProfileVersion: profile.Version,
		ProfileFingerprint: fingerprint,
		Source:             profile.Source, Destination: profile.Destination,
		MaxLamports:             profile.MaxTransferLamports,
		MaxFeeLamports:          profile.MaxFeeLamports,
		DailyDebitCapLamports:   profile.DailyCapLamports,
		AuthorizationLedgerPath: ledgerPath,
		ScheduleWindowSeconds:   profile.ScheduleWindowSeconds,
		ScheduleAnchorUnix:      profile.ScheduleAnchorUnix,
		MaxBlockHeightWindow:    300,
		RiskAuthorityKeyID:      riskID, RiskAuthorityPublicKey: riskPublic,
		SubmitterPublicKey: submitterPublic,
	}
	submitterPolicy := submitter.Policy{
		Cluster: profile.Cluster, Profile: profile.Name,
		ProfileFingerprint: fingerprint, ControlStatePath: controlPath,
		Source: profile.Source, Destination: profile.Destination,
		MaxLamports: profile.MaxTransferLamports, MaxFeeLamports: profile.MaxFeeLamports,
		SubmitterPublicKey: submitterPublic,
	}
	riskPolicy := policyauthority.Policy{TransactionPolicy: signerPolicy, GrantLifetimeSecs: 30}
	if err := signerPolicy.ValidateAuthorizationPolicy(); err != nil {
		return fmt.Errorf("signer policy: %w", err)
	}
	if err := riskPolicy.Validate(); err != nil {
		return fmt.Errorf("risk policy: %w", err)
	}
	if err := submitterPolicy.Validate(); err != nil {
		return fmt.Errorf("submitter policy: %w", err)
	}

	agentCommand, err := resolvedAgentExecutable()
	if err != nil {
		return err
	}
	binDir := filepath.Dir(agentCommand)

	var cfg config
	cfg.Profile = profile
	cfg.MCP.Command = mithrilCommand
	cfg.MCP.Args = []string{"mcp", "--profile", "monitor"}
	if mithrilConfig != "" {
		cfg.MCP.Args = []string{"mcp", "--config", mithrilConfig, "--profile", "monitor"}
	}
	cfg.Policy.Command = filepath.Join(binDir, "mithril-agent-policy")
	cfg.Policy.PolicyPath = filepath.Join(root, "risk-policy.json")
	cfg.Policy.KeypairPath = filepath.Join(root, "risk-authority-keypair.json")
	cfg.Policy.KeyID = riskID
	cfg.Policy.PublicKey = riskPublic
	cfg.Signer.Command = filepath.Join(binDir, "mithril-agent-signer")
	cfg.Signer.PolicyPath = filepath.Join(root, "signer-policy.json")
	cfg.Signer.KeypairPath = walletPath
	cfg.Submitter.Command = filepath.Join(binDir, "mithril-agent-submitter")
	cfg.Submitter.PolicyPath = filepath.Join(root, "submitter-policy.json")
	cfg.Submitter.PrivateKeyPath = filepath.Join(root, "submitter-key.json")
	cfg.Control.StatePath = controlPath
	cfg.Journal.Path = journalPath

	riskKeypairDocument := keypairDocument(riskPrivate)
	defer clear(riskKeypairDocument)
	documents := map[string]any{
		"config.json": cfg, "signer-policy.json": signerPolicy,
		"risk-policy.json": riskPolicy, "risk-authority-keypair.json": riskKeypairDocument,
		"submitter-policy.json":  submitterPolicy,
		"submitter-key.json":     submitter.KeyDocument{Version: 1, PrivateKey: submitterPrivate},
		"destination-proof.json": proof,
	}
	return installSweepDocuments(root, stateDir, documents)
}

// installSweepDocuments is the swap setup's staged installation with the
// sweep's fingerprint-keyed state directory: stage privately, write every
// document, fsync, and only then rename into place — a failed setup leaves
// nothing behind.
func installSweepDocuments(root, stateDir string, documents map[string]any) error {
	parent := filepath.Dir(root)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(root)+".setup-")
	if err != nil {
		return errors.New("create sweep setup staging directory")
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return errors.New("protect sweep setup staging directory")
	}
	if err := os.Mkdir(filepath.Join(stage, stateDir), 0o700); err != nil {
		return errors.New("create sweep state directory")
	}
	for name, document := range documents {
		encoded, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return errors.New("encode sweep setup document")
		}
		encoded = append(encoded, '\n')
		if err := securefile.ReplacePrivate(
			filepath.Join(stage, name), encoded, maxSetupFileBytes,
		); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	stageDirectory, err := os.Open(stage)
	if err != nil {
		return errors.New("open sweep setup staging directory")
	}
	if err := stageDirectory.Sync(); err != nil {
		_ = stageDirectory.Close()
		return errors.New("sync sweep setup staging directory")
	}
	if err := stageDirectory.Close(); err != nil {
		return errors.New("close sweep setup staging directory")
	}
	if err := os.Rename(stage, root); err != nil {
		return errors.New("install sweep setup directory; remove any existing directory at " + root + " first")
	}
	installed = true
	parentDirectory, err := os.Open(parent)
	if err != nil {
		return errors.New("open sweep setup parent directory")
	}
	defer parentDirectory.Close()
	if err := parentDirectory.Sync(); err != nil {
		return errors.New("sync sweep setup parent directory")
	}
	return nil
}
