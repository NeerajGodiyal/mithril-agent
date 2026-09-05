package policyauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/proposalcheck"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/signer"
)

const paperRequestClaimEvent = "paper.unsigned-request-claim-v1"

type paperRequestClaim struct {
	PaperIntentSHA256 string `json:"paper_intent_sha256"`
	PolicySHA256      string `json:"policy_sha256"`
	RequestSHA256     string `json:"request_sha256"`
	MaxDecisionAgeNS  int64  `json:"max_decision_age_ns,string"`
}

// ClaimPaperRequest binds the first frozen paper decision to one exact unsigned,
// ungranted request in a dedicated, caller-protected journal. It rechecks chain
// evidence but neither authorizes nor signs nor submits. This is not funded
// readiness: supplied paper budgets are not native-wallet reserve evidence, and
// rechecking a transaction does not establish the original quote's wall-clock age.
// maxDecisionAge bounds recorded decision and quote receipt recency only; it is
// not a provider quote TTL or evidence of a newly acquired executable quote.
//
// The same journal must be reused across restarts and schedule windows. Its one
// pending claim has no release/reset operation: later actions require a separate
// finalized-inventory contract, never simulated proceeds. Exact repeats still
// require a successful current recheck and an unchanged unsigned request.
func ClaimPaperRequest(
	ctx context.Context,
	path string,
	policy Policy,
	paperPolicy shadow.Policy,
	ticks []shadow.Tick,
	bounds proposalcheck.PaperIntentBounds,
	candidate proposalcheck.Candidate,
	scheduleWindowStartUnix int64,
	now time.Time,
	maxDecisionAge time.Duration,
	evidence proposalcheck.Evidence,
	primary, secondary proposalcheck.FinalizedSlotReader,
) (request signer.Request, err error) {
	if err := policy.Validate(); err != nil {
		return signer.Request{}, err
	}
	if policy.TransactionPolicy.Jupiter == nil {
		return signer.Request{}, errors.New("paper request requires a Jupiter policy")
	}
	if maxDecisionAge <= 0 || now.IsZero() || len(ticks) == 0 || ticks[len(ticks)-1].DecisionQuote == nil {
		return signer.Request{}, errors.New("paper request requires recorded decision recency")
	}
	last := ticks[len(ticks)-1]
	for _, at := range []time.Time{last.At, last.DecisionQuote.ReceivedAt} {
		if at.IsZero() || at.After(now) || at.Before(now.Add(-maxDecisionAge)) {
			return signer.Request{}, errors.New("paper request recorded decision or quote receipt is outside the recency bound")
		}
	}
	store, err := journal.Open(path)
	if err != nil {
		return signer.Request{}, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			request = signer.Request{}
			err = errors.Join(err, closeErr)
		}
	}()
	intent, err := proposalcheck.CheckPaperIntent(paperPolicy, ticks,
		*policy.TransactionPolicy.Jupiter, candidate, bounds)
	if err != nil {
		return signer.Request{}, err
	}
	request, err = PrepareJupiterRequest(ctx, policy, candidate,
		scheduleWindowStartUnix, now, evidence, primary, secondary)
	if err != nil {
		return signer.Request{}, err
	}
	if err := claimPaperRequest(store, policy, intent, request, now, maxDecisionAge); err != nil {
		return signer.Request{}, err
	}
	return request, nil
}

func claimPaperRequest(store *journal.Store, policy Policy, intent proposalcheck.PaperIntent, request signer.Request, now time.Time, maxDecisionAge time.Duration) error {
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return err
	}
	policyHash := sha256.Sum256(append([]byte(paperRequestClaimEvent+"/policy\x00"), policyBytes...))
	requestHash := sha256.Sum256(append([]byte(paperRequestClaimEvent+"/request\x00"), requestBytes...))
	claim := paperRequestClaim{PaperIntentSHA256: intent.SHA256, MaxDecisionAgeNS: int64(maxDecisionAge),
		PolicySHA256: hex.EncodeToString(policyHash[:]), RequestSHA256: hex.EncodeToString(requestHash[:])}
	payload, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	if records := store.Records(); len(records) != 0 {
		if len(records) != 1 || records[0].Type != paperRequestClaimEvent ||
			records[0].ActionID != request.ActionID || !bytes.Equal(records[0].Payload, payload) {
			return errors.New("paper request journal already contains a different or pending claim")
		}
		return nil
	}
	_, err = store.Append(now.UTC(), paperRequestClaimEvent, request.ActionID, claim)
	return err
}
