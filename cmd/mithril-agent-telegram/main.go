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
	"strings"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/openaiexplainer"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/statussocket"
	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

const usage = `Usage:
  mithril-agent-telegram --status-socket PATH [--paper-status-socket PATH] --cursor PATH [--explanations off|openai|local] [--explanation-budget PATH]
  mithril-agent-telegram link    discover your chat ID (read-only; see link --help)
  mithril-agent-telegram test    send one test message to every allowed chat

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
	announcedActionsFile                = "announced-actions.json"
	announcedPaperFile                  = "announced-paper-events.json"
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
	// One help arm for every subcommand, so a third cannot forget it — `link`
	// had none while `test` did, and `link --help` errored while the top-level
	// usage told operators to run it.
	if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
		usage := map[string]string{"link": linkUsage, "test": testUsage}[args[0]]
		if usage == "" {
			return errors.New("unknown subcommand")
		}
		_, err := fmt.Fprintln(output, usage)
		return err
	}
	if len(args) > 0 && args[0] == "link" {
		if len(args) > 1 {
			return errors.New("link takes no arguments")
		}
		return runLink(ctx, output, getenv)
	}
	if len(args) > 0 && args[0] == "test" {
		if len(args) > 1 {
			return errors.New("test takes no arguments")
		}
		return runTest(ctx, output, getenv)
	}
	flags := flag.NewFlagSet("mithril-agent-telegram", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var statusSockets socketPaths
	var paperStatusSockets paperSocketPaths
	flags.Var(&statusSockets, "status-socket",
		"bounded operator status socket; repeat once per strategy leg")
	flags.Var(&paperStatusSockets, "paper-status-socket",
		"bounded paper simulation status socket; may be repeated")
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
	if flags.NArg() != 0 || len(statusSockets) == 0 || !cleanAbsolutePath(*cursorPath) {
		return errors.New("--status-socket and --cursor must be distinct clean absolute paths")
	}
	if base := filepath.Base(*cursorPath); base == announcedActionsFile || base == announcedPaperFile {
		return errors.New("--cursor uses a reserved Telegram state filename")
	}
	announcedActionsPath := filepath.Join(filepath.Dir(*cursorPath), announcedActionsFile)
	announcedPaperPath := filepath.Join(filepath.Dir(*cursorPath), announcedPaperFile)
	reservedStatePath := func(path string) bool {
		return path == announcedActionsPath || path == announcedPaperPath
	}
	for _, socket := range statusSockets {
		if !cleanAbsolutePath(socket) || socket == *cursorPath || reservedStatePath(socket) {
			return errors.New("--status-socket and --cursor must be distinct clean absolute paths")
		}
	}
	for _, socket := range paperStatusSockets {
		if !cleanAbsolutePath(socket) || socket == *cursorPath || statusSockets.contains(socket) ||
			reservedStatePath(socket) {
			return errors.New("paper status sockets must be distinct clean absolute paths")
		}
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
		(statusSockets.contains(resolvedBudgetPath) || paperStatusSockets.contains(resolvedBudgetPath) ||
			resolvedBudgetPath == *cursorPath || reservedStatePath(resolvedBudgetPath)) {
		return errors.New("explanation budget path must be distinct from status socket and cursor paths")
	}
	sources, err := statusReaders(statusSockets)
	if err != nil {
		return err
	}
	paperSources, err := paperStatusReaders(paperStatusSockets)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		output,
		"Mithril Telegram operator starting: read-only, %d allowed chat(s), %d paper source(s), explanations %s. Waiting for Telegram…\n",
		len(chatIDs), len(paperSources), explanationMode,
	); err != nil {
		return err
	}
	return runTelegramService(ctx, telegramoperator.Config{
		Bot: bot, Cursor: telegramoperator.FileCursor(*cursorPath),
		Sources: sources, PaperSources: paperSources,
		// Derived from the cursor rather than taking a flag of its own: it is
		// the same private state directory, already validated absolute, and a
		// sibling name cannot collide with it. That also means an existing
		// install needs no new flag, credential, or unit change to stop
		// repeating announcements after a restart.
		AnnouncedPath:  filepath.Join(filepath.Dir(*cursorPath), announcedActionsFile),
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

// socketPaths collects --status-socket given more than once: a strategy has a
// sell leg, a buy leg and a sweep, and one bot token permits exactly one
// long-poller, so one process has to read them all.
type socketPaths []string

func (p *socketPaths) String() string { return strings.Join(*p, ",") }

func (p *socketPaths) Set(value string) error {
	if !cleanAbsolutePath(value) {
		return errors.New("--status-socket must be a clean absolute path")
	}
	for _, existing := range *p {
		if existing == value {
			return errors.New("--status-socket was given the same path twice")
		}
	}
	*p = append(*p, value)
	return nil
}

func (p socketPaths) contains(candidate string) bool {
	for _, existing := range p {
		if existing == candidate {
			return true
		}
	}
	return false
}

func statusReaders(paths socketPaths) ([]telegramoperator.StatusReader, error) {
	readers := make([]telegramoperator.StatusReader, 0, len(paths))
	for _, path := range paths {
		reader, err := statussocket.NewReader(path)
		if err != nil {
			return nil, err
		}
		readers = append(readers, reader)
	}
	return readers, nil
}

type paperSocketPaths []string

func (p *paperSocketPaths) String() string { return strings.Join(*p, ",") }

func (p *paperSocketPaths) Set(value string) error {
	if !cleanAbsolutePath(value) {
		return errors.New("--paper-status-socket must be a clean absolute path")
	}
	if p.contains(value) {
		return errors.New("--paper-status-socket was given the same path twice")
	}
	*p = append(*p, value)
	return nil
}

func (p paperSocketPaths) contains(candidate string) bool {
	for _, existing := range p {
		if existing == candidate {
			return true
		}
	}
	return false
}

func paperStatusReaders(paths paperSocketPaths) ([]telegramoperator.PaperStatusReader, error) {
	readers := make([]telegramoperator.PaperStatusReader, 0, len(paths))
	for _, path := range paths {
		reader, err := paperstatus.NewSocketReader(path)
		if err != nil {
			return nil, err
		}
		readers = append(readers, reader)
	}
	return readers, nil
}
