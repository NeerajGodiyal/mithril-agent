package paperdashboard

import (
	"strings"
	"testing"
)

func TestDashboardSeparatesLiveSpotAccountFromPerpsResearch(t *testing.T) {
	for _, want := range []string{
		`aria-label="Live spot markets"`,
		`id="perps-research-title">Perps experiments`,
		`Completed simulations stay separate from the live SOL and JUP spot account.`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("page omits %q", want)
		}
	}
	for _, want := range []string{
		`metric('Live spot account'`,
		`filter(market=>!isPerps(market)&&(!market.optional||market.available))`,
		`filter(isPerps)`,
		`market.optional&&!isPerps(market)`,
		`!m.completed&&m.available&&m.ready&&m.fresh`,
		`Completed paper run`,
		`view.unavailable||!m.ready||m.completed`,
		`Completed experiment`,
		`Completed perps research is not included.`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("app JS omits %q", want)
		}
	}
}

func TestDashboardMakesCurrentAutonomousDecisionAndPaperBoundaryUnavoidable(t *testing.T) {
	for _, want := range []string{
		`id="agent-now"`,
		`Paper only · No real orders`,
		`function renderAgentNow()`,
		`const view=strategyView(m)`,
		`P&amp;L this run`,
		`Last recorded P&amp;L`,
		`renderAgentNow();renderMetrics()`,
	} {
		if !strings.Contains(indexHTML+appJS, want) {
			t.Errorf("current agent summary omits %q", want)
		}
	}
	for _, want := range []string{
		`.agent-now-grid { display: grid; grid-template-columns: repeat(2`,
		`.agent-now-grid { grid-template-columns: 1fr; }`,
		`.checked #checked { display: block;`,
	} {
		if !strings.Contains(dashboardCSS, want) {
			t.Errorf("responsive current agent design omits %q", want)
		}
	}
}

func TestDashboardDoesNotPresentHermesAsTradingAuthority(t *testing.T) {
	for _, want := range []string{
		`Hermes advised no change`,
		`Hermes risk review (advisory)`,
		`Deterministic replay gates alone decide whether any paper plan may change.`,
		`Individual source publication freshness is unavailable in this bounded view; packet age is not source age.`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("Hermes boundary copy omits %q", want)
		}
	}
	if strings.Contains(appJS, `?'Vetoed'`) {
		t.Fatal("dashboard still presents Hermes as a veto authority")
	}
}

func TestDashboardExplainsWhyCandidateMarketTrainingFoundNoPlan(t *testing.T) {
	for _, want := range []string{
		`check.training_rejections||{}`,
		`Tested '+tested+' paper plans. Most often,`,
		`A plan can fail more than one check.`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("candidate-market training explanation omits %q", want)
		}
	}
}

func TestDashboardExplainsAutomaticPerpsPlanSelectionTruthfully(t *testing.T) {
	for _, want := range []string{
		`must beat the current paper plan`,
		`untouched normal-cost and doubled-fee replay`,
		`next bounded paper test`,
		`never enables real execution`,
		`Selected paper plan proposed by deterministic search`,
		`Built-in fixed paper plan`,
		`Later verified paper run: Gain`,
		`The completed run plan shown here is unchanged; only a separate selection can change`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("automatic perps selection copy omits %q", want)
		}
	}
	if strings.Contains(appJS, `it changes no plan automatically`) {
		t.Fatal("dashboard denies the automatic next-run perps selection")
	}
	if strings.Contains(appJS, `Hermes selected`) || strings.Contains(appJS, `Hermes proposed`) {
		t.Fatal("dashboard attributes deterministic perps selection to Hermes")
	}
}

func TestPerpsTrainingAttemptsStayCompactAndUnapproved(t *testing.T) {
	for _, want := range []string{
		`qualification_attempts||[]).slice(0,3)`,
		`Best completed training attempts`,
		`Not selected`,
		`Training candidate`,
		`attempt-meter`,
		`completed?.qualification_attempts`,
		`trade fees · `,
		`Funding: `,
		`fundingAdjustment`,
		`largest drop`,
		`forced close`,
		`Strongest completed attempt`,
		`value==='experimental'?'Aggressive'`,
		`betterTrainingAttempt`,
		`left.max_drawdown_micros`,
		`left.liquidations`,
		`trainingRiskRank(left.risk_profile)`,
		`String(left.strategy||'')<String(right.strategy||'')`,
		`profit after costs, complete fills, recorded fees, and no forced close`,
		`Compare '+others.length+' other risk level`,
		`Perps test terms`,
		`perpsRecordingInProgress`,
		`m.latest_completed`,
		`'Recording in progress'`,
		`'Completed result saved'`,
		`'Current status is delayed'`,
		`'Latest completed run · '`,
		`View completed result`,
		`aria-label="View '+safe(m.name)+' completed result"`,
		`'Completed perps experiment flow'`,
		`hasCompleted?'<small>'+safe(perpsPlanSource(completed))+'</small>'`,
		`hasCompleted&&!m.ready?'No current recording'`,
		`saved?'Completed accounting and boundaries':'Current accounting and boundaries'`,
		`saved?'Final paper value':'Paper value now'`,
		`saved?'Completed-run result':'Result this run'`,
		`saved?'Final open result':'Open result'`,
		`data-perps-research-market`,
		`openAttemptMarkets`,
		`focusedAttemptMarket`,
		`market checks saved`,
		`Waiting for first current market check`,
		`recording now`,
		`!m.available?'Unavailable'`,
		`'Needs attention'`,
		`'Experiment incomplete'`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("app JS omits %q", want)
		}
	}
	for _, forbidden := range []string{"Selected paper strategy", "Approved strategy", "+safe(paperValue(attempt.fees_micros,m.value_unit))+' costs", "Running paper experiment", "Collecting market checks"} {
		if strings.Contains(appJS, forbidden) {
			t.Errorf("app JS presents replay evidence as %q", forbidden)
		}
	}
	for _, want := range []string{".perps-research-grid", ".attempt-card", ".attempt-meter", ".attempt-more", ".strategy-market-group"} {
		if !strings.Contains(dashboardCSS, want) {
			t.Errorf("design CSS omits %q", want)
		}
	}
}

func TestDashboardExplainsCurrentPerpsDecisionWithoutImplyingAnOrder(t *testing.T) {
	for _, want := range []string{
		`recording?perpsCurrentEvidence(m):''`,
		`Latest sampled mark`,
		`Latest plan reading`,
		`Action level`,
		`Research checkpoint`,
		`completed one-minute market snapshots`,
		`decisionReason(m.decision_reason)`,
		`No real order has been sent.`,
		`Not a resting exchange order`,
		`latest sampled mark values an open paper position`,
		`breakout_range`,
		`regime_breakout_high`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("current perps explanation omits %q", want)
		}
	}
	for _, want := range []string{".perps-current-evidence", ".perps-progress-head", ".perps-current-reason"} {
		if !strings.Contains(dashboardCSS, want) {
			t.Errorf("current perps design omits %q", want)
		}
	}
}

func TestActivityKeepsProducerFactsExactAndDerivesOnlyEventStatus(t *testing.T) {
	for _, want := range []string{
		`const lines=String(item.message||'').split('\n')`,
		`const status=activityStatus(item.kind)`,
		`order_refused:{label:'Order status',value:'Refused'`,
		`order_opened:{label:'Order status',value:'Placed'`,
		`order_filled:{label:'Order status',value:'Filled'`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("activity display omits %q", want)
		}
	}
	for _, forbidden := range []string{`compactActivityDollars`, `readableActivityResult`, `Plan result at that update`} {
		if strings.Contains(appJS, forbidden) {
			t.Errorf("activity display still rewrites producer facts through %q", forbidden)
		}
	}
}

func TestMarketResearchWindowDoesNotMixBigIntWithNumberMath(t *testing.T) {
	if !strings.Contains(appJS, `Math.max(1,Number(m.window_hours||0))`) {
		t.Fatal("market research window is not converted to Number before Math.max")
	}
	if strings.Contains(appJS, `Math.max(1,integer(m.window_hours))`) {
		t.Fatal("market research window still passes a BigInt to Math.max")
	}
}
