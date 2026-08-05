package swaprun

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

func TestSellProfileEncodingDoesNotChangeForDormantBuyFields(t *testing.T) {
	encoded, err := json.Marshal(testProfile())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("buy_route"), []byte("input_token_amount"),
		[]byte("daily_input_token_cap"), []byte("daily_native_fee_cap_lamports"),
	} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("sell profile contains dormant field %q", forbidden)
		}
	}
}

func TestBuyProfileContract(t *testing.T) {
	profile := testBuyProfile(t)
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	price, err := executablePriceMicros(profile, 3)
	if err != nil || price != 333_333_333_334 {
		t.Fatalf("ceiling buy price = %d, %v", price, err)
	}
	profile.PriceTrigger.ThresholdMicros = price
	if !executableMinimumSatisfies(profile, 3) {
		t.Fatal("buy executable price at the threshold was rejected")
	}
	profile.PriceTrigger.ThresholdMicros--
	if executableMinimumSatisfies(profile, 3) {
		t.Fatal("buy executable price above the threshold was accepted")
	}

	mutations := map[string]func(*Profile){
		"name":             func(value *Profile) { value.Name = orcaswap.ProfileName },
		"version":          func(value *Profile) { value.Version++ },
		"cluster":          func(value *Profile) { value.Cluster = "mainnet" },
		"sell route":       func(value *Profile) { value.Route = testProfile().Route },
		"sell amount":      func(value *Profile) { value.InputLamports = 1 },
		"sell daily cap":   func(value *Profile) { value.DailyDebitCapLamports = 1 },
		"missing route":    func(value *Profile) { value.BuyRoute = nil },
		"route input cap":  func(value *Profile) { value.BuyRoute.MaxInputTokenAmount-- },
		"zero input":       func(value *Profile) { value.InputTokenAmount = 0 },
		"daily input":      func(value *Profile) { value.DailyInputTokenCap-- },
		"zero fee":         func(value *Profile) { value.MaxFeeLamports = 0 },
		"daily fee":        func(value *Profile) { value.DailyNativeFeeCapLamports-- },
		"zero reserve":     func(value *Profile) { value.ReserveLamports = 0 },
		"reserve overflow": func(value *Profile) { value.ReserveLamports = ^uint64(0) },
		"wrong trigger": func(value *Profile) {
			value.PriceTrigger.Direction = pricetrigger.SellAtOrAbove
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := testBuyProfile(t)
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("mutated buy profile was accepted")
			}
		})
	}
}

func testBuyProfile(t *testing.T) Profile {
	t.Helper()
	seed := sha256.Sum256([]byte("buy-profile-owner"))
	key := ed25519.NewKeyFromSeed(seed[:])
	owner := solana.Encode(key.Public().(ed25519.PublicKey))
	input, err := orcaswap.AssociatedTokenAddress(owner, orcaswap.DevnetUSDCMint)
	if err != nil {
		t.Fatal(err)
	}
	return Profile{
		Name: orcaswap.BuyProfileName, Version: orcaswap.BuyProfileVersion, Cluster: "devnet",
		BuyRoute: &orcaswap.BuyPolicyV2{
			Owner: owner, Pool: orcaswap.DevnetPool,
			TokenMintA: orcaswap.WrappedSOLMint, TokenMintB: orcaswap.DevnetUSDCMint,
			InputTokenAccount:   input,
			TokenVaultA:         "C9zLV5zWF66j3rZj3uuhDqvfuA8esJyWnruGzDW9qEj2",
			TokenVaultB:         "7DM3RMz2yzUB8yPRQM3FMZgdFrwZGMsabsfsKopWktoX",
			Oracle:              "2KEWNc3b6EfqoWQpfKQMHh4mhRyKXYRdPbtGRTJX3Cip",
			ProgramData:         orcaswap.WhirlpoolProgramData,
			UpgradeAuthority:    orcaswap.WhirlpoolUpgradeAuth,
			DeploymentSlot:      orcaswap.WhirlpoolDeploySlot,
			MaxInputTokenAmount: 1_000, MinOutputLamports: 1,
			MaxSlippageBPS: 100, MaxTemporaryRentLamports: 3_000_000,
		},
		InputTokenAmount: 1_000, SlippageBPS: 100,
		ReserveLamports: 50_000_000, MaxFeeLamports: 100_000,
		DailyInputTokenCap: 1_000, DailyNativeFeeCapLamports: 100_000,
		ScheduleWindowSeconds:     3_600,
		ScheduleAnchorUnix:        time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		MaxClockUncertaintyMillis: 500, MaxObservationAgeSeconds: 30,
		MinHealthyObservationSeconds: 5, MinHealthySlotAdvance: 1,
		MaxBlockHeightWindow: 200, MaxReconciliationSeconds: 180,
		PriceTrigger: &pricetrigger.Policy{
			Version: pricetrigger.Version, Feed: pricetrigger.FeedSOLUSD,
			Direction: pricetrigger.BuyAtOrBelow, ThresholdMicros: 400_000_000_000,
			MaxAgeSeconds: 120, MaxSourceSkewSeconds: 90,
			MaxDeviationBPS: 200, MaxConfidenceBPS: 200,
			PrimarySourceSHA256:   string(bytes.Repeat([]byte{'a'}, 64)),
			SecondarySourceSHA256: string(bytes.Repeat([]byte{'b'}, 64)),
		},
	}
}
