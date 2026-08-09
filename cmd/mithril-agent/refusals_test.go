package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scripted setup has no terminal, so the confirmation prompt defaults to no
// and the run ends with "the quote was not confirmed" — a true sentence the
// operator cannot act on. That happened on a real host, twice, before the
// number it wanted was worked out by hand.
//
// Interactively, "no" is an ANSWER and needs no advice. Non-interactively
// nobody was asked, and the refusal must carry the value that would let the
// run continue.
func TestANonInteractiveQuoteRefusalNamesTheFlagAndValue(t *testing.T) {
	err := quoteDeclined(false, "--confirm-min-output-amount", 103577)
	if err == nil {
		t.Fatal("a declined quote produced no error")
	}
	if !errors.Is(err, errQuoteDeclined) {
		t.Error("the refusal no longer matches the sentinel callers check")
	}
	for _, want := range []string{"--confirm-min-output-amount", "103577", "no terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// An operator who read the floor and said no does not need to be told how to
// say yes. Cluttering a deliberate refusal with a bypass flag is how somebody
// pastes it without reading.
func TestAnInteractiveRefusalStaysAPlainNo(t *testing.T) {
	err := quoteDeclined(true, "--confirm-min-output-amount", 103577)
	if !errors.Is(err, errQuoteDeclined) {
		t.Fatalf("interactive refusal lost its sentinel: %v", err)
	}
	if strings.Contains(err.Error(), "--confirm-min-output-amount") {
		t.Error("a deliberate no was answered with the flag that overrides it")
	}
}

// Refusing to write into an existing directory is correct — a half-built leg
// mixed with a previous one is unrecoverable. Saying only that it exists leaves
// the operator guessing, and the usual cause is a run that failed a later check
// and left the directory behind.
func TestTheDirectoryRefusalSaysWhatToDo(t *testing.T) {
	existing := t.TempDir()
	_, err := cleanNewSetupPath(existing)
	if err == nil {
		t.Fatal("setup accepted a directory it did not create")
	}
	for _, want := range []string{existing, "--dir", "failed run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// A fresh path inside an existing parent is still accepted.
	fresh := filepath.Join(existing, "new-leg")
	if _, err := cleanNewSetupPath(fresh); err != nil {
		t.Errorf("a fresh directory was rejected: %v", err)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Error("checking the path created it")
	}
}

// The price-mismatch check catches BOTH directions, but a single message
// described only one of them. A priced sell resuming without --buy-at-usd was
// told "This sell trades at market, so omit --buy-at-usd" — advice to remove
// the flag it needed to add, which guarantees the next attempt fails the same
// way. That happened on a real host.
func TestThePriceMismatchAdviceMatchesTheActualMismatch(t *testing.T) {
	for name, test := range map[string]struct {
		sellPrice, buyPrice string
		wants, forbids      string
	}{
		"priced sell, no buy price": {
			sellPrice: "20.700000", buyPrice: "",
			wants: "--buy-at-usd N", forbids: "omit --buy-at-usd",
		},
		"market sell, buy price given": {
			sellPrice: "", buyPrice: "19.50",
			wants: "omit --buy-at-usd", forbids: "pass the price",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := priceModeMismatch(test.sellPrice, test.buyPrice)
			if err == nil {
				t.Fatal("a mismatched pair was accepted")
			}
			if !strings.Contains(err.Error(), test.wants) {
				t.Errorf("advice does not contain %q: %v", test.wants, err)
			}
			if strings.Contains(err.Error(), test.forbids) {
				t.Errorf("advice describes the OPPOSITE mismatch (%q): %v", test.forbids, err)
			}
		})
	}

	// Matching pairs are accepted in both modes.
	for _, ok := range [][2]string{{"", ""}, {"20.700000", "19.50"}} {
		if err := priceModeMismatch(ok[0], ok[1]); err != nil {
			t.Errorf("a matching pair %v was rejected: %v", ok, err)
		}
	}
}

// The pool moves between reading a quote and confirming it, so this mismatch is
// ordinary, not exceptional. Refusing is right — the operator must confirm the
// floor actually being written. But naming only the disagreement made every
// retry a re-derivation: discover again, parse again, paste again, race again.
func TestAStaleQuoteRefusalCarriesTheCurrentNumber(t *testing.T) {
	options := swapSetupOptions{confirmedMinOut: 103577}
	err := options.confirmMinimumOutput(quoteConfirmation{MinOutput: 103412})
	if err == nil {
		t.Fatal("a stale confirmation was accepted")
	}
	for _, want := range []string{"103577", "103412", "--confirm-min-output-amount 103412"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}

	// A matching confirmation still passes untouched.
	if err := (swapSetupOptions{confirmedMinOut: 103412}).
		confirmMinimumOutput(quoteConfirmation{MinOutput: 103412}); err != nil {
		t.Errorf("a matching confirmation was rejected: %v", err)
	}
}

// The executability check tells the operator what the POOL pays and, when the
// price is unreachable, what to lower it to. Deriving that from
// --confirm-min-output-amount meant a mistyped value fabricated a price and
// then advised adopting it: passing 1 produced "the pool pays about $0.000200
// ... lower --sell-at-usd to $0.000200 or below" while the pool was really
// paying $20.92, reproducibly, on a real host.
//
// Following that advice configures an agent that sells at any price. A wrong
// number from the operator must never become a dangerous instruction back to
// them, so the price comes from the pool and only from the pool.
func TestTheExecutablePriceIsNeverDerivedFromTheOperatorsConfirmation(t *testing.T) {
	source, err := os.ReadFile("strategy_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "executablePriceFromQuote(plan.sizeLamports, *confirmedMinOut)") {
		t.Fatal("the pool price is still computed from --confirm-min-output-amount; " +
			"a mistyped value will fabricate a price and advise adopting it")
	}
	if !strings.Contains(text, "poolPriceForSize(ctx") {
		t.Error("the executability check no longer reads a live quote")
	}

	// The arithmetic itself must still be sound: a real quote yields a real
	// price, and a degenerate one must not silently become a plausible number.
	price, err := executablePriceFromQuote(5_000_000, 104_624)
	if err != nil {
		t.Fatal(err)
	}
	// 0.104624 devUSDC for 0.005 SOL is $20.9248/SOL.
	if price != 20_924_800 {
		t.Errorf("executable price = %d micro-USD, want 20924800", price)
	}
	// The value that caused the incident, shown for what it is: absurd.
	absurd, err := executablePriceFromQuote(5_000_000, 1)
	if err == nil && absurd > 1_000_000 {
		t.Errorf("a 1-unit quote produced a plausible-looking price: %d", absurd)
	}
}

// Pasting a minimum-output number back is friction with no safety when a PRICE
// TRIGGER exists: the trigger is itself a floor on the fill — the agent refuses
// to sell below it however the pool moves. The pool re-quotes every few
// seconds, so confirming it means discover, parse, paste, and race, repeatedly.
// Nobody reviews a number they are chasing.
//
// At MARKET there is no trigger, and the quoted minimum is the ONLY thing
// between the agent and any price the pool offers. Skipping it there removes
// the single protection.
func TestTheQuotedFloorMayOnlyBeSkippedWhenPricesBackItUp(t *testing.T) {
	// Priced strategy: skipping is allowed.
	if err := acceptQuotedFloorAllowed(true, false); err != nil {
		t.Errorf("a priced strategy was refused the shortcut: %v", err)
	}
	// Market strategy: refused, and the refusal must say why and what to do.
	err := acceptQuotedFloorAllowed(true, true)
	if err == nil {
		t.Fatal("a market strategy was allowed to skip its only floor")
	}
	for _, want := range []string{"only floor", "sell_at_usd", "--confirm-min-output-amount"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	// Not asking for the shortcut is always fine, in both modes.
	for _, atMarket := range []bool{true, false} {
		if err := acceptQuotedFloorAllowed(false, atMarket); err != nil {
			t.Errorf("not using the shortcut was refused (atMarket=%v): %v", atMarket, err)
		}
	}
}

// Re-typing part of the destination catches a corrupted paste of a wallet
// address — a real protection. But with no terminal the prompt returns empty
// and the refusal blamed the operator for an answer nobody asked them, on a
// scripted run of the very ceremony it protects.
//
// The two cases are opposite facts: a wrong answer is a warning about the
// address; no answer is a missing question.
func TestTheDestinationConfirmationDistinguishesWrongFromUnasked(t *testing.T) {
	source, err := os.ReadFile("sweep_setup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	// The VERDICT, not the prose about it: an earlier version of this test
	// matched its own explanatory comment and passed on the wrong text.
	verdict := strings.LastIndex(text, `errors.New(`+"\n"+
		`\t\t\t\t"the re-typed characters do not match`)
	if verdict < 0 {
		verdict = strings.LastIndex(text, "the re-typed characters do not match")
	}
	guard := strings.Index(text, `answer == "" && !stdinIsTerminal()`)
	if guard < 0 {
		t.Fatal("a scripted run is still told its answer was wrong when it was never asked")
	}
	if guard > verdict {
		t.Error("the no-terminal case is checked AFTER the mismatch verdict, so it never runs")
	}
	// The refusal must offer the way through rather than just refusing.
	if !strings.Contains(text, "setup skips the question when one is given") {
		t.Error("the refusal does not offer supplying a proof instead")
	}
}
