package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file this tool writes must be readable by the tool that wrote it.
// Decoding is strict, so a template carrying its own comments only works if
// the field exists — otherwise `strategy init` produces a file that
// `setup strategy --from` immediately rejects.
func TestTheWrittenTemplateCanBeReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.json")
	if err := runStrategyInit([]string{path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	file, err := readStrategyFile(path)
	if err != nil {
		t.Fatalf("the template this tool wrote could not be read back: %v", err)
	}
	if file.SizeSOL == "" || !file.Sweep.Enabled {
		t.Fatalf("template lost its settings: %+v", file)
	}
	if file.SellAtUSD != "" || file.BuyAtUSD != "" {
		t.Fatalf("template guessed live market prices: %+v", file)
	}
	if len(file.Comment) == 0 {
		t.Error("the template no longer explains itself")
	}
}

// A strategy file holds a destination proof somebody signed. Silently replacing
// it would throw away a signing session.
func TestInitRefusesToOverwriteAnExistingStrategyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.json")
	if err := runStrategyInit([]string{path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runStrategyInit([]string{path}, &bytes.Buffer{}); err == nil {
		t.Fatal("init overwrote an existing strategy file")
	}
}

// Every refusal names the field, so an operator fixes one line rather than
// discovering the problem inside a generated profile.
func TestStrategyFileRefusalsNameTheField(t *testing.T) {
	for name, test := range map[string]struct {
		file strategyFile
		want string
	}{
		"no size":     {strategyFile{Sweep: strategyFileSweep{Enabled: true, To: "x"}}, "size_sol"},
		"half a pair": {strategyFile{SizeSOL: "0.05", SellAtUSD: "250", Sweep: strategyFileSweep{Enabled: true, To: "x"}}, "buy_at_usd"},
		"sweep off":   {strategyFile{SizeSOL: "0.05"}, "sweep.enabled"},
		"sweep no to": {strategyFile{SizeSOL: "0.05", Sweep: strategyFileSweep{Enabled: true}}, "sweep.to"},
		"part proof":  {strategyFile{SizeSOL: "0.05", Sweep: strategyFileSweep{Enabled: true, To: "x", ProofNonce: "n"}}, "proof_signature"},
		"bad window":  {strategyFile{SizeSOL: "0.05", ScheduleWindow: "soon", Sweep: strategyFileSweep{Enabled: true, To: "x"}}, "schedule_window"},
	} {
		t.Run(name, func(t *testing.T) {
			err := test.file.validate()
			if err == nil {
				t.Fatal("an unusable strategy file was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %q", err, test.want)
			}
		})
	}
	// Neither price is a deliberate choice, not an error.
	ok := strategyFile{SizeSOL: "0.05", Sweep: strategyFileSweep{Enabled: true, To: "x"}}
	if err := ok.validate(); err != nil {
		t.Fatalf("a market-order strategy file was refused: %v", err)
	}
}

// The token must never be in the file: a secret here reaches backups,
// screenshots and git.
func TestTheStrategyFileHasNowhereToPutASecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.json")
	if err := runStrategyInit([]string{path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "secret", "key", "keypair"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Errorf("the template invites a secret into the file: %q", forbidden)
		}
	}
}

// withEditor replaces the editor launch with a function that edits the file
// directly, so the round trip is testable without a terminal.
func withEditor(t *testing.T, edit func(path string) error) {
	t.Helper()
	previous := runEditor
	runEditor = func(_, path string) error { return edit(path) }
	t.Cleanup(func() { runEditor = previous })
}

// The value of `strategy edit` is not the typing — it is refusing to leave an
// unusable file behind. A saved file that will be rejected must be reported
// before the operator walks away expecting it to run.
func TestEditRefusesToLeaveAnUnusableFileUnreported(t *testing.T) {
	t.Setenv("EDITOR", "/bin/cat")
	path := filepath.Join(t.TempDir(), "strategy.json")
	if err := runStrategyInit([]string{path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	// An edit that breaks a required field.
	withEditor(t, func(p string) error {
		return os.WriteFile(p, []byte(`{"size_sol":"","sweep":{"enabled":false}}`), 0o600)
	})
	err := runStrategyEdit([]string{path, "--raw"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an invalid saved file was reported as fine")
	}
	if !strings.Contains(err.Error(), "size_sol") {
		t.Errorf("the refusal does not name the broken field: %v", err)
	}
}

// A good edit reports back what the strategy now says, so the operator can see
// their change took effect without opening the file again.
func TestEditSummarisesWhatTheStrategyNowDoes(t *testing.T) {
	t.Setenv("EDITOR", "/bin/cat")
	path := filepath.Join(t.TempDir(), "strategy.json")
	if err := runStrategyInit([]string{path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	withEditor(t, func(p string) error {
		return os.WriteFile(p, []byte(
			`{"size_sol":"0.25","sweep":{"enabled":true,"to":"SOMEWALLET"}}`), 0o600)
	})
	var output bytes.Buffer
	if err := runStrategyEdit([]string{path, "--raw"}, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"0.25", "whatever the pool gives", "SOMEWALLET", "Nothing is armed"} {
		if !strings.Contains(text, expected) {
			t.Errorf("the summary omitted %q:\n%s", expected, text)
		}
	}
}

// The editor is the program that opens a file holding a signed destination
// proof. A bare name must be resolved on PATH, never picked up from the
// working directory.
func TestTheEditorMustBeResolvableAndNamed(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	if _, err := operatorEditor(); err == nil {
		t.Fatal("no editor was configured and none was demanded")
	}
	t.Setenv("EDITOR", "definitely-not-a-real-editor-xyz")
	if _, err := operatorEditor(); err == nil {
		t.Fatal("an editor that does not exist was accepted")
	}
	// VISUAL wins over EDITOR, matching every other tool.
	t.Setenv("VISUAL", "/bin/cat")
	t.Setenv("EDITOR", "/bin/echo")
	got, err := operatorEditor()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/bin/cat" {
		t.Errorf("editor = %q, want VISUAL to win", got)
	}
}

// The person who most needs this file is the least likely to edit JSON safely.
// The guided walkthrough must produce a valid file from plain answers, never
// showing them a brace.
func TestTheGuidedEditorBuildsAValidFileFromPlainAnswers(t *testing.T) {
	var output strings.Builder
	// size, price-rules? (n = market), window, trades/day, wallet,
	// keep (blank = just what the trades need), Telegram, balance alerts.
	answers := strings.Join([]string{
		"0.25", "n", "15m", "8", "MYWALLET", "", "y", "1.5", "0.2",
	}, "\n") + "\n"
	got, err := guideStrategyFile(
		newPrompter(strings.NewReader(answers), &output, true), strategyFile{})
	if err != nil {
		t.Fatal(err)
	}
	if err := got.validate(); err != nil {
		t.Fatalf("the guided answers produced an invalid file: %v", err)
	}
	if got.SizeSOL != "0.25" || got.ScheduleWindow != "15m" || got.TradesPerDay != 8 ||
		!got.Sweep.Enabled || got.Sweep.To != "MYWALLET" || !got.Telegram.Enabled {
		t.Fatalf("guided result lost an answer: %+v", got)
	}
	if got.Alerts.BalanceAboveSOL != "1.5" || got.Alerts.BalanceBelowSOL != "0.2" {
		t.Fatalf("guided result lost the balance alerts: %+v", got.Alerts)
	}
	// A blank sell price must clear BOTH, or the file is refused for half a pair.
	if got.SellAtUSD != "" || got.BuyAtUSD != "" {
		t.Errorf("a blank sell price left a half pair: %+v", got)
	}
	if strings.Contains(output.String(), "{") || strings.Contains(output.String(), "json") {
		t.Errorf("the guided editor showed JSON to the operator:\n%s", output.String())
	}
}

// A proof is bound to one destination. Changing the wallet must discard it,
// or the file would carry a signature for an address nobody approved.
func TestChangingTheDestinationDiscardsTheOldProof(t *testing.T) {
	current := strategyFile{
		SizeSOL: "0.05",
		Sweep: strategyFileSweep{
			Enabled: true, To: "OLDWALLET",
			ProofNonce: "n", ProofIssued: "i", ProofSignature: "s",
		},
	}
	var output strings.Builder
	answers := "0.05\nn\n1h\n8\ny\nNEWWALLET\n\nn\n\n\n"
	got, err := guideStrategyFile(
		newPrompter(strings.NewReader(answers), &output, true), current)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sweep.ProofNonce != "" || got.Sweep.ProofIssued != "" || got.Sweep.ProofSignature != "" {
		t.Fatalf("a proof for the old wallet survived the change: %+v", got.Sweep)
	}
	if !strings.Contains(output.String(), "no longer applies") {
		t.Errorf("the operator was not told the proof was discarded:\n%s", output.String())
	}
}

// Keeping the same wallet must KEEP the proof: re-signing for no reason is the
// thing that wastes a hardware-wallet session.
func TestKeepingTheDestinationKeepsTheProof(t *testing.T) {
	current := strategyFile{
		SizeSOL: "0.05",
		Sweep:   strategyFileSweep{Enabled: true, To: "SAME", ProofNonce: "n", ProofIssued: "i", ProofSignature: "s"},
	}
	var output strings.Builder
	got, err := guideStrategyFile(
		newPrompter(strings.NewReader("\n\n\n\n\n\nn\n\n\n"), &output, true), current)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sweep.ProofSignature != "s" {
		t.Fatalf("an unchanged destination lost its proof: %+v", got.Sweep)
	}
}

// Every optional part of a strategy is an explicit ON or OFF, then its details.
// Price rules used to be inferred from a BLANK answer, which is the worst shape
// for the person this editor exists for: pressing Enter to "keep what is shown"
// silently switched the strategy from trading at chosen prices to trading at
// whatever the pool gives, with nothing on screen saying so.
func TestOptionalFeaturesCanBeTurnedOffAndBackOn(t *testing.T) {
	// Start from a strategy with prices, the required sweep, and Telegram ON.
	on := strategyFile{
		SizeSOL: "0.05", SellAtUSD: "250", BuyAtUSD: "200",
		ScheduleWindow: "1h", TradesPerDay: 6,
		Sweep:    strategyFileSweep{Enabled: true, To: "WALLET", KeepSOL: "0.5"},
		Telegram: strategyFileTelegram{Enabled: true},
		Alerts:   strategyFileAlerts{PriceAboveUSD: "300", BalanceBelowSOL: "0.25"},
	}

	// Turn every optional feature OFF: price rules n and Telegram n. The sweep
	// remains because it is part of the all-in-one strategy contract.
	var out strings.Builder
	off, err := guideStrategyFile(
		newPrompter(strings.NewReader("\n"+"n\n"+"\n"+"\n"+"\n"+"\n"+"n\n"+"\n"+"\n"), &out, true), on)
	if err != nil {
		t.Fatal(err)
	}
	if off.SellAtUSD != "" || off.BuyAtUSD != "" {
		t.Errorf("turning price rules off left a threshold: %+v", off)
	}
	if off.Alerts.PriceAboveUSD != "" || off.Alerts.BalanceBelowSOL != "0.25" {
		t.Errorf("turning price rules off did not clear only their alerts: %+v", off.Alerts)
	}
	if !off.Sweep.Enabled || off.Sweep.To != "WALLET" || off.Telegram.Enabled {
		t.Errorf("a feature stayed on after being turned off: %+v", off)
	}
	if err := off.validate(); err != nil {
		t.Fatalf("the strategy with optional features off was refused: %v", err)
	}

	// And back ON from that state, supplying the details each one needs.
	var back strings.Builder
	again, err := guideStrategyFile(newPrompter(strings.NewReader(
		"\n"+"y\n"+"300\n"+"250\n"+"\n"+"\n"+"MYWALLET\n"+"2\n"+"y\n"+
			"350\n"+"225\n"+"\n"+"\n"),
		&back, true), off)
	if err != nil {
		t.Fatal(err)
	}
	if again.SellAtUSD != "300" || again.BuyAtUSD != "250" {
		t.Errorf("price rules did not come back on: %+v", again)
	}
	if !again.Sweep.Enabled || again.Sweep.To != "MYWALLET" || again.Sweep.KeepSOL != "2" {
		t.Errorf("sweep did not come back on with its details: %+v", again.Sweep)
	}
	if !again.Telegram.Enabled {
		t.Error("telegram did not come back on")
	}
	if again.Alerts.PriceAboveUSD != "350" || again.Alerts.PriceBelowUSD != "225" {
		t.Errorf("price alerts did not come back on: %+v", again.Alerts)
	}
	if err := again.validate(); err != nil {
		t.Fatalf("the re-enabled strategy is invalid: %v", err)
	}
	// Nothing in the questions may show JSON to the person answering them.
	for _, screen := range []string{out.String(), back.String()} {
		if strings.Contains(screen, "{") || strings.Contains(screen, "json") {
			t.Errorf("the guided editor showed JSON to the operator:\n%s", screen)
		}
	}
}

// "private file directory is not trusted" is the right answer for a library and
// a dead end for the FIRST command anybody runs: it names no directory, no
// reason, and nothing to do. The usual cause is a permissive umask, which
// nobody guesses — a group-writable directory is enough, and that is exactly
// what `sudo -u agent mkdir` produces.
func TestStrategyInitSaysWhyTheDirectoryWasRefused(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(directory, 0o775); err != nil {
		t.Fatal(err)
	}
	// os.Mkdir applies the process umask, which usually clears the group bit.
	if err := os.Chmod(directory, 0o775); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runStrategyInit([]string{filepath.Join(directory, "strategy.json")}, &out)
	if err == nil {
		t.Fatal("a strategy file was written into a group-writable directory")
	}
	message := err.Error()
	if !strings.Contains(message, directory) {
		t.Errorf("the refusal does not name the directory: %s", message)
	}
	if !strings.Contains(message, "chmod 700 "+directory) {
		t.Errorf("the refusal does not give the command that fixes it: %s", message)
	}
}

// A directory that is genuinely fine must still be written to, or the
// diagnosis has replaced the feature.
func TestStrategyInitWritesIntoAPrivateDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "strategy.json")
	var out bytes.Buffer
	if err := runStrategyInit([]string{path}, &out); err != nil {
		t.Fatalf("a private directory was refused: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no strategy file was written: %v", err)
	}
}
