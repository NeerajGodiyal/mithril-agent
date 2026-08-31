package pricesource

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// validPushAccount builds a byte-exact PriceUpdateV2 account matching the
// layout observed live on 2026-08-04 (134 bytes, 133 consumed plus one pad).
func validPushAccount(price int64, confidence uint64, exponent int32, publish int64) []byte {
	return validPushAccountFor(SOLUSDFeedID, price, confidence, exponent, publish)
}

func validPushAccountFor(
	feedID string, price int64, confidence uint64, exponent int32, publish int64,
) []byte {
	data := make([]byte, pythPushAccountBytes)
	copy(data[0:8], pythPushDiscriminator[:])
	// write_authority occupies 8..40 and is not validated.
	data[8+32] = pythPushVerificationFull
	feed, _ := hex.DecodeString(feedID)
	copy(data[8+32+1:], feed)

	body := data[pythPushPriceOffset:]
	binary.LittleEndian.PutUint64(body[0:8], uint64(price))
	binary.LittleEndian.PutUint64(body[8:16], confidence)
	binary.LittleEndian.PutUint32(body[16:20], uint32(exponent))
	binary.LittleEndian.PutUint64(body[20:28], uint64(publish))
	return data
}

func TestPythPushReadsSponsoredUSDCFeed(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	data := validPushAccountFor(USDCUSDFeedID, 99_999_800, 1_000, -8,
		now.Add(-20*time.Second).Unix())
	reader := &fakeReader{byAccount: map[string]AccountData{
		pythPushUSDCAccount: {
			ContextSlot: 100, Owner: pythPushLegacyOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
		pythPushUSDCUpgradedAccount: {
			ContextSlot: 100, Owner: pythPushUpgradedOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
	}}
	source, err := NewPythPushUSDC(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	sample, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedUSDCUSD, 99)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != PythPushUSDCIdentitySHA256() ||
		sample.Feed != pricetrigger.FeedUSDCUSD || sample.PriceMicros != 999_998 ||
		sample.ConfidenceMicros != 10 {
		t.Fatalf("sample = %+v", sample)
	}
	if PythPushUSDCIdentitySHA256() == PythPushIdentitySHA256() {
		t.Fatal("SOL and USDC feed identities collided")
	}
}

func TestPythPushReadsPinnedJUPMigrationFeeds(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	data := validPushAccountFor(
		JUPUSDFeedID, 48_500_000, 20_000, -8, now.Add(-20*time.Second).Unix(),
	)
	reader := &fakeReader{byAccount: map[string]AccountData{
		pythPushJUPAccount: {
			ContextSlot: 100, Owner: pythPushLegacyOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
		pythPushJUPUpgradedAccount: {
			ContextSlot: 100, Owner: pythPushUpgradedOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
	}}
	source, err := NewPythPushJUP(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	sample, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedJUPUSD, 99)
	if err != nil {
		t.Fatal(err)
	}
	if sample.SourceSHA256 != PythPushJUPIdentitySHA256() ||
		sample.Feed != pricetrigger.FeedJUPUSD || sample.PriceMicros != 485_000 ||
		sample.ConfidenceMicros != 200 {
		t.Fatalf("JUP sample = %+v", sample)
	}
	if PythPushJUPIdentitySHA256() == PythPushIdentitySHA256() ||
		PythPushJUPIdentitySHA256() == PythPushUSDCIdentitySHA256() {
		t.Fatal("JUP source identity collided with another feed")
	}
}

func TestPythPushReadsAnAdmissionBoundFeed(t *testing.T) {
	const (
		feedID = "4ca4beeca86f0d164160323817a4e42b10010a724c2217c6ee41b54cd4cc61fc"
		legacy = "6B23K3tkb51vLZA14jcEQVCA1pfHptzEHFA93V5dYwbT"
	)
	spec, err := NewPythPushSpec("WIF/USD", feedID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if spec.UpgradedAccount == "" || spec.UpgradedAccount == spec.LegacyAccount {
		t.Fatalf("derived spec = %+v", spec)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	data := validPushAccountFor(feedID, 19_405_000, 10_000, -8, now.Add(-time.Second).Unix())
	reader := &fakeReader{byAccount: map[string]AccountData{
		spec.LegacyAccount: {
			ContextSlot: 100, Owner: pythPushLegacyOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
		spec.UpgradedAccount: {
			ContextSlot: 100, Owner: pythPushUpgradedOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
	}}
	source, err := NewPythPushFromSpec(reader, func() time.Time { return now }, spec)
	if err != nil {
		t.Fatal(err)
	}
	if source.IdentitySHA256() != "a3c54705c3da370b3160839a255943b2914017413eeb1abeb05fd0daf659a8c9" {
		t.Fatalf("unexpected trust-anchor identity: %s", source.IdentitySHA256())
	}
	sample, err := source.Latest(t.Context(), spec.Feed)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Feed != "WIF/USD" || sample.PriceMicros != 194_050 ||
		sample.SourceSHA256 != source.IdentitySHA256() ||
		sample.SourceSHA256 == PythPushIdentitySHA256() {
		t.Fatalf("sample = %+v", sample)
	}
	observation, err := source.LatestObservation(t.Context(), spec.Feed)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Sample != sample || observation.ContextSlot != 100 ||
		observation.FeedID != feedID || (observation.Account != spec.LegacyAccount &&
		observation.Account != spec.UpgradedAccount) {
		t.Fatalf("observation = %+v", observation)
	}

	tampered := spec
	tampered.LegacyAccount = spec.UpgradedAccount
	if _, err := NewPythPushFromSpec(reader, time.Now, tampered); err == nil {
		t.Fatal("tampered Pyth account binding was accepted")
	}
}

func TestPinnedSOLAndUSDCPushSpecsRemainValid(t *testing.T) {
	for _, spec := range []PythPushSpec{PythPushSOLSpec(), PythPushUSDCSpec()} {
		if err := spec.Validate(); err != nil {
			t.Fatalf("pinned spec %+v: %v", spec, err)
		}
	}
}

func TestPythPushUSDCSurvivesLegacyFeedRetirement(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	data := validPushAccountFor(USDCUSDFeedID, 99_999_800, 1_000, -8,
		now.Add(-20*time.Second).Unix())
	reader := &fakeReader{
		byAccount: map[string]AccountData{
			pythPushUSDCUpgradedAccount: {
				ContextSlot: 100, Owner: pythPushUpgradedOwner,
				DataLength: pythPushAccountBytes, Data: data,
			},
		},
		err: map[string]error{pythPushUSDCAccount: errors.New("legacy feed retired")},
	}
	source, err := NewPythPushUSDC(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedUSDCUSD, 99); err != nil {
		t.Fatalf("upgraded USDC feed did not carry the read: %v", err)
	}
}

func TestPythPushUSDCRejectsDisagreeingMigrationFeeds(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-20 * time.Second).Unix()
	reader := &fakeReader{byAccount: map[string]AccountData{
		pythPushUSDCAccount: {
			ContextSlot: 100, Owner: pythPushLegacyOwner, DataLength: pythPushAccountBytes,
			Data: validPushAccountFor(USDCUSDFeedID, 100_000_000, 1_000, -8, fresh),
		},
		pythPushUSDCUpgradedAccount: {
			ContextSlot: 100, Owner: pythPushUpgradedOwner, DataLength: pythPushAccountBytes,
			Data: validPushAccountFor(USDCUSDFeedID, 110_000_000, 1_000, -8, fresh),
		},
	}}
	source, err := NewPythPushUSDC(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedUSDCUSD, 99); err == nil {
		t.Fatal("disagreeing legacy and upgraded USDC feeds were accepted")
	}
}

func TestPythPushUSDCRejectsWrongFeedAndOwner(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for name, account := range map[string]AccountData{
		"wrong feed": {
			Owner: pythPushLegacyOwner, DataLength: pythPushAccountBytes,
			Data: validPushAccount(99_999_800, 1_000, -8, now.Unix()),
		},
		"wrong owner": {
			Owner: pythPushUpgradedOwner, DataLength: pythPushAccountBytes,
			Data: validPushAccountFor(USDCUSDFeedID, 99_999_800, 1_000, -8, now.Unix()),
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{byAccount: map[string]AccountData{pythPushUSDCAccount: account}}
			source, err := NewPythPushUSDC(reader, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Latest(t.Context(), pricetrigger.FeedUSDCUSD); err == nil {
				t.Fatal("tampered USDC account was accepted")
			}
		})
	}
}

type fakeReader struct {
	byAccount map[string]AccountData
	err       map[string]error
	minSlots  []uint64
}

func (r *fakeReader) AccountSlice(
	_ context.Context, address string, minContextSlot, _, length uint64,
) (AccountData, error) {
	r.minSlots = append(r.minSlots, minContextSlot)
	if err, ok := r.err[address]; ok {
		return AccountData{}, err
	}
	account, ok := r.byAccount[address]
	if !ok {
		return AccountData{}, errors.New("absent")
	}
	if length != pythPushAccountBytes {
		return AccountData{}, errors.New("unexpected slice length")
	}
	return account, nil
}

func pushFixture(t *testing.T, now time.Time) (*PythPush, *fakeReader) {
	t.Helper()
	data := validPushAccount(7_354_900_000, 4_400_000, -8, now.Add(-20*time.Second).Unix())
	reader := &fakeReader{byAccount: map[string]AccountData{
		pythPushLegacyAccount: {
			ContextSlot: 100, Owner: pythPushLegacyOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
		pythPushUpgradedAccount: {
			ContextSlot: 100, Owner: pythPushUpgradedOwner,
			DataLength: pythPushAccountBytes, Data: data,
		},
	}}
	source, err := NewPythPush(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return source, reader
}

func TestPythPushDecodesLiveLayout(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	source, reader := pushFixture(t, now)

	sample, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 99)
	if err != nil {
		t.Fatal(err)
	}
	// 7_354_900_000 * 10^-8 = 73.549 USD = 73_549_000 micro-USD.
	if sample.PriceMicros != 73_549_000 {
		t.Fatalf("price = %d micros, want 73549000", sample.PriceMicros)
	}
	if sample.ConfidenceMicros != 44_000 {
		t.Fatalf("confidence = %d micros, want 44000", sample.ConfidenceMicros)
	}
	if sample.SourceSHA256 != PythPushIdentitySHA256() || sample.Feed != pricetrigger.FeedSOLUSD {
		t.Fatalf("sample identity = %+v", sample)
	}
	// The proven slot must be passed to every read, or a stalled node could
	// serve an old account as current.
	for _, got := range reader.minSlots {
		if got != 99 {
			t.Fatalf("read used minContextSlot %d, want 99", got)
		}
	}
}

func TestPythPushRejectsEveryTamperedField(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-20 * time.Second).Unix()

	tests := map[string]struct {
		mutate func(*AccountData)
		want   string
	}{
		// An owner that is wrong for BOTH pinned accounts; using the other
		// account's owner would legitimately pass for one of them.
		"wrong owner": {
			mutate: func(a *AccountData) { a.Owner = "11111111111111111111111111111111" },
			want:   "owner",
		},
		"truncated data": {
			mutate: func(a *AccountData) { a.Data = a.Data[:pythPushAccountBytes-1] },
			want:   "length",
		},
		"oversized data": {
			mutate: func(a *AccountData) { a.Data = append(a.Data, 0) },
			want:   "length",
		},
		"length field disagrees with payload": {
			mutate: func(a *AccountData) { a.DataLength = pythPushAccountBytes - 1 },
			want:   "length",
		},
		"wrong discriminator": {
			mutate: func(a *AccountData) { a.Data[0] ^= 0xff },
			want:   "discriminator",
		},
		"partially verified": {
			mutate: func(a *AccountData) { a.Data[8+32] = 0 },
			want:   "verified",
		},
		"wrong feed id": {
			mutate: func(a *AccountData) { a.Data[8+32+1] ^= 0xff },
			want:   "wrong feed",
		},
		"negative price": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint64(a.Data[pythPushPriceOffset:], ^uint64(0))
			},
			want: "positive",
		},
		"zero price": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint64(a.Data[pythPushPriceOffset:], 0)
			},
			want: "positive",
		},
		"positive exponent": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint32(a.Data[pythPushPriceOffset+16:], uint32(1))
			},
			want: "exponent",
		},
		"exponent below range": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint32(a.Data[pythPushPriceOffset+16:], uint32(0xFFFFFFED))
			},
			want: "exponent",
		},
		"price overflows the bound": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint64(a.Data[pythPushPriceOffset:], uint64(1)<<62)
			},
			want: "range",
		},
		"stale publish time": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint64(a.Data[pythPushPriceOffset+20:],
					uint64(now.Add(-10*time.Minute).Unix()))
			},
			want: "stale",
		},
		"future publish time": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint64(a.Data[pythPushPriceOffset+20:],
					uint64(now.Add(time.Hour).Unix()))
			},
			want: "future",
		},
		"zero publish time": {
			mutate: func(a *AccountData) {
				binary.LittleEndian.PutUint64(a.Data[pythPushPriceOffset+20:], 0)
			},
			want: "publish time",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// Mutate BOTH accounts: if only one were broken the other would
			// legitimately satisfy the read and hide the rejection.
			reader := &fakeReader{byAccount: map[string]AccountData{}}
			for _, pinned := range pythPushFeeds {
				account := AccountData{
					ContextSlot: 100, Owner: pinned.owner,
					DataLength: pythPushAccountBytes,
					Data:       validPushAccount(7_354_900_000, 4_400_000, -8, fresh),
				}
				test.mutate(&account)
				reader.byAccount[pinned.account] = account
			}
			source, err := NewPythPush(reader, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 99)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

// A frozen feed reveals itself by drifting away from the one still publishing.
func TestPythPushRejectsDisagreeingAccounts(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-20 * time.Second).Unix()
	reader := &fakeReader{byAccount: map[string]AccountData{
		pythPushLegacyAccount: {
			ContextSlot: 100, Owner: pythPushLegacyOwner, DataLength: pythPushAccountBytes,
			Data: validPushAccount(7_354_900_000, 4_400_000, -8, fresh),
		},
		pythPushUpgradedAccount: {
			ContextSlot: 100, Owner: pythPushUpgradedOwner, DataLength: pythPushAccountBytes,
			// 10% away — far outside the 200bps cross bound.
			Data: validPushAccount(8_090_390_000, 4_400_000, -8, fresh),
		},
	}}
	source, err := NewPythPush(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 99); err == nil {
		t.Fatal("disagreeing accounts were accepted")
	}
}

// After the completed upgrade, one account may stop and the other must carry the read.
func TestPythPushSurvivesOneAccountFailing(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-20 * time.Second).Unix()
	reader := &fakeReader{
		byAccount: map[string]AccountData{
			pythPushUpgradedAccount: {
				ContextSlot: 100, Owner: pythPushUpgradedOwner, DataLength: pythPushAccountBytes,
				Data: validPushAccount(7_354_900_000, 4_400_000, -8, fresh),
			},
		},
		err: map[string]error{pythPushLegacyAccount: errors.New("account closed")},
	}
	source, err := NewPythPush(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	sample, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 99)
	if err != nil {
		t.Fatalf("a single healthy account should still serve the read: %v", err)
	}
	if sample.PriceMicros != 73_549_000 {
		t.Fatalf("price = %d", sample.PriceMicros)
	}
}

func TestPythPushFailsClosedWhenBothAccountsAreUnavailable(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeReader{
		byAccount: map[string]AccountData{},
		err: map[string]error{
			pythPushLegacyAccount:   errors.New("node unreachable"),
			pythPushUpgradedAccount: errors.New("node unreachable"),
		},
	}
	source, err := NewPythPush(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 99)
	if err == nil {
		t.Fatal("unavailable node produced a price")
	}
	// The endpoint and account identity must never reach an operator-visible error.
	for _, leak := range []string{pythPushLegacyAccount, pythPushUpgradedAccount, "unreachable"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaked %q: %v", leak, err)
		}
	}
}

func TestPythPushRequiresProvenSlotAndSupportedFeed(t *testing.T) {
	now := time.Now().UTC()
	source, _ := pushFixture(t, now)

	if _, err := source.LatestAtSlot(t.Context(), "BTC/USD", 99); err == nil {
		t.Fatal("an unsupported feed was accepted")
	}
	// The authorizing read must refuse without a proven slot.
	if _, err := source.LatestAtSlot(t.Context(), pricetrigger.FeedSOLUSD, 0); err == nil {
		t.Fatal("authorizing read without a proven slot was allowed")
	}
	// The advisory read is allowed without a slot: it only decides whether to
	// keep waiting, and staleness is still gated on the feed's publish time.
	if _, err := source.Latest(t.Context(), pricetrigger.FeedSOLUSD); err != nil {
		t.Fatalf("advisory read was refused: %v", err)
	}
}

// Conversion must never round a price up, or a threshold could be crossed by
// arithmetic rather than by the market.
func TestPythPushMicrosTruncatesAndBoundsExponent(t *testing.T) {
	tests := []struct {
		value    uint64
		exponent int32
		want     uint64
	}{
		{value: 7_354_900_000, exponent: -8, want: 73_549_000},
		{value: 7_354_999_999, exponent: -8, want: 73_549_999}, // truncated, not 73_550_000
		{value: 1, exponent: -8, want: 0},                      // below a micro rounds to zero, never up
		{value: 73, exponent: 0, want: 73_000_000},
	}
	for _, test := range tests {
		got, err := pythPushMicros(test.value, test.exponent)
		if err != nil {
			t.Fatalf("pythPushMicros(%d,%d): %v", test.value, test.exponent, err)
		}
		if got != test.want {
			t.Errorf("pythPushMicros(%d,%d) = %d, want %d", test.value, test.exponent, got, test.want)
		}
	}
	if _, err := pythPushMicros(1, 1); err == nil {
		t.Error("positive exponent accepted")
	}
	if _, err := pythPushMicros(1, -19); err == nil {
		t.Error("exponent below range accepted")
	}
}

// The push adapter is a distinct trust domain from Hermes, so its identity
// must not collide with the keyed adapter's.
func TestPythPushIdentityDiffersFromHermes(t *testing.T) {
	if PythPushIdentitySHA256() == PythIdentitySHA256() {
		t.Fatal("push and Hermes adapters share an identity hash")
	}
	if len(PythPushIdentitySHA256()) != 64 {
		t.Fatal("identity hash is not a sha256 hex digest")
	}
}

// An advisory read must still refuse a stale feed: publish time, not the node
// slot, is what proves the price is current.
func TestPythPushAdvisoryReadStillRejectsStalePrice(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-10 * time.Minute).Unix()
	reader := &fakeReader{byAccount: map[string]AccountData{}}
	for _, pinned := range pythPushFeeds {
		reader.byAccount[pinned.account] = AccountData{
			ContextSlot: 100, Owner: pinned.owner, DataLength: pythPushAccountBytes,
			Data: validPushAccount(7_354_900_000, 4_400_000, -8, stale),
		}
	}
	source, err := NewPythPush(reader, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Latest(t.Context(), pricetrigger.FeedSOLUSD); err == nil {
		t.Fatal("advisory read accepted a stale price")
	}
}
