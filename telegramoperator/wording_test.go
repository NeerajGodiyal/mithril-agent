package telegramoperator

import (
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
)

// Every state the engine can report must have operator language. An unmapped
// state would surface a bare internal token to a non-specialist.
func TestEveryEngineDecisionHasOperatorWording(t *testing.T) {
	// These are the decisions swaprun can put in a status snapshot.
	for _, decision := range []string{
		"waiting", "ready", "complete", "stopped", "canceled",
		"degraded", "failed", "halted",
	} {
		text := describeDecision(decision)
		if text == decision {
			t.Errorf("decision %q has no operator wording", decision)
		}
		if strings.ContainsAny(text, "_") {
			t.Errorf("wording for %q still looks like an identifier: %q", decision, text)
		}
	}
	for _, verdict := range []string{
		"pending", "finalized", "failed", "unresolved", "diverged",
	} {
		if describeVerdict(verdict) == verdict {
			t.Errorf("verdict %q has no operator wording", verdict)
		}
	}
	for _, reason := range []string{
		"operation_timeout", "quote_unavailable", "price_below_floor",
		"before_schedule_anchor", "blockhash_expired", "signer_refused",
		"node_unavailable", "observation_not_ready", "clock_unusable",
		"control_state_unavailable", "operation_failed",
	} {
		if text := describeReason(reason); text == reason || strings.Contains(text, "_") {
			t.Errorf("reason %q has no operator wording: %q", reason, text)
		}
	}
}

// An unknown state must still be visible rather than silently blank, because
// hiding a state we did not anticipate is worse than showing a raw token.
func TestUnknownStatesRemainVisible(t *testing.T) {
	if got := describeDecision("some_future_state"); got != "some_future_state" {
		t.Errorf("unknown decision rendered as %q, want the raw token", got)
	}
	if got := describeDecision(""); got != "unknown" {
		t.Errorf("empty decision rendered as %q, want \"unknown\"", got)
	}
	if got := describeVerdict("some_future_verdict"); got != "some_future_verdict" {
		t.Errorf("unknown verdict rendered as %q, want the raw token", got)
	}
	if got := describeReason("some_future_reason"); got != "some_future_reason" {
		t.Errorf("unknown reason rendered as %q, want the raw token", got)
	}
}

// The states an operator must act on have to read as needing action, and the
// safe ones must not. This is the whole point of the mapping.
func TestActionableStatesReadAsActionable(t *testing.T) {
	for _, decision := range []string{"failed", "halted"} {
		if !strings.Contains(strings.ToLower(describeDecision(decision)), "review") {
			t.Errorf("%q does not tell the operator it needs review: %q", decision, describeDecision(decision))
		}
	}
	if strings.Contains(strings.ToLower(describeDecision("complete")), "review") {
		t.Error("a completed action should not read as needing review")
	}
	// An unresolved send is the one an operator must never blindly retry.
	if !strings.Contains(strings.ToLower(describeVerdict("unresolved")), "do not retry") {
		t.Errorf("unresolved outcome omits the do-not-retry instruction: %q", describeVerdict("unresolved"))
	}
}

// The three things this agent does read very differently to the person holding
// the phone, and every one of them used to be called a "trade". A buy reported
// "Sold 0.10 devUSDC" — backwards — and a transfer to the operator's own wallet
// reported "Trade complete", which it was not.
func TestBuySellAndTransferEachReadCorrectly(t *testing.T) {
	for _, c := range []struct {
		name       string
		result     operatorstatus.Result
		wantHead   string
		wantSpent  string
		mustNotSay []string
	}{
		{
			name: "sell",
			result: operatorstatus.Result{
				Decision: "complete", Verdict: "finalized",
				InputAsset: "SOL", OutputAsset: "devUSDC",
			},
			wantHead: "Sold SOL — confirmed on-chain", wantSpent: "Sold",
			mustNotSay: []string{"Bought", "your wallet"},
		},
		{
			name: "buy",
			result: operatorstatus.Result{
				Decision: "complete", Verdict: "finalized",
				InputAsset: "devUSDC", OutputAsset: "SOL",
			},
			wantHead: "Bought SOL — confirmed on-chain", wantSpent: "Spent",
			mustNotSay: []string{"Sold", "your wallet"},
		},
		{
			name: "transfer to your wallet",
			result: operatorstatus.Result{
				Decision: "complete", Verdict: "finalized",
				AmountLamports: 1_000_000,
			},
			wantHead: "Sent to your wallet — confirmed on-chain", wantSpent: "Sent",
			mustNotSay: []string{"Sold", "Bought", "Trade complete"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			kind := kindOf(c.result)
			head := tradeHeadline(kind, c.result.Decision, c.result.Verdict, c.result.Submitted)
			if head != c.wantHead {
				t.Errorf("headline = %q, want %q", head, c.wantHead)
			}
			if got := kind.spentLabel(); got != c.wantSpent {
				t.Errorf("spent label = %q, want %q", got, c.wantSpent)
			}
			for _, forbidden := range c.mustNotSay {
				if strings.Contains(head, forbidden) {
					t.Errorf("headline %q says %q, which is the wrong action", head, forbidden)
				}
			}
		})
	}

	// A failure has to name the right action too, or an operator chasing a
	// failed buy goes looking through their sales.
	buy := operatorstatus.Result{Decision: "canceled", InputAsset: "devUSDC", OutputAsset: "SOL"}
	if got := tradeHeadline(kindOf(buy), buy.Decision, "", false); !strings.HasPrefix(got, "Buy canceled") {
		t.Errorf("canceled buy = %q, want it to name the buy", got)
	}
	sweep := operatorstatus.Result{Decision: "failed", AmountLamports: 5}
	if got := tradeHeadline(kindOf(sweep), sweep.Decision, "", false); !strings.HasPrefix(got, "Transfer failed") {
		t.Errorf("failed transfer = %q, want it to name the transfer", got)
	}
}
