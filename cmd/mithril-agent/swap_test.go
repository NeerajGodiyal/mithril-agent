package main

import (
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

// Both reviewed primary adapters must be accepted, and nothing else. The
// on-chain push feed is the default so a paid subscription is not required.
func TestPriceSourcePolicyAcceptsBothReviewedPrimaries(t *testing.T) {
	coinbase := pricesource.CoinbaseIdentitySHA256()

	for name, primary := range map[string]string{
		"on-chain push": pricesource.PythPushIdentitySHA256(),
		"hermes":        pricesource.PythIdentitySHA256(),
	} {
		t.Run(name, func(t *testing.T) {
			if !priceSourcePolicyMatches(pricetrigger.Policy{
				PrimarySourceSHA256:   primary,
				SecondarySourceSHA256: coinbase,
			}) {
				t.Fatalf("%s was rejected as a primary source", name)
			}
		})
	}

	if priceSourcePolicyMatches(pricetrigger.Policy{
		PrimarySourceSHA256:   strings.Repeat("c", 64),
		SecondarySourceSHA256: coinbase,
	}) {
		t.Fatal("an unreviewed primary source was accepted")
	}
	if priceSourcePolicyMatches(pricetrigger.Policy{
		PrimarySourceSHA256:   pricesource.PythPushIdentitySHA256(),
		SecondarySourceSHA256: strings.Repeat("d", 64),
	}) {
		t.Fatal("an unreviewed secondary source was accepted")
	}
	// The two primaries are distinct trust configurations and must not collide.
	if pricesource.PythPushIdentitySHA256() == pricesource.PythIdentitySHA256() {
		t.Fatal("push and Hermes share an identity")
	}
}

// The ready report must name the reviewed adapter in operator terms and must
// never expose an endpoint or credential.
func TestDescribePriceSourceNamesReviewedAdaptersOnly(t *testing.T) {
	if got := describePriceSource(nil); !strings.Contains(got, "no price rule") {
		t.Fatalf("absent price rule = %q", got)
	}
	push := describePriceSource(&pricetrigger.Policy{
		PrimarySourceSHA256: pricesource.PythPushIdentitySHA256(),
	})
	if !strings.Contains(push, "on-chain") || !strings.Contains(push, "Coinbase") {
		t.Fatalf("push source = %q", push)
	}
	hermes := describePriceSource(&pricetrigger.Policy{
		PrimarySourceSHA256: pricesource.PythIdentitySHA256(),
	})
	if !strings.Contains(hermes, "Hermes") {
		t.Fatalf("hermes source = %q", hermes)
	}
	if got := describePriceSource(&pricetrigger.Policy{
		PrimarySourceSHA256: strings.Repeat("e", 64),
	}); got != "unrecognised" {
		t.Fatalf("unknown source = %q, want unrecognised", got)
	}
	// Naming a credential REQUIREMENT is useful; carrying an endpoint, host, or
	// credential value is not. Check for the latter only.
	for _, text := range []string{push, hermes} {
		for _, leak := range []string{"http", "://", ".com", ".app", "127.0.0.1", "bearer"} {
			if strings.Contains(strings.ToLower(text), leak) {
				t.Fatalf("description %q leaks %q", text, leak)
			}
		}
	}
}
