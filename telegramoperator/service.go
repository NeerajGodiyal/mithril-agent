// Package telegramoperator exposes the bounded, read-only operator status over
// a deliberately small Telegram command surface.
package telegramoperator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	BotTokenEnvironment   = "MITHRIL_AGENT_TELEGRAM_BOT_TOKEN"
	AllowedIDsEnvironment = "MITHRIL_AGENT_TELEGRAM_CHAT_IDS"

	maxInputBytes       = 1024
	maxQuestionBytes    = 512
	maxExplanationBytes = 1600
	maxOutputBytes      = 3500
	defaultPollTimeout  = 25 * time.Second
	defaultMinInterval  = time.Second
	defaultExplainTime  = 5 * time.Second
	statusSource        = "local bounded operator status"
)

// Update is the only Telegram update shape consumed by Service.
type Update struct {
	ID     int64
	ChatID int64
	Text   string
}

// Bot is a narrow transport interface. Implementations must positively verify
// Telegram's ok response before returning success.
type Bot interface {
	Poll(context.Context, int64, time.Duration) ([]Update, error)
	Send(context.Context, int64, string) error
}

// Cursor durably records the next Telegram update ID. Store must reject
// regressions so an old process cannot reopen already acknowledged updates.
type Cursor interface {
	Load() (int64, error)
	Store(int64) error
}

// StatusReader returns only the bounded, non-secret operator status artifact.
type StatusReader interface {
	Read() (operatorstatus.Snapshot, error)
}

// ExplanationRequest is the complete optional model boundary. It contains a
// bounded question and deterministic text derived from operatorstatus only.
// There is intentionally no action, tool, key, configuration, or endpoint API.
type ExplanationRequest struct {
	Question   string
	StatusText string
	ObservedAt time.Time
	Stale      bool
}

// Explainer may explain status, but cannot perform or request an agent action.
// Implementations must treat StatusText as data and honor context cancellation.
type Explainer interface {
	Explain(context.Context, ExplanationRequest) (string, error)
}

type Config struct {
	Bot               Bot
	Cursor            Cursor
	Status            StatusReader
	AllowedChatIDs    []int64
	Explainer         Explainer
	ExplanationBudget ExplanationBudget
	Now               func() time.Time
	PollTimeout       time.Duration
	MinimumInterval   time.Duration
	ExplanationLimit  time.Duration
}

// Service has one sequential long-poll consumer. Read-only commands do not
// depend on the optional explanation provider.
type Service struct {
	bot               Bot
	cursor            Cursor
	status            StatusReader
	allowed           map[int64]struct{}
	explainer         Explainer
	explanationBudget ExplanationBudget
	now               func() time.Time
	pollTimeout       time.Duration
	minInterval       time.Duration
	explainTimeout    time.Duration

	rateMu  sync.Mutex
	next    map[int64]time.Time
	running atomic.Bool
}

func New(config Config) (*Service, error) {
	if config.Bot == nil || config.Cursor == nil || config.Status == nil {
		return nil, errors.New("Telegram bot, cursor, and operator status reader are required")
	}
	allowed := make(map[int64]struct{}, len(config.AllowedChatIDs))
	for _, id := range config.AllowedChatIDs {
		if id == 0 {
			return nil, errors.New("allowed Telegram chat IDs must be nonzero")
		}
		if _, exists := allowed[id]; exists {
			return nil, errors.New("allowed Telegram chat IDs must be unique")
		}
		allowed[id] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, errors.New("at least one allowed Telegram chat ID is required")
	}
	if (config.Explainer == nil) != (config.ExplanationBudget == nil) {
		return nil, errors.New("explanation provider and budget must be configured together")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.PollTimeout == 0 {
		config.PollTimeout = defaultPollTimeout
	}
	if config.MinimumInterval == 0 {
		config.MinimumInterval = defaultMinInterval
	}
	if config.ExplanationLimit == 0 {
		config.ExplanationLimit = defaultExplainTime
	}
	if config.PollTimeout < time.Second || config.PollTimeout > 50*time.Second {
		return nil, errors.New("Telegram poll timeout must be between 1 and 50 seconds")
	}
	if config.MinimumInterval < 100*time.Millisecond || config.MinimumInterval > time.Minute {
		return nil, errors.New("Telegram command interval must be between 100 milliseconds and 1 minute")
	}
	if config.ExplanationLimit < 100*time.Millisecond || config.ExplanationLimit > 15*time.Second {
		return nil, errors.New("explanation timeout must be between 100 milliseconds and 15 seconds")
	}
	return &Service{
		bot: config.Bot, cursor: config.Cursor, status: config.Status, allowed: allowed,
		explainer: config.Explainer, explanationBudget: config.ExplanationBudget, now: config.Now,
		pollTimeout: config.PollTimeout, minInterval: config.MinimumInterval,
		explainTimeout: config.ExplanationLimit, next: make(map[int64]time.Time),
	}, nil
}

// Run consumes updates sequentially. An update is advanced only after an
// allowed reply receives Telegram's positive acknowledgement, or after the
// update is intentionally ignored. A transport failure exits for supervision.
func (s *Service) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("Telegram operator already has an active consumer")
	}
	defer s.running.Store(false)
	if cursor, ok := s.cursor.(interface{ lockConsumer() (func(), error) }); ok {
		unlock, err := cursor.lockConsumer()
		if err != nil {
			return err
		}
		defer unlock()
	}

	offset, err := s.cursor.Load()
	if err != nil {
		return errors.New("load Telegram update cursor")
	}
	if offset < 0 {
		return errors.New("Telegram update cursor is invalid")
	}
	for {
		updates, err := s.bot.Poll(ctx, offset, s.pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("poll Telegram updates")
		}
		deferredOffset := int64(0)
		for _, update := range updates {
			if update.ID < offset {
				continue
			}
			if update.ID == math.MaxInt64 {
				return errors.New("Telegram update ID cannot be advanced")
			}
			reply, send := s.Reply(ctx, update.ChatID, update.Text)
			if send {
				if err := s.bot.Send(ctx, update.ChatID, reply); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return errors.New("send Telegram reply")
				}
			}
			nextOffset := update.ID + 1
			// Telegram's send acknowledgement necessarily precedes local cursor
			// persistence. A crash in this narrow window, or a lost response after
			// Telegram accepts the message, can replay a read-only reply. Persisting
			// before acknowledgement could lose the reply instead.
			if send {
				if err := s.cursor.Store(nextOffset); err != nil {
					return errors.New("store Telegram update cursor")
				}
				deferredOffset = 0
			} else {
				// Unauthorized and locally rate-limited updates do not receive a
				// reply. Coalesce their durable cursor writes so public update
				// volume cannot force one file and directory fsync per message.
				deferredOffset = nextOffset
			}
			offset = nextOffset
		}
		if deferredOffset > 0 {
			if err := s.cursor.Store(deferredOffset); err != nil {
				return errors.New("store Telegram update cursor")
			}
		}
	}
}

// Reply returns bounded plain text for one message. false means that the
// update is intentionally ignored (unauthorized chat or local rate limit).
func (s *Service) Reply(ctx context.Context, chatID int64, text string) (string, bool) {
	if _, allowed := s.allowed[chatID]; !allowed {
		return "", false
	}
	now := s.now().UTC()
	if !s.allowAt(chatID, now) {
		return "", false
	}
	if len(text) > maxInputBytes || !utf8.ValidString(text) {
		return bounded("Request: rejected\nReason: input is invalid or too long\n" + footer(now)), true
	}
	command, argument := parseCommand(text)
	switch command {
	case "/help":
		return bounded(s.help(now)), true
	case "/status":
		if argument != "" {
			return bounded("Usage: /status\n" + footer(now)), true
		}
		report, _, _, _ := s.statusReport(now)
		return bounded(report), true
	case "/last_trade":
		if argument != "" {
			return bounded("Usage: /last_trade\n" + footer(now)), true
		}
		return bounded(s.lastTrade(now)), true
	case "/price":
		if argument != "" {
			return bounded("Usage: /price\n" + footer(now)), true
		}
		return bounded(s.price(now)), true
	case "/explain":
		return bounded(s.explain(ctx, argument, now)), true
	default:
		return bounded("Command: unknown\nUse /help for the read-only command list.\n" + footer(now)), true
	}
}

func (s *Service) allowAt(chatID int64, now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if now.Before(s.next[chatID]) {
		return false
	}
	s.next[chatID] = now.Add(s.minInterval)
	return true
}

func (s *Service) help(now time.Time) string {
	commands := "/help — show this read-only command list\n/status — current bounded operator status\n/price — current price rule and observation\n/last_trade — most recent recorded trade"
	if s.explainer != nil {
		commands += "\n/explain QUESTION — optional explanation of the same bounded status"
	}
	return "Mithril operator — read only\n" + commands +
		"\nAlerts arrive only for faults needing action. Restarts, completed" +
		"\nactions and price targets are not sent; ask with the commands above.\n" +
		"This process cannot enable, sign, submit, stop, or configure actions.\n" + footer(now)
}

func (s *Service) statusReport(now time.Time) (string, operatorstatus.Snapshot, bool, bool) {
	snapshot, err := s.status.Read()
	if err != nil {
		return "Status: unknown\nReason: operator status is unavailable or invalid\n" + footer(now), operatorstatus.Snapshot{}, false, false
	}
	freshness, age, usable := snapshotFreshness(snapshot, now)
	if !usable {
		return "Status: unknown\nReason: operator status timestamp is in the future\n" +
			observedFooter(snapshot.ObservedAt, now), operatorstatus.Snapshot{}, false, false
	}
	attention := operatorstatus.RequiresAttention(snapshot.Result, snapshot.Control, now)
	report := fmt.Sprintf(
		"Status: %s\nFreshness: %s (%ds old)\nControl: %s\nAttention required: %s\nSubmitted: %s",
		safeField(describeDecision(snapshot.Result.Decision), 64), freshness, age,
		safeField(snapshot.Control.Mode, 32), yesNo(attention), yesNo(snapshot.Result.Submitted),
	)
	if snapshot.Result.Verdict != "" {
		report += "\nOutcome: " + safeField(describeVerdict(snapshot.Result.Verdict), 64)
	}
	if snapshot.Result.Reason != "" {
		report += "\nReason: " + safeField(snapshot.Result.Reason, 160)
	}
	if trigger := snapshot.Result.PriceTrigger; trigger != nil {
		report += "\nPrice rule: " + triggerState(*trigger)
	}
	report += "\n" + observedFooter(snapshot.ObservedAt, now)
	return report, snapshot, true, freshness == "stale"
}

func (s *Service) price(now time.Time) string {
	snapshot, err := s.status.Read()
	if err != nil {
		return "Price: unknown\nReason: operator status is unavailable or invalid\n" + footer(now)
	}
	freshness, age, usable := snapshotFreshness(snapshot, now)
	if !usable {
		return "Price: unknown\nReason: operator status timestamp is in the future\n" +
			observedFooter(snapshot.ObservedAt, now)
	}
	trigger := snapshot.Result.PriceTrigger
	if trigger == nil {
		return fmt.Sprintf(
			"Price rule: not configured\nFreshness: %s (%ds old)\n%s",
			freshness, age, observedFooter(snapshot.ObservedAt, now),
		)
	}
	report := fmt.Sprintf(
		"Price rule: %s\nTarget: %s\nFreshness: %s (%ds old)",
		triggerDirection(*trigger), formatUSDMicros(trigger.ThresholdMicros), freshness, age,
	)
	if !trigger.Available {
		report += "\nPrice: temporarily unavailable"
	} else {
		report += "\nConservative price: " + formatUSDMicros(trigger.ConservativePrice) +
			"\nCondition: " + triggerState(*trigger) +
			"\nPrice observed: " + trigger.ObservedAt.UTC().Format(time.RFC3339)
		if trigger.ExecutableMinimum != 0 {
			label := "Minimum executable rate"
			if trigger.Direction == pricetrigger.BuyAtOrBelow {
				label = "Maximum executable rate"
			}
			report += "\n" + label + ": " + formatMicroUnits(trigger.ExecutableMinimum) +
				" devUSDC/SOL"
		}
	}
	return report + "\n" + observedFooter(snapshot.ObservedAt, now)
}

func triggerState(trigger pricetrigger.Status) string {
	if !trigger.Available {
		return "unavailable"
	}
	if !trigger.ConditionMet {
		return "waiting"
	}
	if trigger.ExecutableMinimum == 0 {
		return "target reached; quote not checked"
	}
	if trigger.ExecutableCondition {
		return "ready"
	}
	if trigger.Direction == pricetrigger.BuyAtOrBelow {
		return "quote above limit"
	}
	return "quote below target"
}

func triggerDirection(trigger pricetrigger.Status) string {
	if trigger.Direction == pricetrigger.BuyAtOrBelow {
		return "buy SOL at or below"
	}
	return "sell SOL at or above"
}

func formatUSDMicros(value uint64) string {
	return "$" + formatMicroUnits(value)
}

func formatMicroUnits(value uint64) string {
	whole := value / 1_000_000
	fraction := fmt.Sprintf("%06d", value%1_000_000)
	fraction = strings.TrimRight(fraction, "0")
	for len(fraction) < 2 {
		fraction += "0"
	}
	return fmt.Sprintf("%d.%s", whole, fraction)
}

func (s *Service) lastTrade(now time.Time) string {
	snapshot, err := s.status.Read()
	if err != nil {
		return "Last trade: unknown\nReason: operator status is unavailable or invalid\n" + footer(now)
	}
	freshness, age, usable := snapshotFreshness(snapshot, now)
	if !usable {
		return "Last trade: unknown\nReason: operator status timestamp is in the future\n" +
			observedFooter(snapshot.ObservedAt, now)
	}
	action := snapshot.LastAction
	if action.Result.ActionID == "" && snapshot.Result.ActionID != "" {
		action = operatorstatus.Action{ObservedAt: snapshot.ObservedAt, Result: snapshot.Result}
	}
	if action.Result.ActionID == "" {
		return fmt.Sprintf(
			"Last trade: unknown\nReason: no recorded trade\nFreshness: %s (%ds old)\n%s",
			freshness, age, observedFooter(snapshot.ObservedAt, now),
		)
	}
	result := action.Result
	report := fmt.Sprintf(
		"Last trade: %s\nFreshness: %s (%ds old)\nSubmitted: %s",
		safeField(result.Decision, 32), freshness, age, yesNo(result.Submitted),
	)
	if result.Verdict != "" {
		report += "\nOutcome: " + safeField(describeVerdict(result.Verdict), 64)
	}
	if result.InputAmount != 0 && result.InputAsset != "" {
		report += "\nInput: " + operatorstatus.FormatAmount(result.InputAmount, result.InputAsset)
	} else if result.AmountLamports != 0 {
		report += "\nInput: " + operatorstatus.FormatAmount(result.AmountLamports, "SOL")
	}
	if result.MinimumOutput != 0 || result.OutputAmount != 0 {
		report += "\nOutput: " + operatorstatus.FormatAmount(result.OutputAmount, result.OutputAsset) +
			" (minimum " + operatorstatus.FormatAmount(result.MinimumOutput, result.OutputAsset) + ")"
	}
	if result.Signature != "" {
		report += "\nSignature: " + safeField(result.Signature, 128)
		if snapshot.Cluster == "devnet" {
			report += "\nExplorer: https://explorer.solana.com/tx/" +
				safeField(result.Signature, 128) + "?cluster=devnet"
		}
	}
	report += "\nTrade observed: " + action.ObservedAt.UTC().Format(time.RFC3339) +
		"\n" + observedFooter(snapshot.ObservedAt, now)
	return report
}

func (s *Service) explain(ctx context.Context, question string, now time.Time) string {
	if s.explainer == nil {
		return "Explanation: unavailable\nReason: no explanation provider is configured\n" + footer(now)
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return "Usage: /explain QUESTION\n" + footer(now)
	}
	if len(question) > maxQuestionBytes || !utf8.ValidString(question) {
		return "Explanation: rejected\nReason: question is invalid or too long\n" + footer(now)
	}
	statusText, snapshot, usable, stale := s.statusReport(now)
	if !usable {
		return "Explanation: unavailable\nReason: deterministic operator status is unknown\n" + footer(now)
	}
	if err := s.explanationBudget.Reserve(now); err != nil {
		if errors.Is(err, errExplanationBudgetExhausted) {
			return "Explanation: unavailable\nReason: daily explanation request budget is exhausted\n" + footer(now)
		}
		return "Explanation: unavailable\nReason: explanation request budget is unavailable\n" + footer(now)
	}
	type result struct {
		text string
		err  error
	}
	resultChannel := make(chan result, 1)
	explainCtx, cancel := context.WithTimeout(ctx, s.explainTimeout)
	defer cancel()
	request := ExplanationRequest{
		Question: question, StatusText: bounded(statusText),
		ObservedAt: snapshot.ObservedAt.UTC(), Stale: stale,
	}
	go func() {
		var response result
		defer func() {
			if recover() != nil {
				response = result{err: errors.New("explanation provider failed")}
			}
			resultChannel <- response
		}()
		response.text, response.err = s.explainer.Explain(explainCtx, request)
	}()
	select {
	case <-explainCtx.Done():
		return "Explanation: unavailable\nReason: explanation provider timed out\n" + footer(now)
	case response := <-resultChannel:
		if response.err != nil || strings.TrimSpace(response.text) == "" {
			return "Explanation: unavailable\nReason: explanation provider failed\n" + footer(now)
		}
		text := safeField(response.text, maxExplanationBytes)
		return "Explanation (optional, not authoritative):\n" + text +
			"\nVerify with /status. This process cannot take action.\n" +
			observedFooter(snapshot.ObservedAt, now)
	}
}

func ParseAllowedChatIDs(value string) ([]int64, error) {
	parts := strings.Split(value, ",")
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("Telegram chat ID allowlist contains an empty value")
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id == 0 {
			return nil, errors.New("Telegram chat IDs must be nonzero signed decimal integers")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("Telegram chat ID allowlist contains a duplicate")
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("Telegram chat ID allowlist is empty")
	}
	return ids, nil
}

func parseCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	parts := strings.Fields(text)
	command := strings.ToLower(parts[0])
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	argument := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
	return command, argument
}

func snapshotFreshness(snapshot operatorstatus.Snapshot, now time.Time) (string, uint64, bool) {
	observed := snapshot.ObservedAt.UTC()
	if observed.After(now.Add(5 * time.Second)) {
		return "unknown", 0, false
	}
	age := now.Sub(observed)
	if age < 0 {
		age = 0
	}
	seconds := uint64(age / time.Second)
	if age > operatorstatus.StaleAfter {
		return "stale", seconds, true
	}
	return "recent", seconds, true
}

func footer(now time.Time) string {
	return "Source: " + statusSource + "\nChecked: " + now.UTC().Format(time.RFC3339)
}

func observedFooter(observedAt, now time.Time) string {
	return "Source: " + statusSource + "\nObserved: " + observedAt.UTC().Format(time.RFC3339) +
		"\nChecked: " + now.UTC().Format(time.RFC3339)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func safeField(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return truncateUTF8(value, limit)
}

func bounded(value string) string {
	return truncateUTF8(strings.ToValidUTF8(value, "�"), maxOutputBytes)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= len("…") {
		return strings.Repeat(".", limit)
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}
