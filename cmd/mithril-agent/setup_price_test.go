package main

import (
	"strings"
	"testing"
)

// The wizard could not set a price at all, so it always wrote a profile that
// trades the instant it is armed — while the operator believed they had set up
// a limit order. These pin the question and, more importantly, which field the
// answer lands in.
func TestWizardAsksForAPriceAndRoutesItByDirection(t *testing.T) {
	for name, test := range map[string]struct {
		direction  string
		wantPrompt string
	}{
		"sell": {"sell", "ABOVE"},
		"buy":  {"buy", "BELOW"},
	} {
		t.Run(name, func(t *testing.T) {
			var output strings.Builder
			// directory, direction, account, mithril, config, node, quote, price
			answers := strings.Join([]string{
				"/tmp/setup", test.direction, "/tmp/a.json", "/bin/true",
				"/tmp/config.toml", "/bin/true", "/bin/true", "250",
			}, "\n") + "\n"
			choices, err := gatherSetupChoices(
				newPrompter(strings.NewReader(answers), &output, true), setupChoices{})
			if err != nil {
				t.Fatal(err)
			}
			if choices.priceMicros != 250_000_000 {
				t.Fatalf("priceMicros = %d, want 250000000 (250 USD in micros)",
					choices.priceMicros)
			}
			// The operator states a price, never a direction keyword. Asking for
			// both is how a sell ends up configured to fire when the price drops.
			if !strings.Contains(output.String(), test.wantPrompt) {
				t.Errorf("the %s prompt did not say which way it fires:\n%s",
					test.direction, output.String())
			}
		})
	}
}

// Blank must mean "no condition", not "zero", and must not invent a price.
func TestWizardTreatsABlankPriceAsNoCondition(t *testing.T) {
	var output strings.Builder
	answers := "/tmp/setup\nsell\n/tmp/a.json\n/bin/true\n/tmp/config.toml\n/bin/true\n/bin/true\n\n"
	choices, err := gatherSetupChoices(
		newPrompter(strings.NewReader(answers), &output, true), setupChoices{})
	if err != nil {
		t.Fatal(err)
	}
	if choices.priceMicros != 0 {
		t.Fatalf("a blank answer produced priceMicros = %d", choices.priceMicros)
	}
}

// A price that is not a price must stop setup rather than silently become an
// unconditional trade.
func TestWizardRefusesAPriceItCannotParse(t *testing.T) {
	var output strings.Builder
	answers := "/tmp/setup\nsell\n/tmp/a.json\n/bin/true\n/tmp/config.toml\n/bin/true\n/bin/true\ntwo hundred\n"
	if _, err := gatherSetupChoices(
		newPrompter(strings.NewReader(answers), &output, true), setupChoices{}); err == nil {
		t.Fatal("an unparseable price was accepted")
	}
}
