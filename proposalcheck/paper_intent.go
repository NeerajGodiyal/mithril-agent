package proposalcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

// PaperIntentBounds is independently supplied review input, not wallet evidence
// or spending authority. Amounts are base units, never USD or simulated profits;
// JSON uses decimal strings so consumers cannot round uint64 values.
type PaperIntentBounds struct {
	PolicySHA256         string `json:"policy_sha256"`
	EvidenceSHA256       string `json:"evidence_sha256"`
	MaxInputAmount       uint64 `json:"max_input_amount,string"`
	NativeBudgetLamports uint64 `json:"native_budget_lamports,string"`
	ReserveLamports      uint64 `json:"reserve_lamports,string"`
}

// PaperIntent is a content-bound, unsigned review receipt. It cannot be sent,
// signed or used as a control grant. A repeated input yields the same identity;
// an execution owner must durably claim it before any future execution.
type PaperIntent struct {
	SHA256          string    `json:"sha256"`
	PolicySHA256    string    `json:"policy_sha256"`
	EvidenceSHA256  string    `json:"evidence_sha256"`
	CandidateSHA256 string    `json:"candidate_sha256"`
	Market          string    `json:"market"`
	Sell            bool      `json:"sell"`
	InputMint       string    `json:"input_mint"`
	OutputMint      string    `json:"output_mint"`
	InputAmount     uint64    `json:"input_amount,string"`
	At              time.Time `json:"at"`
}

// PaperEvidenceSHA256 binds the exact ordered replay input for independent
// review. A hash is provenance binding, not proof that providers were honest.
func PaperEvidenceSHA256(ticks []shadow.Tick) (string, error) {
	return paperIntentHash(ticks)
}

// CheckPaperIntent checks only the first actionable signal in a frozen paper
// history against a separately protected unsigned Jupiter proposal. Later legs
// require real finalized inventory; simulated proceeds must not size them.
// This function performs no IO, authorization, freshness check or reservation.
func CheckPaperIntent(policy shadow.Policy, ticks []shadow.Tick, route jupiterswap.Policy, candidate Candidate, bounds PaperIntentBounds) (PaperIntent, error) {
	fingerprint, err := policy.Fingerprint()
	if err != nil || fingerprint != bounds.PolicySHA256 || policy.Cluster != shadow.Mainnet ||
		policy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional {
		return PaperIntent{}, errors.New("paper intent requires the frozen Mainnet policy")
	}
	evidence, err := PaperEvidenceSHA256(ticks)
	if err != nil || evidence != bounds.EvidenceSHA256 || len(ticks) == 0 {
		return PaperIntent{}, errors.New("paper intent evidence differs from reviewed history")
	}
	if _, err := shadow.Replay(policy, ticks); err != nil {
		return PaperIntent{}, err
	}
	last := ticks[len(ticks)-1]
	if last.Event != shadow.EventSignal || !last.Triggered || last.Deferred || last.DecisionQuote == nil {
		return PaperIntent{}, errors.New("paper intent requires a new actionable signal")
	}
	for _, tick := range ticks[:len(ticks)-1] {
		if tick.Event == shadow.EventSignal || tick.Fill != nil || tick.DecisionMissed {
			return PaperIntent{}, errors.New("paper intent cannot reuse pending actions or simulated proceeds")
		}
	}
	market := policy.Market
	if market == "" {
		market = shadow.MarketSOLUSDC
	}
	want := shadow.MainnetMarketQuoteRoute(market, policy.IsSell())
	if want.Provider != shadow.QuoteJupiter || policy.QuoteRoute != want ||
		route.Owner != policy.Observe || route.InputMint != want.InputMint || route.OutputMint != want.OutputMint ||
		candidate.Request.InputAmount != last.DecisionQuote.InputAmount ||
		candidate.Quote.EstimatedOutput < last.DecisionQuote.EstimatedOutput ||
		candidate.Quote.MinimumOutput < last.DecisionQuote.MinimumOutput ||
		candidate.Request.SlippageBPS > policy.SlippageBPS || route.MaxFeeLamports > policy.FeeLamports {
		return PaperIntent{}, errors.New("paper intent route or amount differs from the decision")
	}
	if _, _, err := ValidateCandidateMaterial(route, candidate); err != nil {
		return PaperIntent{}, err
	}
	amount := candidate.Request.InputAmount
	if amount == 0 || amount > bounds.MaxInputAmount || bounds.ReserveLamports == 0 || bounds.NativeBudgetLamports < bounds.ReserveLamports {
		return PaperIntent{}, errors.New("paper intent input or reserve exceeds budget")
	}
	remaining := bounds.NativeBudgetLamports - bounds.ReserveLamports
	// Reserve both possible token-account rents conservatively. Sequential
	// subtraction avoids overflow even for malicious maximum uint64 limits.
	for _, debit := range []uint64{route.MaxFeeLamports, route.MaxTokenAccountRentLamports, route.MaxTokenAccountRentLamports} {
		if debit > remaining {
			return PaperIntent{}, errors.New("paper intent native costs exceed budget")
		}
		remaining -= debit
	}
	if route.NativeInput() && amount > remaining {
		return PaperIntent{}, errors.New("paper intent native input exceeds budget")
	}
	encoded, err := EncodeCandidate(candidate)
	if err != nil {
		return PaperIntent{}, err
	}
	digest := sha256.Sum256(encoded)
	result := PaperIntent{PolicySHA256: fingerprint, EvidenceSHA256: evidence,
		CandidateSHA256: hex.EncodeToString(digest[:]), Market: market, Sell: policy.IsSell(),
		InputMint: want.InputMint, OutputMint: want.OutputMint, InputAmount: amount, At: last.At.UTC()}
	result.SHA256, err = paperIntentHash(struct {
		Intent PaperIntent
		Bounds PaperIntentBounds
	}{result, bounds})
	return result, err
}

func paperIntentHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("mithril-agent/paper-intent-v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}
