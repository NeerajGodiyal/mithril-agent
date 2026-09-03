package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/openaiexplainer"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

const commandTestToken = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcd"

func TestRunBuildsReadOnlyTelegramServiceWithoutExposingInputs(t *testing.T) {
	environment := map[string]string{
		telegramoperator.BotTokenEnvironment:   commandTestToken,
		telegramoperator.AllowedIDsEnvironment: "123,-456",
	}
	var captured telegramoperator.Config
	withServiceRunner(t, func(_ context.Context, config telegramoperator.Config) error {
		captured = config
		return nil
	})
	var output bytes.Buffer
	err := run(t.Context(), []string{
		"--status-socket", "/private/operator-status.sock",
		"--paper-status-socket", "SOL/USDC=/private/paper-status.sock",
		"--cursor", "/private/telegram-cursor.json",
	}, &output, func(key string) string { return environment[key] })
	if err != nil {
		t.Fatal(err)
	}
	if captured.Bot == nil || captured.Cursor == nil || len(captured.Sources) == 0 ||
		len(captured.PaperSources) != 1 ||
		captured.Explainer != nil || captured.ExplanationBudget != nil ||
		len(captured.AllowedChatIDs) != 2 {
		t.Fatalf("config = %+v", captured)
	}
	if labeled, ok := captured.PaperSources[0].(interface{ SourceLabel() string }); !ok || labeled.SourceLabel() != "SOL/USDC" {
		t.Fatalf("paper source label = %T", captured.PaperSources[0])
	}
	text := output.String()
	if !strings.Contains(text, "read-only, 2 allowed chat(s), 1 paper source(s), explanations off") ||
		strings.Contains(text, commandTestToken) || strings.Contains(text, "123") ||
		strings.Contains(text, "/private/") {
		t.Fatalf("startup output = %q", text)
	}
}

func TestRunWiresOptionalPaperExperiment(t *testing.T) {
	environment := map[string]string{
		telegramoperator.BotTokenEnvironment:   commandTestToken,
		telegramoperator.AllowedIDsEnvironment: "123",
	}
	var captured telegramoperator.Config
	withServiceRunner(t, func(_ context.Context, config telegramoperator.Config) error {
		captured = config
		return nil
	})
	err := run(t.Context(), []string{
		"--status-socket", "/private/operator.sock",
		"--paper-status-socket", "SOL/USDC=/private/sol.sock",
		"--optional-paper-status-socket", "SOL-PERP=/private/perp.sock",
		"--cursor", "/private/cursor.json",
	}, io.Discard, func(key string) string { return environment[key] })
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.PaperSources) != 2 {
		t.Fatalf("paper sources = %d", len(captured.PaperSources))
	}
	optional, ok := captured.PaperSources[1].(interface{ Optional() bool })
	label, labeled := captured.PaperSources[1].(interface{ SourceLabel() string })
	if !ok || !optional.Optional() || !labeled || label.SourceLabel() != "SOL-PERP" {
		t.Fatalf("optional paper source = %T", captured.PaperSources[1])
	}
}

func TestRunWiresOptionalOpenAIExplanation(t *testing.T) {
	environment := map[string]string{
		telegramoperator.BotTokenEnvironment:   commandTestToken,
		telegramoperator.AllowedIDsEnvironment: "123",
		openAIKeyEnvironment:                   "test-api-key",
		openAIModelEnvironment:                 "gpt-test",
	}
	var captured telegramoperator.Config
	withServiceRunner(t, func(_ context.Context, config telegramoperator.Config) error {
		captured = config
		return nil
	})
	cursorPath := filepath.Join(t.TempDir(), "telegram-cursor.json")
	if err := run(t.Context(), []string{
		"--status-socket", "/private/operator-status.sock",
		"--cursor", cursorPath,
		"--explanations", "openai",
	}, &bytes.Buffer{}, func(key string) string { return environment[key] }); err != nil {
		t.Fatal(err)
	}
	if _, ok := captured.Explainer.(*openaiexplainer.Client); !ok || captured.ExplanationBudget == nil {
		t.Fatalf("explainer = %T", captured.Explainer)
	}
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	for range telegramoperator.DefaultDailyExplanationRequests {
		if err := captured.ExplanationBudget.Reserve(now); err != nil {
			t.Fatalf("default budget exhausted early: %v", err)
		}
	}
	if err := captured.ExplanationBudget.Reserve(now); err == nil {
		t.Fatal("default daily explanation request budget was not enforced")
	}
	if _, err := os.Stat(cursorPath + ".explanation-budget.json"); err != nil {
		t.Fatalf("derived explanation budget: %v", err)
	}
}

func TestRunRejectsAmbiguousOrUnsafeConfiguration(t *testing.T) {
	base := map[string]string{
		telegramoperator.BotTokenEnvironment:   commandTestToken,
		telegramoperator.AllowedIDsEnvironment: "123",
	}
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "relative status", args: []string{"--status-socket", "status.sock", "--cursor", "/private/cursor.json"}},
		{name: "same path", args: []string{"--status-socket", "/private/state.sock", "--cursor", "/private/state.sock"}},
		{name: "paper aliases live", args: []string{"--status-socket", "/private/state.sock", "--paper-status-socket", "/private/state.sock", "--cursor", "/private/cursor.json"}},
		{name: "paper aliases cursor", args: []string{"--status-socket", "/private/status.sock", "--paper-status-socket", "/private/cursor.json", "--cursor", "/private/cursor.json"}},
		{name: "cursor aliases action dedup", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/announced-actions.json"}},
		{name: "cursor aliases paper dedup", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/announced-paper-events.json"}},
		{name: "status aliases action dedup", args: []string{"--status-socket", "/private/announced-actions.json", "--cursor", "/private/cursor.json"}},
		{name: "paper aliases paper dedup", args: []string{"--status-socket", "/private/status.sock", "--paper-status-socket", "/private/announced-paper-events.json", "--cursor", "/private/cursor.json"}},
		{name: "invalid paper label", args: []string{"--status-socket", "/private/status.sock", "--paper-status-socket", "bad label=/private/paper.sock", "--cursor", "/private/cursor.json"}},
		{name: "duplicate paper label", args: []string{"--status-socket", "/private/status.sock", "--paper-status-socket", "SOL=/private/sol-1.sock", "--paper-status-socket", "SOL=/private/sol-2.sock", "--cursor", "/private/cursor.json"}},
		{name: "required optional label collision", args: []string{"--status-socket", "/private/status.sock", "--paper-status-socket", "SOL=/private/sol.sock", "--optional-paper-status-socket", "SOL=/private/sol-perp.sock", "--cursor", "/private/cursor.json"}},
		{name: "unknown mode", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanations", "other"}},
		{name: "custom remote", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanations", "local"}, env: map[string]string{
			openAIKeyEnvironment: "test-api-key", openAIModelEnvironment: "gpt-test",
			openAIBaseURLEnvironment: "https://example.com",
		}},
		{name: "unused key", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json"}, env: map[string]string{
			openAIKeyEnvironment: "must-not-be-silently-unused",
		}},
		{name: "unused budget", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanation-budget", "/private/budget.json"}},
		{name: "unused daily limit", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json"}, env: map[string]string{
			dailyExplanationRequestsEnvironment: "1",
		}},
		{name: "budget aliases cursor", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanations", "openai", "--explanation-budget", "/private/cursor.json"}, env: map[string]string{
			openAIKeyEnvironment: "test-api-key", openAIModelEnvironment: "gpt-test",
		}},
		{name: "budget aliases action dedup", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanations", "openai", "--explanation-budget", "/private/announced-actions.json"}, env: map[string]string{
			openAIKeyEnvironment: "test-api-key", openAIModelEnvironment: "gpt-test",
		}},
		{name: "budget aliases paper dedup", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanations", "openai", "--explanation-budget", "/private/announced-paper-events.json"}, env: map[string]string{
			openAIKeyEnvironment: "test-api-key", openAIModelEnvironment: "gpt-test",
		}},
		{name: "invalid daily limit", args: []string{"--status-socket", "/private/status.sock", "--cursor", "/private/cursor.json", "--explanations", "openai", "--explanation-budget", "/private/budget.json"}, env: map[string]string{
			openAIKeyEnvironment: "test-api-key", openAIModelEnvironment: "gpt-test",
			dailyExplanationRequestsEnvironment: "0",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := make(map[string]string, len(base)+len(test.env))
			for key, value := range base {
				environment[key] = value
			}
			for key, value := range test.env {
				environment[key] = value
			}
			err := run(t.Context(), test.args, &bytes.Buffer{}, func(key string) string {
				return environment[key]
			})
			secret := environment[openAIKeyEnvironment]
			if err == nil || secret != "" && strings.Contains(err.Error(), secret) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBoundedHTTPClientHasNoProxyAndStrictTLS(t *testing.T) {
	client := boundedHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || client.Timeout == 0 || transport.Proxy != nil ||
		transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion == 0 ||
		transport.ResponseHeaderTimeout == 0 {
		t.Fatalf("client=%+v transport=%+v", client, transport)
	}
}

func withServiceRunner(
	t *testing.T,
	runner func(context.Context, telegramoperator.Config) error,
) {
	t.Helper()
	previous := runTelegramService
	runTelegramService = runner
	t.Cleanup(func() { runTelegramService = previous })
}

func TestRunPropagatesServiceFailureWithoutSecretDetail(t *testing.T) {
	withServiceRunner(t, func(context.Context, telegramoperator.Config) error {
		return errors.New("service stopped")
	})
	environment := map[string]string{
		telegramoperator.BotTokenEnvironment:   commandTestToken,
		telegramoperator.AllowedIDsEnvironment: "123",
	}
	err := run(t.Context(), []string{
		"--status-socket", "/private/operator-status.sock",
		"--cursor", "/private/telegram-cursor.json",
	}, &bytes.Buffer{}, func(key string) string { return environment[key] })
	if err == nil || err.Error() != "service stopped" {
		t.Fatalf("error = %v", err)
	}
}

func TestLinkRequiresTheTokenFromEnvironmentOnly(t *testing.T) {
	err := run(t.Context(), []string{"link"}, io.Discard, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "MITHRIL_AGENT_TELEGRAM_BOT_TOKEN") {
		t.Fatalf("link without the env token must fail naming the variable, got %v", err)
	}
	if err := run(t.Context(), []string{"link", "extra"}, io.Discard, func(string) string { return "" }); err == nil {
		t.Fatal("link with arguments must be refused")
	}
}

type testBotStub struct {
	sent    map[int64]string
	failFor map[int64]error
}

func (b *testBotStub) Poll(context.Context, int64, time.Duration) ([]telegramoperator.Update, error) {
	return nil, errors.New("test must never poll")
}

func (b *testBotStub) Send(_ context.Context, chatID int64, message string) error {
	if err := b.failFor[chatID]; err != nil {
		return err
	}
	if b.sent == nil {
		b.sent = map[int64]string{}
	}
	b.sent[chatID] = message
	return nil
}

func withTestBot(t *testing.T, bot telegramoperator.Bot) {
	t.Helper()
	previous := newTestBot
	newTestBot = func(string) (telegramoperator.Bot, error) { return bot, nil }
	t.Cleanup(func() { newTestBot = previous })
}

// The command exists to answer "will I actually get the message?", so a chat
// that cannot receive must be named individually and must make the command
// fail. A single pass/fail would hide the case where one of two chats works,
// which is the one an operator is most likely to trust by mistake.
func TestTelegramTestReportsEveryChatAndFailsOnAnyLoss(t *testing.T) {
	bot := &testBotStub{failFor: map[int64]error{-456: errors.New("chat not found")}}
	withTestBot(t, bot)
	var output bytes.Buffer
	err := run(t.Context(), []string{"test"}, &output, func(key string) string {
		return map[string]string{
			telegramoperator.BotTokenEnvironment:   commandTestToken,
			telegramoperator.AllowedIDsEnvironment: "123,-456",
		}[key]
	})
	if err == nil {
		t.Fatal("a chat that could not receive the test still reported success")
	}
	text := output.String()
	if !strings.Contains(text, "chat 123: delivered") {
		t.Errorf("the working chat was not reported: %s", text)
	}
	if !strings.Contains(text, "chat -456: FAILED") {
		t.Errorf("the failing chat was not named: %s", text)
	}
	// The working chat is still attempted; one bad ID must not silence the rest.
	if bot.sent[123] == "" {
		t.Error("the working chat was skipped after the other failed")
	}
	if strings.Contains(text, commandTestToken) {
		t.Error("the token leaked into the output")
	}
}

// Nothing about trades may reach this path: it must be safe to run before any
// setup exists, and it must never imply a trade happened.
func TestTelegramTestSendsOnlyTheFixedLine(t *testing.T) {
	bot := &testBotStub{}
	withTestBot(t, bot)
	var output bytes.Buffer
	if err := run(t.Context(), []string{"test"}, &output, func(key string) string {
		return map[string]string{
			telegramoperator.BotTokenEnvironment:   commandTestToken,
			telegramoperator.AllowedIDsEnvironment: "123",
		}[key]
	}); err != nil {
		t.Fatal(err)
	}
	if bot.sent[123] != testMessage {
		t.Fatalf("sent %q, want the fixed test line", bot.sent[123])
	}
	if !strings.Contains(testMessage, "No trade happened") {
		t.Error("the test line does not say that no trade happened")
	}
}

// An unset variable must say so. Both are read before any network call, so this
// is the first thing an operator hits and it has to point somewhere.
func TestTelegramTestNamesMissingConfiguration(t *testing.T) {
	for name, environment := range map[string]map[string]string{
		"no token": {telegramoperator.AllowedIDsEnvironment: "123"},
		"no chats": {telegramoperator.BotTokenEnvironment: commandTestToken},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			err := run(t.Context(), []string{"test"}, &output, func(key string) string {
				return environment[key]
			})
			if err == nil {
				t.Fatal("missing configuration was accepted")
			}
			if !strings.Contains(err.Error(), "not set") {
				t.Errorf("error did not say the variable is unset: %v", err)
			}
		})
	}
}

// Telegram answers a distinct status for each common misconfiguration, and each
// one has a different fix. Collapsing them into one sentence is what made
// "telegram is not working" unanswerable.
func TestTelegramTestNamesTheCauseNotJustTheFailure(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		want   string
	}{
		"never pressed start": {403, "press Start"},
		"wrong chat id":       {400, "link"},
		"bad token":           {401, telegramoperator.BotTokenEnvironment},
		"rate limited":        {429, "rate limiting"},
	} {
		t.Run(name, func(t *testing.T) {
			bot := &testBotStub{failFor: map[int64]error{
				123: telegramoperator.StatusError{Status: test.status},
			}}
			withTestBot(t, bot)
			var output bytes.Buffer
			err := run(t.Context(), []string{"test"}, &output, func(key string) string {
				return map[string]string{
					telegramoperator.BotTokenEnvironment:   commandTestToken,
					telegramoperator.AllowedIDsEnvironment: "123",
				}[key]
			})
			if err == nil {
				t.Fatal("a refused delivery reported success")
			}
			if !strings.Contains(output.String(), test.want) {
				t.Errorf("HTTP %d did not name the fix (%q):\n%s",
					test.status, test.want, output.String())
			}
		})
	}
}

// An error that is not a Telegram status must still be reported, not swallowed
// by the classifier's default.
func TestTelegramTestStillReportsUnclassifiedFailures(t *testing.T) {
	bot := &testBotStub{failFor: map[int64]error{123: errors.New("network unreachable")}}
	withTestBot(t, bot)
	var output bytes.Buffer
	if err := run(t.Context(), []string{"test"}, &output, func(key string) string {
		return map[string]string{
			telegramoperator.BotTokenEnvironment:   commandTestToken,
			telegramoperator.AllowedIDsEnvironment: "123",
		}[key]
	}); err == nil {
		t.Fatal("an unclassified failure reported success")
	}
	if !strings.Contains(output.String(), "network unreachable") {
		t.Errorf("the underlying error was lost:\n%s", output.String())
	}
}
