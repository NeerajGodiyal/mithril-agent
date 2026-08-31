package pricesource

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

// Pyth publishes SOL/USD as a sponsored on-chain push feed, so the price can be
// read through the operator's own Mithril node with no data subscription. The
// Core upgrade has separate legacy and upgraded program-derived feed accounts.
// Both sponsored accounts and their exact owners are pinned here: reading both
// survives the migration, and while both publish they cross-check each other.
//
// The dangerous failure of a sponsored feed is not disappearance but silent
// staleness — a stopped feed keeps serving a well-formed, plausible price
// forever. Freshness is therefore a hard gate on the feed's own publish time,
// never on the node's context slot, which advances regardless.
const (
	PythPushTrustDomain = "pyth-network"

	pythPushLegacyAccount = "7UVimffxr9ow1uXYxsr4LHAcV58mLzhmwaeKvJ1pjLiE"
	pythPushLegacyOwner   = "rec5EKMGg6MxZYaMdyBfgwp4d5rB9T1VQH5pJv5LtFJ"
	pythPushLegacyProgram = "pythWSnswVUd12oZpeFP8e9CVaEqJg25g1Vtc2biRsT"

	pythPushUpgradedAccount = "7AviUf9nL62mcxNbQGKm4nKDQnPjswo6c5MX4D57HmyE"
	pythPushUpgradedOwner   = "rec2HHDDnjLfj4kE7VyEtFA1HPGQLK33259532cRyHp"
	pythPushUpgradedProgram = "pyt2F414BA6dPttK6RddPZUdHfapoBN24GL5wbrPCou"

	pythPushUSDCAccount         = "Dpw1EAVrSB1ibxiDQyTAW6Zip3J4Btk2x4SgApQCeFbX"
	pythPushUSDCUpgradedAccount = "6HAuqASbHEh4w4REJEUUUCginTLfj1kwCh215ZLtMkrT"
	USDCUSDFeedID               = "eaa020c61cc479712813461ce153894a96a6c00b21ed0cfc2798d1f9a9e9c94a"

	pythPushJUPAccount         = "7dbob1psH1iZBS7qPsm3Kwbf5DzSXK8Jyg31CTgTnxH5"
	pythPushJUPUpgradedAccount = "EitcZS5LtbR4EyNhCSy56vvUHPhsifSfWFG5gwSkjNpV"
	JUPUSDFeedID               = "0a0408d619e9380abad35060f9192039ed5042fa6f82301d0e48bb52be830996"

	// PriceUpdateV2 is a fixed-size Anchor account: an 8-byte discriminator,
	// a 32-byte write authority, a 1-byte verification level, the 32-byte feed
	// id, then the price record. Full verification consumes 133 bytes and the
	// account is padded to 134.
	pythPushAccountBytes = 134
	pythPushPriceOffset  = 8 + 32 + 1 + 32

	pythPushVerificationFull = 1

	// Measured publish ages on 2026-08-04 ranged from 15s to 58s across both
	// accounts on devnet and mainnet, so a 30s budget would reject a healthy
	// feed about half the time. The caller's policy still applies its own
	// stricter age limit on top of this ceiling.
	pythPushMaxAge = 150 * time.Second

	// A price dated ahead of us is a clock or data fault, not freshness.
	pythPushMaxFutureSkew = 10 * time.Second

	// The two accounts must agree while both publish; a wide gap means one has
	// drifted and neither should be trusted silently.
	pythPushMaxCrossDeviationBPS = 200

	pythPushIdentityDescription     = "mithril-agent/price-source-v2|pyth-push-onchain|mithril-getaccountinfo|stable:SOL/USD|aggregate-confidence"
	pythPushUSDCIdentityDescription = "mithril-agent/price-source-v2|pyth-push-onchain|mithril-getaccountinfo|stable:USDC/USD|aggregate-confidence"
	pythPushJUPIdentityDescription  = "mithril-agent/price-source-v2|pyth-push-onchain|mithril-getaccountinfo|stable:JUP/USD|aggregate-confidence"
)

var pythPushDiscriminator = [8]byte{0x22, 0xf1, 0x23, 0x63, 0x9d, 0x7e, 0xf4, 0xcd}

// pythPushFeed pins one sponsored account to the exact program allowed to own
// it. An address alone is not sufficient: a program upgrade that re-pointed an
// account would otherwise go unnoticed.
type pythPushFeed struct {
	account string
	owner   string
}

var pythPushFeeds = []pythPushFeed{
	{account: pythPushLegacyAccount, owner: pythPushLegacyOwner},
	{account: pythPushUpgradedAccount, owner: pythPushUpgradedOwner},
}

var pythPushUSDCFeeds = []pythPushFeed{
	{account: pythPushUSDCAccount, owner: pythPushLegacyOwner},
	{account: pythPushUSDCUpgradedAccount, owner: pythPushUpgradedOwner},
}

var pythPushJUPFeeds = []pythPushFeed{
	{account: pythPushJUPAccount, owner: pythPushLegacyOwner},
	{account: pythPushJUPUpgradedAccount, owner: pythPushUpgradedOwner},
}

type pythPushDefinition struct {
	feed     string
	feedID   string
	identity string
	accounts []pythPushFeed
}

// PythPushSpec pins an allowlisted candidate feed to its stable ID and both sponsored
// Solana accounts. The accounts are derived and checked from the feed ID, so a
// display ticker can never redirect the reader to a different asset.
type PythPushSpec struct {
	Feed            string `json:"feed"`
	FeedID          string `json:"feed_id"`
	LegacyAccount   string `json:"legacy_account"`
	UpgradedAccount string `json:"upgraded_account"`
}

// NewPythPushSpec derives the upgraded shard-zero account and verifies the
// published legacy account against the same canonical PDA rule.
func NewPythPushSpec(feed, feedID, legacyAccount string) (PythPushSpec, error) {
	decoded, err := hex.DecodeString(feedID)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != feedID {
		return PythPushSpec{}, errors.New("Pyth push feed ID is invalid")
	}
	shard := []byte{0, 0}
	legacy, _, err := solana.FindProgramAddress([][]byte{shard, decoded}, pythPushLegacyProgram)
	if err != nil || legacy != legacyAccount {
		return PythPushSpec{}, errors.New("Pyth legacy account does not match its feed ID")
	}
	upgraded, _, err := solana.FindProgramAddress([][]byte{shard, decoded}, pythPushUpgradedProgram)
	if err != nil {
		return PythPushSpec{}, errors.New("derive Pyth upgraded account")
	}
	spec := PythPushSpec{
		Feed: feed, FeedID: feedID, LegacyAccount: legacyAccount, UpgradedAccount: upgraded,
	}
	if err := spec.Validate(); err != nil {
		return PythPushSpec{}, err
	}
	return spec, nil
}

func (spec PythPushSpec) Validate() error {
	if !pricetrigger.ValidUSDFeed(spec.Feed) {
		return errors.New("Pyth push feed name is invalid")
	}
	feedID, err := hex.DecodeString(spec.FeedID)
	if err != nil || len(feedID) != 32 || hex.EncodeToString(feedID) != spec.FeedID {
		return errors.New("Pyth push feed ID is invalid")
	}
	shard := []byte{0, 0}
	legacy, _, err := solana.FindProgramAddress([][]byte{shard, feedID}, pythPushLegacyProgram)
	if err != nil || legacy != spec.LegacyAccount {
		return errors.New("Pyth legacy account does not match its feed ID")
	}
	upgraded, _, err := solana.FindProgramAddress([][]byte{shard, feedID}, pythPushUpgradedProgram)
	if err != nil || upgraded != spec.UpgradedAccount {
		return errors.New("Pyth upgraded account does not match its feed ID")
	}
	return nil
}

func (spec PythPushSpec) definition() (pythPushDefinition, error) {
	if err := spec.Validate(); err != nil {
		return pythPushDefinition{}, err
	}
	identity := fmt.Sprintf(
		"mithril-agent/price-source-v3|pyth-push-onchain|mithril-getaccountinfo|stable:%s|feed:%s|accounts:%s,%s|programs:%s,%s|owners:%s,%s|aggregate-confidence",
		spec.Feed, spec.FeedID, spec.LegacyAccount, spec.UpgradedAccount,
		pythPushLegacyProgram, pythPushUpgradedProgram,
		pythPushLegacyOwner, pythPushUpgradedOwner,
	)
	return pythPushDefinition{
		feed: spec.Feed, feedID: spec.FeedID, identity: identity,
		accounts: []pythPushFeed{
			{account: spec.LegacyAccount, owner: pythPushLegacyOwner},
			{account: spec.UpgradedAccount, owner: pythPushUpgradedOwner},
		},
	}, nil
}

func (spec PythPushSpec) IdentitySHA256() (string, error) {
	definition, err := spec.definition()
	if err != nil {
		return "", err
	}
	return sourceIdentity(definition.identity), nil
}

// PythPushSOLSpec returns the pinned SOL/USD account provenance.
func PythPushSOLSpec() PythPushSpec {
	return PythPushSpec{
		Feed: pricetrigger.FeedSOLUSD, FeedID: SOLUSDFeedID,
		LegacyAccount: pythPushLegacyAccount, UpgradedAccount: pythPushUpgradedAccount,
	}
}

// PythPushUSDCSpec returns the pinned USDC/USD account provenance.
func PythPushUSDCSpec() PythPushSpec {
	return PythPushSpec{
		Feed: pricetrigger.FeedUSDCUSD, FeedID: USDCUSDFeedID,
		LegacyAccount: pythPushUSDCAccount, UpgradedAccount: pythPushUSDCUpgradedAccount,
	}
}

var (
	pythPushSOLDefinition = pythPushDefinition{
		feed: pricetrigger.FeedSOLUSD, feedID: SOLUSDFeedID,
		identity: pythPushIdentityDescription, accounts: pythPushFeeds,
	}
	pythPushUSDCDefinition = pythPushDefinition{
		feed: pricetrigger.FeedUSDCUSD, feedID: USDCUSDFeedID,
		identity: pythPushUSDCIdentityDescription, accounts: pythPushUSDCFeeds,
	}
	pythPushJUPDefinition = pythPushDefinition{
		feed: pricetrigger.FeedJUPUSD, feedID: JUPUSDFeedID,
		identity: pythPushJUPIdentityDescription, accounts: pythPushJUPFeeds,
	}
)

func PythPushIdentitySHA256() string {
	hash := sha256.Sum256([]byte(pythPushIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func PythPushUSDCIdentitySHA256() string {
	hash := sha256.Sum256([]byte(pythPushUSDCIdentityDescription))
	return hex.EncodeToString(hash[:])
}

func PythPushJUPIdentitySHA256() string {
	hash := sha256.Sum256([]byte(pythPushJUPIdentityDescription))
	return hex.EncodeToString(hash[:])
}

// AccountReader is the narrow slice of Mithril RPC this adapter needs. Keeping
// it this small means the adapter cannot reach any other node capability.
type AccountReader interface {
	AccountSlice(ctx context.Context, address string, minContextSlot, offset, length uint64) (AccountData, error)
}

// AccountData mirrors the reader's result without importing the RPC package,
// so pricesource stays free of a submission-capable dependency.
type AccountData struct {
	ContextSlot uint64
	Owner       string
	DataLength  uint64
	Data        []byte
}

// PythPush reads the sponsored SOL/USD push feed through a Mithril node.
type PythPush struct {
	reader     AccountReader
	now        func() time.Time
	definition pythPushDefinition
}

// PythObservation keeps account provenance out of the shared price-sample
// schema used by existing live and paper journals.
type PythObservation struct {
	Sample      pricetrigger.Sample `json:"sample"`
	ContextSlot uint64              `json:"context_slot"`
	Account     string              `json:"account"`
	FeedID      string              `json:"feed_id"`
}

func NewPythPush(reader AccountReader, now func() time.Time) (*PythPush, error) {
	return newPythPush(reader, now, pythPushSOLDefinition)
}

func NewPythPushUSDC(reader AccountReader, now func() time.Time) (*PythPush, error) {
	return newPythPush(reader, now, pythPushUSDCDefinition)
}

func NewPythPushJUP(reader AccountReader, now func() time.Time) (*PythPush, error) {
	return newPythPush(reader, now, pythPushJUPDefinition)
}

// NewPythPushFromSpec creates an observation-only allowlisted-market source.
// The existing SOL, USDC, and JUP constructors retain their exact identities.
func NewPythPushFromSpec(
	reader AccountReader,
	now func() time.Time,
	spec PythPushSpec,
) (*PythPush, error) {
	definition, err := spec.definition()
	if err != nil {
		return nil, err
	}
	return newPythPush(reader, now, definition)
}

func newPythPush(
	reader AccountReader,
	now func() time.Time,
	definition pythPushDefinition,
) (*PythPush, error) {
	if reader == nil {
		return nil, errors.New("Pyth push source requires a Mithril account reader")
	}
	if now == nil {
		now = time.Now
	}
	return &PythPush{reader: reader, now: now, definition: definition}, nil
}

func (source *PythPush) IdentitySHA256() string {
	hash := sha256.Sum256([]byte(source.definition.identity))
	return hex.EncodeToString(hash[:])
}

// Latest performs an advisory read with no slot floor. It is for deciding
// whether to keep waiting and for operator display; it never authorizes an
// action. Staleness is still rejected here, because the feed's own publish time
// — not the node's slot — is what proves the price is current.
func (source *PythPush) Latest(ctx context.Context, feed string) (pricetrigger.Sample, error) {
	observation, err := source.readObservation(ctx, feed, 0)
	return observation.Sample, err
}

// LatestObservation returns the advisory price and the exact account that
// supplied it. Existing Latest callers retain the legacy Sample schema.
func (source *PythPush) LatestObservation(
	ctx context.Context,
	feed string,
) (PythObservation, error) {
	return source.readObservation(ctx, feed, 0)
}

// LatestAtSlot is the authorizing read. The slot floor additionally proves the
// node has reached a point the caller already verified, so a node that has
// stalled cannot serve old account state as current.
func (source *PythPush) LatestAtSlot(
	ctx context.Context,
	feed string,
	minContextSlot uint64,
) (pricetrigger.Sample, error) {
	if minContextSlot == 0 {
		return pricetrigger.Sample{}, errors.New("Pyth push authorizing read requires a proven context slot")
	}
	observation, err := source.readObservation(ctx, feed, minContextSlot)
	return observation.Sample, err
}

func (source *PythPush) readObservation(
	ctx context.Context,
	feed string,
	minContextSlot uint64,
) (PythObservation, error) {
	if feed != source.definition.feed {
		return PythObservation{}, errors.New("Pyth push price feed is unsupported")
	}

	now := source.now().UTC()
	type candidate struct {
		observation PythObservation
		age         time.Duration
	}
	var accepted []candidate
	var lastErr error

	for _, pinned := range source.definition.accounts {
		account, err := source.reader.AccountSlice(
			ctx, pinned.account, minContextSlot, 0, pythPushAccountBytes)
		if err != nil {
			// The endpoint and account identity must never reach an error.
			lastErr = errors.New("read Pyth push account")
			continue
		}
		observation, age, err := decodePythPush(account, pinned, source.definition, now)
		if err != nil {
			lastErr = err
			continue
		}
		accepted = append(accepted, candidate{observation: observation, age: age})
	}

	if len(accepted) == 0 {
		if lastErr == nil {
			lastErr = errors.New("no Pyth push account was usable")
		}
		return PythObservation{}, lastErr
	}
	if len(accepted) > 1 {
		if err := requireCloseEnough(
			accepted[0].observation.Sample.PriceMicros,
			accepted[1].observation.Sample.PriceMicros,
			pythPushMaxCrossDeviationBPS,
		); err != nil {
			return PythObservation{}, err
		}
	}
	freshest := accepted[0]
	for _, c := range accepted[1:] {
		if c.age < freshest.age {
			freshest = c
		}
	}
	return freshest.observation, nil
}

// decodePythPush validates every pinned identity before trusting any number,
// and uses only integer arithmetic so no rounding can widen a threshold.
func decodePythPush(
	account AccountData,
	pinned pythPushFeed,
	definition pythPushDefinition,
	now time.Time,
) (PythObservation, time.Duration, error) {
	if account.Owner != pinned.owner {
		return PythObservation{}, 0, errors.New("Pyth push account owner is unexpected")
	}
	if account.DataLength != pythPushAccountBytes || len(account.Data) != pythPushAccountBytes {
		return PythObservation{}, 0, errors.New("Pyth push account length is unexpected")
	}
	if [8]byte(account.Data[:8]) != pythPushDiscriminator {
		return PythObservation{}, 0, errors.New("Pyth push account discriminator is unexpected")
	}
	if account.Data[8+32] != pythPushVerificationFull {
		return PythObservation{}, 0, errors.New("Pyth push price is not fully verified")
	}
	if hex.EncodeToString(account.Data[8+32+1:8+32+1+32]) != definition.feedID {
		return PythObservation{}, 0, errors.New("Pyth push account is for the wrong feed")
	}

	body := account.Data[pythPushPriceOffset:]
	price := int64(binary.LittleEndian.Uint64(body[0:8]))
	confidence := binary.LittleEndian.Uint64(body[8:16])
	exponent := int32(binary.LittleEndian.Uint32(body[16:20]))
	publishTime := int64(binary.LittleEndian.Uint64(body[20:28]))

	if price <= 0 {
		return PythObservation{}, 0, errors.New("Pyth push price is not positive")
	}
	priceMicros, err := pythPushMicros(uint64(price), exponent)
	if err != nil {
		return PythObservation{}, 0, err
	}
	confidenceMicros, err := pythPushMicros(confidence, exponent)
	if err != nil {
		return PythObservation{}, 0, err
	}

	if publishTime <= 0 {
		return PythObservation{}, 0, errors.New("Pyth push publish time is invalid")
	}
	publishedAt := time.Unix(publishTime, 0).UTC()
	if publishedAt.After(now.Add(pythPushMaxFutureSkew)) {
		return PythObservation{}, 0, errors.New("Pyth push publish time is in the future")
	}
	age := now.Sub(publishedAt)
	if age > pythPushMaxAge {
		return PythObservation{}, 0, errors.New("Pyth push price is stale")
	}

	return PythObservation{
		Sample: pricetrigger.Sample{
			SourceSHA256: sourceIdentity(definition.identity), Feed: definition.feed,
			PriceMicros: priceMicros, ConfidenceMicros: confidenceMicros,
			PublishedAt: publishedAt,
		},
		ContextSlot: account.ContextSlot, Account: pinned.account, FeedID: definition.feedID,
	}, age, nil
}

func sourceIdentity(description string) string {
	hash := sha256.Sum256([]byte(description))
	return hex.EncodeToString(hash[:])
}

// pythPushMicros converts value*10^exponent into micro-units exactly. Pyth's
// exponent is normally -8, so this is usually a division by 100 that must not
// silently round a price upward.
func pythPushMicros(value uint64, exponent int32) (uint64, error) {
	if exponent > 0 || exponent < -18 {
		return 0, errors.New("Pyth push exponent is out of range")
	}
	scaled := new(big.Int).SetUint64(value)
	scaled.Mul(scaled, big.NewInt(1_000_000))

	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exponent)), nil)
	scaled.Quo(scaled, divisor) // truncates, so a price never rounds up

	if !scaled.IsUint64() || scaled.Uint64() > pricetrigger.MaxPriceMicros {
		return 0, errors.New("Pyth push price is out of range")
	}
	return scaled.Uint64(), nil
}

// requireCloseEnough rejects two published prices that have drifted apart,
// which is how a silently frozen feed reveals itself while the other still
// moves.
func requireCloseEnough(first, second uint64, maxBPS uint64) error {
	higher, lower := first, second
	if lower > higher {
		higher, lower = lower, higher
	}
	if higher == 0 {
		return errors.New("Pyth push price is not positive")
	}
	if (higher-lower)*10_000 > higher*maxBPS {
		return errors.New("Pyth push accounts disagree")
	}
	return nil
}
