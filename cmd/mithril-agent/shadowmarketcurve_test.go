package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
)

type shadowMarketCurveFakeQuotes struct {
	requests []jupiterquote.Request
	from     time.Time
}

func (fake *shadowMarketCurveFakeQuotes) Quote(
	_ context.Context,
	request jupiterquote.Request,
) (jupiterquote.Result, error) {
	index := len(fake.requests)
	fake.requests = append(fake.requests, request)
	output := request.InputAmount * 5
	if index%2 == 1 {
		output = shadowMarketCurveNotionals()[index/2] * 99 / 100
	}
	minimum := (output*uint64(10_000-request.SlippageBPS) + 9_999) / 10_000
	return jupiterquote.Result{
		InputAmount: request.InputAmount, EstimatedOutput: output, MinimumOutput: minimum,
		PriceImpactPct: "0.012345",
		ReceivedAt:     fake.from.Add(time.Duration(index*2+1) * time.Second),
		ResponseSHA256: strings.Repeat(string("abcdef12"[index]), 64),
	}, nil
}

func TestShadowMarketCurveCollectsSerialCanonicalDiagnostic(t *testing.T) {
	candidate, ok := marketadmission.Lookup(marketadmission.MarketJTOUSDC)
	if !ok {
		t.Fatal("JTO market is unavailable")
	}
	from := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	fake := &shadowMarketCurveFakeQuotes{from: from}
	clockCalls := 0
	now := func() time.Time {
		result := from.Add(time.Duration(clockCalls) * time.Second)
		clockCalls++
		return result
	}
	artifact, err := collectShadowMarketCurve(
		t.Context(), candidate, "11111111111111111111111111111111", fake, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	if artifact.Status != shadowMarketCurveStatus || artifact.OperationallyQualified ||
		len(artifact.Points) != 4 || len(fake.requests) != 8 {
		t.Fatalf("curve artifact = %+v; requests = %d", artifact, len(fake.requests))
	}
	for index, notional := range shadowMarketCurveNotionals() {
		buy, sell := fake.requests[index*2], fake.requests[index*2+1]
		if buy.InputAmount != notional || buy.InputMint != candidate.QuoteMint ||
			buy.OutputMint != candidate.BaseMint || sell.InputMint != candidate.BaseMint ||
			sell.OutputMint != candidate.QuoteMint ||
			sell.InputAmount != artifact.Points[index].Buy.EstimatedOutput ||
			artifact.Points[index].RoundTripRouteCostBPS != 100 {
			t.Fatalf("curve point %d is not a serial round trip: %+v", index, artifact.Points[index])
		}
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"operationally_qualified":false`, `"price_impact_pct":"0.012345"`,
		`"latency_millis":1`, `"response_sha256":`, `"content_sha256":`,
	} {
		if !bytes.Contains(encoded, []byte(field)) {
			t.Fatalf("curve JSON omits %s: %s", field, encoded)
		}
	}
	tampered := artifact
	tampered.Points = append([]shadowMarketCurvePoint(nil), artifact.Points...)
	tampered.Points[0].RoundTripRouteCostBPS++
	if tampered.Validate() == nil {
		t.Fatal("tampered curve artifact was accepted")
	}
}

func TestShadowMarketCurveCommandRequiresItsWatchOnlyInputs(t *testing.T) {
	err := runShadowMarket(context.Background(), []string{
		"curve", "--market", marketadmission.MarketWIFUSDC,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires --market and --observe") {
		t.Fatalf("curve argument error = %v", err)
	}
}

func TestShadowMarketCurveRejectsMissingPriceImpact(t *testing.T) {
	if shadowMarketCurvePriceImpactValid("") || shadowMarketCurvePriceImpactValid("1e-3") ||
		!shadowMarketCurvePriceImpactValid("-0.000001") {
		t.Fatal("price impact syntax validation is incorrect")
	}
}
