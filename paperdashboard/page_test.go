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
		`filter(market=>!market.optional)`,
		`filter(market=>market.optional)`,
		`Completed experiment`,
		`Completed perps research is not included.`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("app JS omits %q", want)
		}
	}
}

func TestPerpsTrainingAttemptsStayCompactAndUnapproved(t *testing.T) {
	for _, want := range []string{
		`qualification_attempts||[]).slice(0,3)`,
		`Best completed training attempts`,
		`Not selected`,
		`Training candidate`,
		`attempt-meter`,
		`Boolean(m.qualification_tracked)`,
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

func TestActivityDoesNotMislabelExperimentOutcomeAsProfitOrLoss(t *testing.T) {
	for _, want := range []string{
		`const experiment=/STRATEGY CHECK/i.test(lines[0])`,
		`experiment?'Experiment result:':'This market gain/loss:'`,
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("activity display omits %q", want)
		}
	}
}
