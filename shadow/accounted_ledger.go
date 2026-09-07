package shadow

import "errors"

// AccountedOutcome contains exact settled amounts, not modeled quote evidence.
// The caller must verify their finalized provenance and apply each outcome once.
type AccountedOutcome struct {
	AccountingSHA256       string `json:"accounting_sha256"`
	TokenMint              string `json:"token_mint"`
	Success                bool   `json:"success"`
	Sell                   bool   `json:"sell"`
	SpentUnits             uint64 `json:"spent_units,string"`
	ReceivedUnits          uint64 `json:"received_units,string"`
	FeeLamports            uint64 `json:"fee_lamports,string"`
	PreBaseUnits           uint64 `json:"pre_base_units,string"`
	PreQuoteUnits          uint64 `json:"pre_quote_units,string"`
	PostBaseUnits          uint64 `json:"post_base_units,string"`
	PostQuoteUnits         uint64 `json:"post_quote_units,string"`
	ReclaimedInputLamports uint64 `json:"reclaimed_input_lamports,string"`
	OutputAccountRent      uint64 `json:"output_account_rent,string"`
}

// ApplyAccounted books a verified SOL/USDC outcome against the existing books.
// It neither verifies chain evidence nor deduplicates outcomes or authorizes a
// trade. Existing opening basis and observed drawdown history are retained.
// Rent effects are unsupported: the payer-only opening does not establish the
// basis of capital in a pre-existing wrapped-SOL account.
func (l Ledger) ApplyAccounted(outcome AccountedOutcome, markPriceMicros uint64) (Ledger, error) {
	if err := l.Policy.Validate(); err != nil {
		return Ledger{}, err
	}
	if outcome.TokenMint != mainnetUSDCMint || l.Policy.Cluster != Mainnet || l.Policy.Market != "" && l.Policy.Market != MarketSOLUSDC ||
		l.baseDecimals() != 9 || l.quoteDecimals() != 6 || usesSeparateNativePrice(l.Policy) ||
		l.Policy.StartingFeeReserveLamports != 0 || l.Policy.OneTimeSetupRentLamports != 0 ||
		l.FeeReserveLamports != 0 || l.FeeReserveCostBasisMicros != 0 ||
		l.LockedRentLamports != 0 || l.LockedRentCostBasisMicros != 0 || l.NativeFeePriceMicros != 0 {
		return Ledger{}, errors.New("accounted ledger requires unsplit SOL/USDC balances without modeled rent")
	}
	if outcome.ReclaimedInputLamports != 0 || outcome.OutputAccountRent != 0 {
		return Ledger{}, errors.New("accounted ledger cannot establish rent capital basis")
	}
	if outcome.PreBaseUnits != l.BaseUnits || outcome.PreQuoteUnits != l.QuoteUnits || outcome.FeeLamports == 0 ||
		outcome.Success && (outcome.SpentUnits == 0 || outcome.ReceivedUnits == 0) ||
		!outcome.Success && (outcome.SpentUnits != 0 || outcome.ReceivedUnits != 0) {
		return Ledger{}, errors.New("accounted outcome does not match ledger balances or terminal amounts")
	}
	next, err := l.applyAmounts(outcome.Success, outcome.Sell, outcome.SpentUnits, outcome.ReceivedUnits,
		outcome.FeeLamports, 0, markPriceMicros)
	if err != nil {
		return Ledger{}, err
	}
	if next.BaseUnits != outcome.PostBaseUnits || next.QuoteUnits != outcome.PostQuoteUnits {
		return Ledger{}, errors.New("accounted outcome post balances do not match exact amounts")
	}
	return next, nil
}
