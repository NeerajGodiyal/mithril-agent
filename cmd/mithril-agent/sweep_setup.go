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
	"math"
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

Sweeps move native SOL only. Proceeds held as devUSDC must be bought back
into SOL before they can be swept.

  --wallet PATH          the agent account keypair (from setup / wallet new)
  --to ADDRESS           YOUR wallet address; you will prove control of it
  --floor SOL            keep at least this much in the agent account
  --max SOL              largest single sweep (default 1.0)
  --min SOL              smallest sweep worth making (default 0.1)
  --daily SOL            daily sweep cap (default 2.0)
  --activation-delay DUR wait before the first sweep (default 24h, min 1h)
                         0 = active immediately, for Devnet testing only
  --swap-config PATH     a swap setup on this wallet the floor must protect;
                         repeat once per leg of a round trip
  --dir PATH             setup directory (default ~/.mithril-agent/sweep)
  --mithril-command PATH Mithril node executable for observations
  --mithril-config PATH  Mithril node config.toml
  --yes                  accept defaults without asking

Sign the challenge ahead of time and pass it in, instead of signing at the
prompt (this is what makes the setup scriptable):
  --proof-nonce NONCE --proof-issued TIME --proof-signature BASE58

Print a challenge to sign without writing any config:
  mithril-agent swap challenge --wallet PATH --to ADDRESS

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
	proofNonce := flags.String("proof-nonce", "", "nonce of a proof signed earlier")
	proofIssued := flags.String("proof-issued", "", "issue time of a proof signed earlier")
	proofSignature := flags.String("proof-signature", "", "base58 signature of a proof signed earlier")
	var swapConfigs repeatedPath
	flags.Var(&swapConfigs, "swap-config",
		"a swap setup sharing this wallet whose needs the floor must reserve; repeatable")
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
	// The delay exists so that a destination change made by someone who should
	// not be making it cannot receive funds before anyone notices. That is a
	// real protection for real money — and pure obstruction on Devnet, where
	// the funds are free and the point is to watch the mechanism work.
	//
	// Because the schedule anchor must be midnight-aligned (both the proposer
	// and the signer require it independently), any positive delay rounds up
	// to the next UTC midnight — so even a one-hour delay can mean waiting
	// most of a day. A zero delay anchors at the most recent past midnight
	// instead, which is equally legal and leaves the current window already
	// open, so the sweep is live as soon as it is enabled.
	if *activationDelay != 0 && *activationDelay < minActivationDelay {
		return fmt.Errorf(
			"the activation delay must be 0 (Devnet testing: active immediately) or at least %s",
			minActivationDelay)
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

	// Every named setup, plus whatever the wizard recorded. Read BEFORE the
	// ceremony: this depends on nothing the ceremony produces, and rejecting a
	// mistyped path afterwards throws away a signature the operator already gave.
	needs, err := loadSwapNeeds(swapConfigs, []string{discoverCurrentConfig()})
	if err != nil {
		return err
	}

	prompt := newPrompter(os.Stdin, output, !*assumeYes)

	// Ceremony part 1: the operator re-types the middle of the address.
	// Lookalike grinding and clipboard clippers target the ends, which is
	// exactly why the ends are not what gets re-typed.
	digest := sha256.Sum256([]byte(*destination))
	prompt.sayf("Destination: %s\n", *destination)
	prompt.sayf("Fingerprint: %s..%s · sha256:%s\n",
		(*destination)[:8], (*destination)[len(*destination)-8:], hex.EncodeToString(digest[:6]))
	// The re-typed slice guards against a corrupted paste. A signature over a
	// challenge naming this destination proves the same thing more strongly,
	// so it is only asked for when no proof has been supplied.
	if *proofSignature == "" {
		middle := (*destination)[8:16]
		typed, err := prompt.ask(
			"Type characters 9-16 of the destination address to confirm it survived copy-paste", "")
		if err != nil {
			return err
		}
		if answer := strings.TrimSpace(typed); answer != middle {
			// Distinguish a wrong answer from no answer. With no terminal the
			// prompt returns empty, and "the re-typed characters do not match"
			// then blames the operator for something nobody asked them — the
			// same dead end the quote confirmation had, hit again on a scripted
			// run of the very ceremony this check protects.
			if answer == "" && !stdinIsTerminal() {
				return fmt.Errorf(
					"the destination needs confirming and nothing could ask you, because this "+
						"run has no terminal. Either run this ceremony interactively, or supply "+
						"a proof — a valid signature over %s proves the address as well as "+
						"re-typing it does, and setup skips the question when one is given",
					*destination)
			}
			return errors.New(
				"the re-typed characters do not match; check the address end to end and start again")
		}
	}

	// Ceremony part 2: proof of control. There is no bypass — a signature is
	// required either way — but it may be produced ahead of time.
	//
	// Signing a message is not something a wallet UI initiates: it is a
	// method an application calls, and the wallet then shows its own prompt.
	// Demanding the signature at this exact moment therefore forces the
	// operator to have a signing tool open beside a half-finished wizard.
	// Accepting a proof made earlier splits the ceremony where it naturally
	// divides, and makes the whole setup scriptable instead of requiring
	// someone to sit in front of a prompt.
	//
	// Nothing is weakened by the split: the signature still has to match a
	// challenge naming this exact agent and destination, and the nonce and
	// issue time are inside the signed bytes, so a proof cannot be moved to
	// another destination or reused for another agent.
	nonce, issuedAt := *proofNonce, *proofIssued
	signatureText := *proofSignature
	preSigned := nonce != "" || issuedAt != "" || signatureText != ""
	if preSigned {
		if nonce == "" || issuedAt == "" || signatureText == "" {
			return errors.New(
				"a pre-signed proof needs all three of --proof-nonce, --proof-issued and --proof-signature")
		}
		// The issue time is inside the signed bytes, so it has to mean
		// something or it should not be there. Without this check a proof
		// stays usable forever — including for a wallet whose owner has since
		// rotated away from it, which is exactly the case where re-confirming
		// is cheap and being wrong is not.
		if err := checkProofFreshness(strings.TrimSpace(issuedAt), time.Now().UTC()); err != nil {
			return err
		}
	} else {
		var nonceErr error
		if nonce, nonceErr = newSweepNonce(); nonceErr != nil {
			return nonceErr
		}
		issuedAt = time.Now().UTC().Format(time.RFC3339)
		challenge := sweepChallenge(source, *destination, nonce, issuedAt)
		prompt.sayf("\nProve the destination is yours. Sign this exact text with the DESTINATION's key:\n\n")
		prompt.sayf("  %s\n\n", challenge)
		prompt.sayf("With the Solana CLI:\n  solana sign-offchain-message -k YOUR_WALLET_KEYPAIR '%s'\n", challenge)
		// Serving is the DEFAULT, not a flag. A better path nobody knows to ask
		// for is not a better path: the wizard is where this ceremony actually
		// happens, and leaving it on copy-a-file-then-paste-twice meant the good
		// flow existed only for someone who had already read the source.
		//
		// Pasting still works, concurrently: whichever arrives first wins, so an
		// operator who prefers the Solana CLI line above is never made to wait.
		signed, askErr := collectSweepSignature(ctx, prompt, output,
			challenge, source, *destination, nonce, issuedAt)
		if askErr != nil {
			return askErr
		}
		signatureText = signed
	}
	if err := verifySweepDestinationProof(
		source, *destination, nonce, strings.TrimSpace(issuedAt), strings.TrimSpace(signatureText),
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
			if !prompt.interactive {
				// confirm() returns the DEFAULT when there is nobody to ask, and
				// the default here is no. Reporting that as the operator's choice
				// blamed them for a decision they were never offered.
				return errors.New(
					"the destination has never been used on Devnet, and --yes cannot " +
						"agree to that on your behalf; re-run without --yes, or use a " +
						"destination that exists on Devnet")
			}
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

	// The floor: rent + fee headroom + whatever a sibling swap setup needs,
	// filtered to the setups that actually share this wallet. Printed as
	// arithmetic so the operator can check it, not trust it.
	siblingReserve, siblingNote, err := siblingSwapReserve(source, needs)
	if err != nil {
		return err
	}
	floorMinimum, err := addLamports(rentExemptFloorLamports+3*defaultSweepFee, siblingReserve)
	if err != nil {
		return fmt.Errorf("sweep floor: %w", err)
	}
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
	if *activationDelay == 0 {
		anchor = mostRecentMidnight(proofTime)
		prompt.sayf("First possible sweep: immediately (no activation delay — Devnet testing)\n")
	} else {
		prompt.sayf("First possible sweep: %s (proof time + %s, rounded up to UTC midnight)\n",
			anchor.Format(time.RFC3339), *activationDelay)
	}

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
	prompt.sayf("\nSweep configured. Nothing moves until you enable it — and the")
	prompt.sayf("first sweep window opens %s.\n", anchor.Format("2006-01-02 15:04 UTC"))
	// A grant lasts at most 24h and the anchor is 24-48h out, so enabling now
	// buys an authority that expires before the first window ever opens. That
	// produced a runner failing every cycle and an operator with no way to tell
	// that the setup was correct and merely early.
	prompt.sayf("On or after that time, run:\n")
	prompt.sayf("  mithril-agent devnet-enable --config %s --duration 24h --max-actions 24 --reason 'daily sweep'\n", configPath)
	prompt.sayf("  mithril-agent devnet-run --config %s --metrics-address 127.0.0.1:9192\n", configPath)
	prompt.sayf("\nThe agent can only ever sweep to %s — the signer refuses any other destination.\n", *destination)
	// Worth saying at the end of the sweep setup, because the natural next
	// thought after a profitable sell is "now send me the profit" — and the
	// profit is devUSDC, which this cannot move.
	prompt.sayf("Sweeps move native SOL only. devUSDC proceeds have to be bought")
	prompt.sayf("back into SOL before a sweep can send them.\n")
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

type swapNeed struct {
	path     string
	owner    string
	lamports uint64
	// required marks a setup the operator named with --swap-config. Dropping one
	// of those for any reason lowers the floor by exactly that leg's requirement.
	required bool
}

// loadSwapNeeds reads the setups whose needs the sweep floor must reserve.
//
// The two sources are NOT interchangeable. A discovered `current` pointer is
// very often empty or stale, so an unreadable one is skipped. An EXPLICIT
// --swap-config is an operator naming a leg to protect: skipping a mistyped
// one silently lowered the floor by exactly that leg's requirement, which is
// the starvation this function exists to prevent, reported as success.
func loadSwapNeeds(explicit, discovered []string) ([]swapNeed, error) {
	var needs []swapNeed
	seen := make(map[string]struct{}, len(explicit)+len(discovered))
	for index, path := range append(append([]string{}, explicit...), discovered...) {
		required := index < len(explicit)
		if path == "" {
			if required {
				return nil, errors.New("--swap-config needs a path")
			}
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		cfg, err := readSwapConfig(path)
		if err != nil {
			if required {
				return nil, fmt.Errorf("--swap-config %s: %w", path, err)
			}
			continue
		}
		needs = append(needs, swapNeed{
			path: path, owner: cfg.Swap.Owner(), required: required,
			// Ask the profile itself. Rebuilding this arithmetic here is how the
			// buy leg came to be under-reserved by the whole temporary rent.
			lamports: cfg.Swap.WalletRequirementLamports(),
		})
	}
	return needs, nil
}

// siblingSwapReserve is how much SOL the sweep must leave behind for the swap
// legs sharing this wallet. It takes the setups EXPLICITLY rather than resolving
// one `current` pointer: that pointer holds a single path, is written only by
// the wizard, and is last-write-wins — so with two legs it named at most one,
// and under the plain `swap setup` path it named neither, leaving a floor of
// about 1.3M lamports against a swap needing ~53M. The sweep then drained the
// wallet below what the trader needed and every later trade was refused.
//
// SUM, not max. Reserving only the largest leg would be correct only if exactly
// one leg could ever be armed, and nothing enforces that — a funding guarantee
// must not rest on an operator convention. Summing holds whatever is armed.
// Over-reserving costs sweep availability and fails closed; under-reserving
// starves a live trade.
func siblingSwapReserve(source string, needs []swapNeed) (uint64, string, error) {
	var reserve uint64
	var counted []string
	for _, need := range needs {
		// A setup on a different wallet cannot compete for this balance, and
		// reserving for it would starve the sweep for no reason. But a setup the
		// operator NAMED being quietly discarded is the same silent lowering this
		// reserve exists to prevent, so a named mismatch is an error, not a skip.
		if need.owner != source {
			if need.required {
				return 0, "", fmt.Errorf(
					"--swap-config %s belongs to %s, not this agent wallet %s",
					need.path, need.owner, source)
			}
			continue
		}
		next, err := addLamports(reserve, need.lamports)
		if err != nil {
			return 0, "", fmt.Errorf("swap setup %s: %w", need.path, err)
		}
		reserve = next
		counted = append(counted, need.path)
	}
	if reserve == 0 {
		return 0, "", nil
	}
	return reserve, fmt.Sprintf(
		" + swap needs %d (total of %d setup(s): %s)",
		reserve, len(counted), strings.Join(counted, ", ")), nil
}

// addLamports refuses to wrap. Saturating here was WORSE than wrapping: the
// caller adds rent and fee headroom to the result, so a saturated reserve wrapped
// one line later into a floor BELOW the no-swap baseline — the starvation this
// arithmetic exists to prevent, printed as a reassuring total.
func addLamports(base, add uint64) (uint64, error) {
	if add > math.MaxUint64-base {
		return 0, errors.New("the reserved amount exceeds the whole lamport supply")
	}
	return base + add, nil
}

const (
	// maxProofAge bounds how long a signed destination proof stays usable.
	// Generous enough that signing and configuring need not happen in one
	// sitting, short enough that a proof is evidence of a recent decision.
	maxProofAge = 30 * 24 * time.Hour
	// proofFutureSkew tolerates ordinary clock disagreement between the
	// machine that signed and the one that configures.
	proofFutureSkew = 5 * time.Minute
)

// checkProofFreshness rejects a proof that is stale or dated in the future.
// It runs only when accepting a proof, never when re-verifying a stored one:
// a proof made months ago was still valid when it was made, and the sweep
// display must keep saying so rather than reporting a false tamper.
func checkProofFreshness(issuedAt string, now time.Time) error {
	issued, err := time.Parse(time.RFC3339, issuedAt)
	if err != nil {
		return errors.New("the proof issue time is not a valid RFC3339 timestamp")
	}
	if issued.After(now.Add(proofFutureSkew)) {
		return errors.New("the proof is dated in the future; check the clock on the signing machine")
	}
	if now.Sub(issued) > maxProofAge {
		return fmt.Errorf(
			"the proof is older than %d days; generate a fresh challenge and sign it again",
			int(maxProofAge.Hours()/24))
	}
	return nil
}

// mostRecentMidnight is the UTC midnight at or before at, so the current
// schedule window is already open and the sweep can act as soon as it is
// enabled. It satisfies the same alignment rule the proposer and signer both
// enforce; only the delay is given up.
func mostRecentMidnight(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
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
// walletAccountResponse is the getAccountInfo envelope. The value is nested
// under result: decoding it at the top level, as this once did, yields nil for
// every address alive or not, so the setup warned that EVERY destination did
// not exist on Devnet. A warning that always fires is worse than none, because
// the operator learns to dismiss the one case it exists to catch — a wallet
// that has never touched the cluster.
type walletAccountResponse struct {
	Result struct {
		Value *struct {
			Owner string `json:"owner"`
		} `json:"value"`
	} `json:"result"`
}

func destinationAccountInfo(ctx context.Context, address string) (bool, error) {
	var response walletAccountResponse
	err := walletRPC(ctx, "getAccountInfo", []any{address, map[string]string{"encoding": "base64"}}, &response)
	if err != nil {
		return false, err
	}
	return response.Result.Value != nil, nil
}

// installSweepSetup writes the coherent file set for the sweep profile into
// root with the same staged, private-permission installation the swap setup
// uses. The state directory is keyed by the profile fingerprint, so a
// destination change can never inherit the previous ledger.
// stableStateDirName is what BOTH kinds of leg expose their state under: the
// swap setups create a real directory with this name, and the sweep links to
// its fingerprinted one. Anything outside the agent that needs to find a leg's
// journal or operator status can use the same path shape for every leg.
const stableStateDirName = "state"

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
	// A STABLE name beside the fingerprinted one. The hash keeps a destination
	// change from inheriting the previous ledger, which is the point — but it
	// also meant the sweep's state lived at a path nothing outside this function
	// could predict, while the swap legs use a plain "state". Deployment tooling
	// that has to name the file therefore hardcoded a hash, and when the sweep
	// was next re-created the name moved: on 2026-08-06 the operator-status
	// bridge kept pointing at state-6fa913b1 while the live directory was
	// state-425cd4ee, failed with 243/CREDENTIALS, and every sweep notification
	// was silently lost.
	//
	// The symlink is read-only convenience. Nothing writes through it: the
	// profile still records the real fingerprinted paths, so the isolation the
	// hash provides is untouched.
	if err := os.Symlink(stateDir, filepath.Join(stage, stableStateDirName)); err != nil {
		return errors.New("link the stable sweep state directory name")
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

// runSweepChallenge prints a challenge to sign, and nothing else. It writes no
// configuration and creates no keys, so an operator can produce the signature
// on whatever machine holds their wallet — which is rarely the machine running
// the agent — and complete the setup later with --proof-* flags.
func runSweepChallenge(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("swap challenge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	walletPath := flags.String("wallet", "", "agent account keypair path")
	agentAddress := flags.String("agent", "", "agent account address, if the keypair is elsewhere")
	destination := flags.String("to", "", "destination wallet address")
	asJSON := flags.Bool("json", false, "emit the challenge and its parts as JSON")
	// Serving is the default. --no-serve exists for a script that wants the
	// challenge printed and nothing else; making the GOOD path the flag meant
	// only someone who had read the source ever found it.
	noServe := flags.Bool("no-serve", false,
		"just print the challenge; do not serve the signing page or wait")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, sweepChallengeUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *destination == "" || (*walletPath == "") == (*agentAddress == "") {
		return errors.New(
			"swap challenge requires --to ADDRESS and exactly one of --wallet PATH or --agent ADDRESS")
	}
	source := *agentAddress
	if *walletPath != "" {
		var err error
		if source, err = walletAddress(*walletPath); err != nil {
			return fmt.Errorf("agent account: %w", err)
		}
	}
	if _, err := solana.Decode32(source); err != nil {
		return errors.New("the agent address is not a valid address")
	}
	if _, err := solana.Decode32(*destination); err != nil {
		return errors.New("the destination is not a valid address")
	}
	if source == *destination {
		return errors.New("the destination must be YOUR wallet, not the agent account itself")
	}
	nonce, err := newSweepNonce()
	if err != nil {
		return err
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	challenge := sweepChallenge(source, *destination, nonce, issuedAt)

	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			Challenge   string `json:"challenge"`
			Agent       string `json:"agent"`
			Destination string `json:"destination"`
			Nonce       string `json:"nonce"`
			IssuedAt    string `json:"issued_at"`
		}{challenge, source, *destination, nonce, issuedAt})
	}
	if _, err = fmt.Fprintf(output,
		"Sign this exact text with the key that owns %s:\n\n%s\n\n"+
			"Signing a message moves nothing and costs nothing; it only proves the key is yours.\n\n"+
			"With the Solana CLI:\n  solana sign-offchain-message -k YOUR_WALLET.json '%s'\n"+
			"With a browser wallet, open this page:\n",
		*destination, challenge, challenge); err != nil {
		return err
	}
	// --json is a machine-readable request: serving would block a caller that
	// only wants the challenge text back.
	if !*noServe && !*asJSON {
		return serveSweepSignature(context.Background(), output, challenge,
			source, *destination, nonce, issuedAt)
	}
	if _, err = fmt.Fprintln(output,
		"\nBrowser-wallet verification is available in the default mode. Re-run this command without --no-serve,\n"+
			"or use the Solana CLI signature above."); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"\nThen finish the setup with:\n"+
			"  mithril-agent setup sweep --wallet AGENT_KEYPAIR --to %s \\\n"+
			"      --proof-nonce %s --proof-issued %s \\\n"+
			"      --proof-signature PASTE_SIGNATURE_HERE\n",
		*destination, nonce, issuedAt)
	return err
}

const sweepChallengeUsage = `Usage: mithril-agent swap challenge (--wallet PATH | --agent ADDRESS) --to ADDRESS [--json]

Prints a challenge for the destination wallet to sign. Writes nothing and
changes nothing: run it anywhere, sign on whatever machine holds the wallet,
then pass the result to setup sweep with the --proof-* flags.`

// repeatedPath collects a flag given more than once. A round trip has two swap
// legs on one wallet and the floor has to know about both; a single-valued flag
// would silently keep only the last.
type repeatedPath []string

func (paths *repeatedPath) String() string { return strings.Join(*paths, ",") }

func (paths *repeatedPath) Set(value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return errors.New("swap config must be an absolute clean path")
	}
	*paths = append(*paths, value)
	return nil
}

// serveSweepSignature runs the browser half of the ceremony over a loopback
// tunnel, so the operator never copies a file, a challenge, or a signature.
func serveSweepSignature(
	ctx context.Context, output io.Writer,
	challenge, source, destination, nonce, issuedAt string,
) error {
	collector, err := newSignatureCollector(challenge, "", func(signature string) error {
		return verifySweepDestinationProof(source, destination, nonce, issuedAt, signature)
	})
	if err != nil {
		return err
	}
	host := hostnameForCopy()
	session, err := collector.session()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output,
		"\nWaiting for your payout wallet. On YOUR Mac or Linux desktop, run:\n\n"+
			"    %s\n\n"+
			"It opens and closes the SSH tunnel for you. If the helper is not installed\n"+
			"there yet, use this manual fallback:\n\n"+
			"    %s\n"+
			"    %s\n\n"+
			"The challenge is already filled in. Choose your wallet and approve the\n"+
			"proof; the verified signature returns here automatically. The page never\n"+
			"sees your private key.\n\n"+
			"Waiting up to %s. Ctrl-C to stop.\n",
		walletVerifyInvocation(host, session), collector.tunnelCommand(host), collector.url(), signServeTimeout); err != nil {
		return err
	}
	signature, err := collector.collect(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"\nSignature received and verified.\n\nFinish the setup with:\n"+
			"  mithril-agent setup sweep --wallet AGENT_KEYPAIR --to %s \\\n"+
			"      --proof-nonce %s --proof-issued %s \\\n"+
			"      --proof-signature %s\n",
		destination, nonce, issuedAt, signature)
	return err
}

// collectSweepSignature waits for a signature from either place it can come
// from: the operator's browser, over a loopback tunnel, or their keyboard.
//
// Both run at once deliberately. The browser route removes four manual steps,
// but somebody signing with the Solana CLI should not have to abandon a server
// to paste one line — and a wizard that only accepted one of the two would be
// worse than what it replaced for half its users.
func collectSweepSignature(
	ctx context.Context, prompt *prompter, output io.Writer,
	challenge, source, destination, nonce, issuedAt string,
) (string, error) {
	verify := func(signature string) error {
		return verifySweepDestinationProof(source, destination, nonce, issuedAt, signature)
	}
	collector, err := newSignatureCollector(challenge, "", verify)
	if err != nil {
		prompt.sayf("Browser-wallet verification could not start: %v", err)
		prompt.sayf("Use the Solana CLI command above and paste its signature here.")
		return prompt.ask("Paste the base58 signature", "")
	}
	session, err := collector.session()
	if err != nil {
		return "", err
	}
	host := hostnameForCopy()
	prompt.sayf("\nWith a browser wallet, run this on YOUR Mac or Linux desktop:\n")
	prompt.sayf("    %s\n", walletVerifyInvocation(host, session))
	prompt.sayf("If the helper is not installed there, use the manual fallback:\n")
	prompt.sayf("    %s\n", collector.tunnelCommand(host))
	prompt.sayf("    %s\n", collector.url())
	prompt.sayf("The challenge is already filled in; the signature returns on its own.\n")

	served := make(chan string, 1)
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() {
		if signature, err := collector.collect(serveCtx); err == nil {
			served <- signature
		}
	}()

	// The keyboard read runs in its own goroutine so the browser can win. If it
	// does, this read is simply abandoned — the process is a short-lived command
	// and exits moments later.
	typed := make(chan string, 1)
	go func() {
		if answer, err := prompt.ask("…or paste the base58 signature here", ""); err == nil {
			typed <- answer
		}
	}()

	select {
	case signature := <-served:
		if _, err := fmt.Fprintf(output, "\nSignature received from your wallet.\n"); err != nil {
			return "", err
		}
		return signature, nil
	case signature := <-typed:
		return signature, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
