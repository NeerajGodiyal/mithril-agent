package jupiterquote

import "testing"

func TestQuoteDecodesPriceImpactForDiagnostics(t *testing.T) {
	request := Request{
		Taker: testTaker, InputMint: testInputMint, OutputMint: testOutputMint,
		InputAmount: 1_000_000, SlippageBPS: 50,
	}
	result, err := decodeResult([]byte(`{
          "inputMint":"`+testInputMint+`",
          "outputMint":"`+testOutputMint+`",
          "inAmount":"1000000",
          "outAmount":"150000",
          "otherAmountThreshold":"149250",
          "priceImpactPct":"0.012345",
          "swapMode":"ExactIn",
          "slippageBps":50,
          "routePlan":[{"bps":10000}]
        }`), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.PriceImpactPct != "0.012345" {
		t.Fatalf("price impact = %q", result.PriceImpactPct)
	}
}
