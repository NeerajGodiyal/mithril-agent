package main

import (
	"bytes"
	"context"
	"errors"
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
		"--cursor", "/private/telegram-cursor.json",
	}, &output, func(key string) string { return environment[key] })
	if err != nil {
		t.Fatal(err)
	}
	if captured.Bot == nil || captured.Cursor == nil || captured.Status == nil ||
		captured.Explainer != nil || captured.ExplanationBudget != nil ||
		len(captured.AllowedChatIDs) != 2 {
		t.Fatalf("config = %+v", captured)
	}
	text := output.String()
	if !strings.Contains(text, "read-only, 2 allowed chat(s), explanations off") ||
		strings.Contains(text, commandTestToken) || strings.Contains(text, "123") ||
		strings.Contains(text, "/private/") {
		t.Fatalf("startup output = %q", text)
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
