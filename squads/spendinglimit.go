// Package squads reads a Squads v4 spending limit and reports whether it is a
// real funding boundary.
//
// The point of the boundary is that it is enforced somewhere this software
// cannot reach. A Squads spending limit caps how much can ever leave a vault
// for a named destination in a period, and that cap is enforced on-chain by the
// Squads program — not by our policy, not by our signer, not by our operator.
// So the correct role for this package is to VERIFY the cap, never to use it.
//
// That is why nothing here builds, signs, or submits a transaction. If our
// software could move funds through the boundary, the boundary would only be as
// trustworthy as our software, which defeats the entire purpose of having one.
// Moving funds through it is a human action taken in Squads; our job is to let
// an operator prove the cap is real and correctly aimed before they fund
// anything.
//
// A spending limit is a funding boundary, NOT a policy engine. Squads v4's
// spending_limit_use can only perform system_program::transfer and
// transfer_checked — it cannot call another program. It can bound how much is
// at risk; it cannot express "only swap SOL for USDC on this pool".
package squads

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

// ProgramID is the Squads v4 program, verified deployed and executable on both
// mainnet-beta and devnet with identical program data.
const ProgramID = "SQDS4ep65T869zMMBKyuUq6aD6EgTu8psMjkvj52pCf"

// NativeMint is the all-zero mint a spending limit uses to mean native SOL
// rather than an SPL token.
const NativeMint = "11111111111111111111111111111111"

// Period is how often a spending limit refills.
type Period uint8

const (
	OneTime Period = 0
	Daily   Period = 1
	Weekly  Period = 2
	Monthly Period = 3
)

func (p Period) String() string {
	switch p {
	case OneTime:
		return "one-time"
	case Daily:
		return "daily"
	case Weekly:
		return "weekly"
	case Monthly:
		return "monthly"
	default:
		return "unknown"
	}
}

// Valid reports whether the period is one the program actually defines. An
// unrecognised period must never be reported as a bounded one.
func (p Period) Valid() bool { return p <= Monthly }

// Seconds is the refill interval, or 0 for OneTime which never refills. The
// values mirror Squads v4 Period::to_seconds exactly: a month is 30 days, not a
// calendar month.
func (p Period) Seconds() int64 {
	switch p {
	case Daily:
		return 24 * 60 * 60
	case Weekly:
		return 7 * 24 * 60 * 60
	case Monthly:
		return 30 * 24 * 60 * 60
	default:
		return 0
	}
}

// AvailableAt reports how much may be spent through this limit at the given
// time, accounting for a refill the program will perform but has not yet
// written down.
//
// The stored Remaining is only correct until the period elapses: the program
// resets it lazily, inside the spend instruction. A caller that reads Remaining
// alone concludes that an exhausted limit is exhausted forever, and stops
// spending against a limit that has in fact refilled.
//
// The comparison is strictly greater-than, matching spending_limit_use.rs:152.
// Using >= here would claim a refill one second before the program performs it,
// and the transaction would fail on chain.
func (l SpendingLimit) AvailableAt(now time.Time) uint64 {
	period := l.Period.Seconds()
	if period <= 0 {
		// OneTime never refills, so what is left is all there will ever be.
		return l.Remaining
	}
	if now.UTC().Unix()-l.LastResetAt > period {
		return l.Amount
	}
	return l.Remaining
}

// SpendingLimit is the decoded on-chain account.
type SpendingLimit struct {
	Multisig    string `json:"multisig"`
	CreateKey   string `json:"create_key"`
	VaultIndex  uint8  `json:"vault_index"`
	Mint        string `json:"mint"`
	Amount      uint64 `json:"amount"`
	Period      Period `json:"period"`
	Remaining   uint64 `json:"remaining_amount"`
	LastResetAt int64  `json:"last_reset_unix"`
	Bump        uint8  `json:"bump"`
	// Members are the keys allowed to spend through this limit, and
	// Destinations are where they are allowed to send. An empty Destinations
	// list means "anywhere", which is not a boundary at all.
	Members      []string `json:"members"`
	Destinations []string `json:"destinations"`
}

// discriminator is sha256("account:SpendingLimit")[:8], the Anchor tag every
// SpendingLimit account starts with.
var discriminator = [8]byte{0x0a, 0xc9, 0x1b, 0xa0, 0xda, 0xc3, 0xde, 0x98}

// Field offsets, confirmed by decoding real accounts on both clusters rather
// than by trusting the published layout.
const (
	offsetMultisig   = 8
	offsetCreateKey  = 40
	offsetVaultIndex = 72
	offsetMint       = 73
	offsetAmount     = 105
	offsetPeriod     = 113
	offsetRemaining  = 114
	offsetLastReset  = 122
	offsetBump       = 130
	offsetMembersLen = 131
	fixedPrefixBytes = 135
	pubkeyBytes      = 32
	maxKeysPerVector = 64
	maxAccountBytes  = fixedPrefixBytes + 4 + 2*maxKeysPerVector*pubkeyBytes
)

// DecodeSpendingLimit reads the account. It is strict about length at every
// step: a truncated account that decoded to a small, plausible-looking limit
// would be the most dangerous possible failure, because it would understate how
// much can leave the vault.
func DecodeSpendingLimit(data []byte) (SpendingLimit, error) {
	if len(data) < fixedPrefixBytes {
		return SpendingLimit{}, errors.New("spending limit account is too short")
	}
	if len(data) > maxAccountBytes {
		return SpendingLimit{}, errors.New("spending limit account is implausibly large")
	}
	if [8]byte(data[:8]) != discriminator {
		return SpendingLimit{}, errors.New("account is not a Squads spending limit")
	}

	limit := SpendingLimit{
		Multisig:    solana.Encode(data[offsetMultisig : offsetMultisig+pubkeyBytes]),
		CreateKey:   solana.Encode(data[offsetCreateKey : offsetCreateKey+pubkeyBytes]),
		VaultIndex:  data[offsetVaultIndex],
		Mint:        solana.Encode(data[offsetMint : offsetMint+pubkeyBytes]),
		Amount:      binary.LittleEndian.Uint64(data[offsetAmount : offsetAmount+8]),
		Period:      Period(data[offsetPeriod]),
		Remaining:   binary.LittleEndian.Uint64(data[offsetRemaining : offsetRemaining+8]),
		LastResetAt: int64(binary.LittleEndian.Uint64(data[offsetLastReset : offsetLastReset+8])),
		Bump:        data[offsetBump],
	}
	if !limit.Period.Valid() {
		return SpendingLimit{}, errors.New("spending limit period is not one this program defines")
	}

	members, next, err := readKeys(data, offsetMembersLen)
	if err != nil {
		return SpendingLimit{}, err
	}
	destinations, end, err := readKeys(data, next)
	if err != nil {
		return SpendingLimit{}, err
	}
	if end != len(data) {
		return SpendingLimit{}, errors.New("spending limit account has trailing bytes")
	}
	if len(members) == 0 {
		return SpendingLimit{}, errors.New("spending limit has no members")
	}
	limit.Members, limit.Destinations = members, destinations
	return limit, nil
}

// readKeys reads a length-prefixed pubkey vector and returns the offset that
// follows it.
func readKeys(data []byte, offset int) ([]string, int, error) {
	if offset+4 > len(data) {
		return nil, 0, errors.New("spending limit account ends inside a length prefix")
	}
	count := binary.LittleEndian.Uint32(data[offset : offset+4])
	if count > maxKeysPerVector {
		return nil, 0, errors.New("spending limit lists an implausible number of keys")
	}
	offset += 4
	end := offset + int(count)*pubkeyBytes
	if end > len(data) {
		return nil, 0, errors.New("spending limit account ends inside a key list")
	}
	keys := make([]string, 0, count)
	for index := range int(count) {
		start := offset + index*pubkeyBytes
		keys = append(keys, solana.Encode(data[start:start+pubkeyBytes]))
	}
	return keys, end, nil
}

// IsNativeSOL reports whether the limit caps native SOL rather than a token.
func (l SpendingLimit) IsNativeSOL() bool { return l.Mint == NativeMint }
