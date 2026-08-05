package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/openaiexplainer"
	"github.com/Overclock-Validator/mithril-agent/statussocket"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

const usage = `Usage:
  mithril-agent-telegram --status-socket PATH --cursor PATH [--explanations off|openai|local] [--explanation-budget PATH]
  mithril-agent-telegram link    discover your chat ID (read-only; see link --help)

Environment:
  MITHRIL_AGENT_TELEGRAM_BOT_TOKEN  Telegram bot token
  MITHRIL_AGENT_TELEGRAM_CHAT_IDS   comma-separated numeric chat allowlist
  MITHRIL_AGENT_TELEGRAM_EXPLANATIONS  off, openai, or local (default off)
  MITHRIL_AGENT_TELEGRAM_DAILY_EXPLANATION_REQUESTS  daily request limit (default 20)
  OPENAI_API_KEY                    required only for explanations
  MITHRIL_AGENT_OPENAI_MODEL        required only for explanations
  MITHRIL_AGENT_OPENAI_BASE_URL     literal loopback origin for local mode only`

const (
	openAIKeyEnvironment                = "OPENAI_API_KEY"
	openAIModelEnvironment              = "MITHRIL_AGENT_OPENAI_MODEL"
	openAIBaseURLEnvironment            = "MITHRIL_AGENT_OPENAI_BASE_URL"
	explanationEnvironment              = "MITHRIL_AGENT_TELEGRAM_EXPLANATIONS"
	dailyExplanationRequestsEnvironment = "MITHRIL_AGENT_TELEGRAM_DAILY_EXPLANATION_REQUESTS"
)

var runTelegramService = func(ctx context.Context, config telegramoperator.Config) error {
	service, err := telegramoperator.New(config)
	if err != nil {
		return err
	}
	return service.Run(ctx)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-telegram:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	output io.Writer,
	getenv func(string) string,
) error {
	if len(args) > 0 && args[0] == "link" {
		if len(args) > 1 {
			return errors.New("link takes no arguments")
		}
		return runLink(ctx, output, getenv)
	}
	flags := flag.NewFlagSet("mithril-agent-telegram", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	statusSocketPath := flags.String("status-socket", "", "bounded operator status socket")
	cursorPath := flags.String("cursor", "", "private Telegram update cursor")
	explanations := flags.String("explanations", "", "off, openai, or local")
	explanationBudgetPath := flags.String("explanation-budget", "", "private daily explanation request budget")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, usage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || !cleanAbsolutePath(*statusSocketPath) || !cleanAbsolutePath(*cursorPath) {
		return errors.New("--status-socket and --cursor must be distinct clean absolute paths")
	}
	if *statusSocketPath == *cursorPath {
		return errors.New("--status-socket and --cursor must be distinct clean absolute paths")
	}
	if getenv == nil {
		return errors.New("environment reader is required")
	}
	explanationMode := *explanations
	if explanationMode == "" {
		explanationMode = getenv(explanationEnvironment)
	}
	if explanationMode == "" {
		explanationMode = "off"
	}
	chatIDs, err := telegramoperator.ParseAllowedChatIDs(
		getenv(telegramoperator.AllowedIDsEnvironment),
	)
	if err != nil {
		return err
	}
	bot, err := telegramoperator.NewHTTPBot(
		getenv(telegramoperator.BotTokenEnvironment), boundedHTTPClient(),
	)
	if err != nil {
		return err
	}
	explainer, err := loadExplainer(explanationMode, getenv)
	if err != nil {
		return err
	}
	resolvedBudgetPath := *explanationBudgetPath
	if explanationMode != "off" && resolvedBudgetPath == "" {
		resolvedBudgetPath = *cursorPath + ".explanation-budget.json"
	}
	explanationBudget, err := loadExplanationBudget(
		explanationMode, resolvedBudgetPath,
		getenv(dailyExplanationRequestsEnvironment),
	)
	if err != nil {
		return err
	}
	if explanationBudget != nil &&
		(resolvedBudgetPath == *statusSocketPath || resolvedBudgetPath == *cursorPath) {
		return errors.New("explanation budget path must be distinct from status socket and cursor paths")
	}
	statusReader, err := statussocket.NewReader(*statusSocketPath)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		output,
		"Mithril Telegram operator starting: read-only, %d allowed chat(s), explanations %s. Waiting for Telegram…\n",
		len(chatIDs), explanationMode,
	); err != nil {
		return err
	}
	return runTelegramService(ctx, telegramoperator.Config{
		Bot: bot, Cursor: telegramoperator.FileCursor(*cursorPath),
		Status:         statusReader,
		AllowedChatIDs: chatIDs, Explainer: explainer,
		ExplanationBudget: explanationBudget,
	})
}

func loadExplanationBudget(
	mode string,
	path string,
	configuredLimit string,
) (telegramoperator.ExplanationBudget, error) {
	if mode == "off" {
		if path != "" || configuredLimit != "" {
			return nil, errors.New("explanation budget is configured while explanations are off")
		}
		return nil, nil
	}
	if !cleanAbsolutePath(path) {
		return nil, errors.New("enabled explanations require --explanation-budget with a clean absolute path")
	}
	limit := uint64(telegramoperator.DefaultDailyExplanationRequests)
	if configuredLimit != "" {
		parsed, err := strconv.ParseUint(configuredLimit, 10, 32)
		if err != nil || parsed == 0 || parsed > uint64(telegramoperator.MaxDailyExplanationRequests) {
			return nil, errors.New("daily explanation request limit is invalid")
		}
		limit = parsed
	}
	return telegramoperator.NewFileExplanationBudget(path, uint32(limit))
}

func loadExplainer(
	mode string,
	getenv func(string) string,
) (telegramoperator.Explainer, error) {
	key := getenv(openAIKeyEnvironment)
	model := getenv(openAIModelEnvironment)
	baseURL := getenv(openAIBaseURLEnvironment)
	switch mode {
	case "off":
		if key != "" || model != "" || baseURL != "" {
			return nil, errors.New("explanation environment is set while explanations are off")
		}
		return nil, nil
	case "openai":
		if baseURL != "" {
			return nil, errors.New("OpenAI mode does not accept a custom API origin")
		}
		return openaiexplainer.New(key, model)
	case "local":
		if baseURL == "" {
			return nil, errors.New("local explanation mode requires a literal loopback API origin")
		}
		return openaiexplainer.NewWithBaseURL(key, model, baseURL)
	default:
		return nil, errors.New("--explanations must be off, openai, or local")
	}
}

func cleanAbsolutePath(path string) bool {
	return path != "" && path != string(filepath.Separator) &&
		filepath.IsAbs(path) && filepath.Clean(path) == path
}

func boundedHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{
		Timeout: 5 * time.Second, KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ResponseHeaderTimeout = 55 * time.Second
	transport.ExpectContinueTimeout = time.Second
	transport.IdleConnTimeout = 30 * time.Second
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
}
