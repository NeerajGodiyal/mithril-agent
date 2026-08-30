// Package telegramoperator exposes the bounded, read-only operator status over
// a deliberately small Telegram command surface.
package telegramoperator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	BotTokenEnvironment   = "MITHRIL_AGENT_TELEGRAM_BOT_TOKEN"
	AllowedIDsEnvironment = "MITHRIL_AGENT_TELEGRAM_CHAT_IDS"

	maxInputBytes          = 1024
	maxQuestionBytes       = 512
	maxExplanationBytes    = 1600
	maxOutputBytes         = 3500
	defaultPollTimeout     = 25 * time.Second
	defaultMinInterval     = time.Second
	defaultExplainTime     = 5 * time.Second
	statusSource           = "local bounded operator status"
	maxPaperSources        = 4
	maxPaperAlertChats     = 8
	maxPaperAnnounced      = 2304
	maxPaperAnnouncedBytes = 192 << 10
	maxPaperReportBytes    = 800
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

// PaperStatusReader returns only the bounded simulation-event projection. It
// cannot read a journal, select a strategy, sign, or submit.
type PaperStatusReader interface {
	Read() (paperstatus.Snapshot, error)
	SourceID() string
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
	Bot     Bot
	Cursor  Cursor
	Sources []StatusReader
	// PaperSources are optional, read-only simulation alert sockets.
	PaperSources []PaperStatusReader
	// AnnouncedPath persists which actions have been announced so a restart
	// does not repeat them. Empty keeps dedup in memory only.
	AnnouncedPath     string
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
const (
	// maxConsecutivePollFailures bounds how long a broken configuration hides
	// behind retries. Ten failures spaced by the delay below is roughly a
	// minute of outage absorbed, which covers a restart of the far side without
	// masking a revoked token for long.
	maxConsecutivePollFailures = 10
	defaultPollRetryDelay      = 5 * time.Second
)

type Service struct {
	bot               Bot
	cursor            Cursor
	sources           []StatusReader
	paperSources      []PaperStatusReader
	allowed           map[int64]struct{}
	explainer         Explainer
	explanationBudget ExplanationBudget
	now               func() time.Time
	pollTimeout       time.Duration
	minInterval       time.Duration
	explainTimeout    time.Duration
	// pollRetryDelay spaces out retries after a failed poll. A field rather
	// than a constant only so tests need not sleep.
	pollRetryDelay time.Duration

	rateMu  sync.Mutex
	next    map[int64]time.Time
	running atomic.Bool

	// announcedAction is the last action already reported unprompted. It is
	// seeded from whatever is current when the consumer starts, so a restart
	// re-announces nothing: an operator who has already been told about a trade
	// should not hear about it again because the process bounced.
	announcedAction map[string]string
	announceSeeded  map[string]bool
	// The independent stores keep a busy live strategy from evicting paper
	// delivery IDs that are still present in a bounded paper snapshot.
	announced       *announcedStore
	paperAnnounced  *announcedStore
	paperHealthSeen map[string]bool
	paperHealthy    map[string]bool
}

func New(config Config) (*Service, error) {
	if config.Bot == nil || config.Cursor == nil || len(config.Sources) == 0 {
		return nil, errors.New("Telegram bot, cursor, and operator status reader are required")
	}
	paperSourceIDs := make(map[string]struct{}, len(config.PaperSources))
	paperSourceLabels := make(map[string]struct{}, len(config.PaperSources))
	for _, source := range config.PaperSources {
		if source == nil {
			return nil, errors.New("paper status readers must not be nil")
		}
		identity := source.SourceID()
		if identity == "" || len(identity) > 512 {
			return nil, errors.New("paper status readers need a bounded stable identity")
		}
		if _, duplicate := paperSourceIDs[identity]; duplicate {
			return nil, errors.New("paper status reader identities must be unique")
		}
		paperSourceIDs[identity] = struct{}{}
		label := paperReaderLabel(source)
		if _, duplicate := paperSourceLabels[label]; duplicate {
			return nil, errors.New("paper status reader identities have colliding display tags")
		}
		paperSourceLabels[label] = struct{}{}
	}
	if len(config.PaperSources) > maxPaperSources {
		return nil, fmt.Errorf("at most %d paper status readers are supported", maxPaperSources)
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
	if len(config.PaperSources) != 0 && len(allowed) > maxPaperAlertChats {
		return nil, fmt.Errorf("paper alerts support at most %d Telegram chats", maxPaperAlertChats)
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
	paperAnnouncedPath := ""
	if config.AnnouncedPath != "" {
		paperAnnouncedPath = filepath.Join(
			filepath.Dir(config.AnnouncedPath), "announced-paper-events.json",
		)
	}
	return &Service{
		bot: config.Bot, cursor: config.Cursor, sources: config.Sources,
		paperSources: config.PaperSources, allowed: allowed,
		announcedAction: map[string]string{}, announceSeeded: map[string]bool{},
		announced: loadAnnouncedStore(config.AnnouncedPath),
		paperAnnounced: loadBoundedAnnouncedStore(
			paperAnnouncedPath, maxPaperAnnounced, maxPaperAnnouncedBytes,
		),
		paperHealthSeen: map[string]bool{}, paperHealthy: map[string]bool{},
		explainer: config.Explainer, explanationBudget: config.ExplanationBudget, now: config.Now,
		pollTimeout: config.PollTimeout, minInterval: config.MinimumInterval,
		pollRetryDelay: defaultPollRetryDelay,
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
	pollFailures := 0
	for {
		updates, err := s.bot.Poll(ctx, offset, s.pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A read-only observer must survive a network blip. Exiting on the
			// FIRST failure produced seven crash-loops in one night, each from a
			// transient error, with systemd papering over them — and a unit that
			// stops trying altogether once its start limit is reached. Alerts
			// then go missing with no signal that anything is wrong.
			pollFailures++
			log.Printf("telegram: poll failed (%d/%d): %v",
				pollFailures, maxConsecutivePollFailures, err)
			if pollFailures >= maxConsecutivePollFailures {
				// Still give up eventually: a bad token or a revoked bot must
				// surface as a failed unit, not as a process retrying in silence.
				return errors.New("poll Telegram updates")
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(s.pollRetryDelay):
			}
			continue
		}
		pollFailures = 0
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
					// A PERMANENT rejection must not stall the cursor. Returning
					// here left the update unconsumed, so Telegram redelivered it,
					// the process failed identically, and systemd looped it — and
					// because announce() runs after this loop, every other chat
					// lost its trade alerts too. A group migrating to a supergroup
					// does this on its own, with no operator error.
					//
					// Transient failures still stop the batch: retrying is what
					// gets the reply delivered, and the cursor must not skip past
					// an update that a 429 or a 5xx would have answered.
					var status StatusError
					if !errors.As(err, &status) || !permanentSendRejection(status.Status) {
						return errors.New("send Telegram reply")
					}
					log.Printf("telegram: chat %d permanently rejected a reply (HTTP %d); "+
						"skipping that update", update.ChatID, status.Status)
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
		// Poll returns on its own timeout as well as on traffic, so this runs on
		// a regular tick without a second goroutine — the one sequential
		// consumer stays one sequential consumer.
		s.announce(ctx)
	}
}

// announce reports a finished action nobody asked about. It is the only
// unprompted message the operator sends, and it still cannot authorize
// anything: it reads the same bounded status the read-only commands read.
//
// Routine waiting stays quiet. Meaningful paper events are announced; source
// health transitions stay in local logs and /paper reports the current state.
// None of these messages can authorize an action.
func (s *Service) announce(ctx context.Context) {
	for index, source := range s.sources {
		s.announceSource(ctx, index, source)
	}
	for index, source := range s.paperSources {
		s.announcePaperSource(ctx, index, source)
	}
}

func (s *Service) announcePaperSource(ctx context.Context, index int, source PaperStatusReader) {
	sourceID := source.SourceID()
	snapshot, err := source.Read()
	if err != nil || paperstatus.ValidateSnapshot(snapshot) != nil {
		s.recordPaperHealth(index, sourceID, false)
		return
	}
	s.recordPaperHealth(index, sourceID, true)
	events := snapshot.Events
	if gap, ok := paperstatus.TruncationEvent(snapshot); ok {
		events = append([]paperstatus.Event{gap}, events...)
	}
	blocked := make(map[int64]bool)
	for _, event := range events {
		if !paperAnnounceWorthy(event.Kind) {
			continue
		}
		delivered := 0
		for chatID := range s.allowed {
			if blocked[chatID] {
				continue
			}
			deliveryID := paperDeliveryID(sourceID, event.ID, chatID)
			if s.paperAnnounced.announced(deliveryID) {
				continue
			}
			// Version 1 keyed delivery only by list position, which cannot be
			// rebound safely when several sources exist. Migrate the unambiguous
			// one-source case; multi-source upgrades may replay retained alerts
			// once rather than silently suppressing the wrong source.
			if len(s.paperSources) == 1 {
				legacyID := legacyPaperDeliveryID(index, event.ID, chatID)
				if s.paperAnnounced.announced(legacyID) {
					if err := s.paperAnnounced.record(deliveryID); err != nil {
						log.Printf("telegram: %v", err)
					}
					continue
				}
			}
			label := ""
			if len(s.paperSources) > 1 {
				label = paperReaderLabel(source)
			}
			if err := s.bot.Send(ctx, chatID, bounded(paperAnnouncement(event, label))); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("telegram: paper announcement to chat %d failed: %v", chatID, err)
				blocked[chatID] = true
				continue
			}
			delivered++
			if err := s.paperAnnounced.record(deliveryID); err != nil {
				log.Printf("telegram: %v", err)
			}
		}
		if delivered > 0 {
			log.Printf("telegram: announced paper event %s to %d chat(s)", shortActionID(event.ID), delivered)
		}
	}
}

func paperAnnounceWorthy(kind string) bool {
	switch kind {
	case paperstatus.KindStrategyActive, paperstatus.KindStrategyChanged,
		paperstatus.KindOrderFilled, paperstatus.KindRiskHalted,
		paperstatus.KindPeriodClosed, "history_truncated":
		return true
	default:
		return false
	}
}

func paperAnnouncement(event paperstatus.Event, label string) string {
	message := event.Message
	if label != "" {
		if strings.HasPrefix(message, "PAPER ·") {
			message = strings.Replace(message, "PAPER ·", "PAPER · "+label+" ·", 1)
		} else {
			message = strings.Replace(message, "PAPER SIMULATION —", "PAPER SIMULATION · "+label+" ·", 1)
		}
	}
	return timestampPaperMessage(message, event.At)
}

func timestampPaperMessage(message string, at time.Time) string {
	first, rest, found := strings.Cut(message, "\n")
	first += " · " + at.Format("2006-01-02 15:04 UTC")
	if !found {
		return first
	}
	return first + "\n" + rest
}

func paperCurrentAge(message string, fresh bool) string {
	if fresh {
		return message
	}
	_, details, found := strings.Cut(
		strings.Replace(message, "\nToday ", "\nLast result ", 1), "\n",
	)
	if !found {
		return "PAPER · ⚠️ Observer stale"
	}
	return "PAPER · ⚠️ Observer stale\n" + details
}

func paperSnapshotFresh(snapshot paperstatus.Snapshot, now time.Time) bool {
	if snapshot.ObservedAt.After(now.Add(5*time.Second)) ||
		snapshot.ObservedAt.UTC().Format("2006-01-02") != now.UTC().Format("2006-01-02") {
		return false
	}
	staleAfter := 5 * time.Minute
	if snapshot.Summary != nil {
		staleAfter = max(2*time.Minute, 3*time.Duration(snapshot.Summary.TickSeconds)*time.Second)
	}
	return now.Sub(snapshot.ObservedAt) <= staleAfter
}

func (s *Service) recordPaperHealth(index int, sourceID string, healthy bool) {
	seen, previous := s.paperHealthSeen[sourceID], s.paperHealthy[sourceID]
	s.paperHealthSeen[sourceID], s.paperHealthy[sourceID] = true, healthy
	if !healthy && (!seen || previous) {
		log.Printf("telegram: paper status source %d unavailable", index+1)
	}
	if healthy && seen && !previous {
		log.Printf("telegram: paper status source %d available again", index+1)
	}
}

func paperDeliveryID(sourceID, eventID string, chatID int64) string {
	digest := sha256.Sum256([]byte(
		"mithril-agent/paper-telegram-delivery/v2\x00" + sourceID + "\x00" +
			eventID + "\x00" + strconv.FormatInt(chatID, 10),
	))
	return fmt.Sprintf("%x", digest)
}

func legacyPaperDeliveryID(source int, eventID string, chatID int64) string {
	digest := sha256.Sum256([]byte(
		"mithril-agent/paper-telegram-delivery/v1\x00" + strconv.Itoa(source) + "\x00" +
			eventID + "\x00" + strconv.FormatInt(chatID, 10),
	))
	return fmt.Sprintf("%x", digest)
}

func paperSourceLabel(sourceID string) string {
	digest := sha256.Sum256([]byte("mithril-agent/paper-source-label/v1\x00" + sourceID))
	return fmt.Sprintf("SRC %X", digest[:3])
}

func paperReaderLabel(source PaperStatusReader) string {
	if labeled, ok := source.(interface{ SourceLabel() string }); ok {
		if label := labeled.SourceLabel(); label != "" {
			return label
		}
	}
	return paperSourceLabel(source.SourceID())
}

// announceSource reports one leg. The dedup state is per SOURCE, so a restart
// re-announces nothing on any leg and two legs sharing a profile name cannot
// overwrite each other.
func (s *Service) announceSource(ctx context.Context, index int, source StatusReader) {
	snapshot, err := source.Read()
	if err != nil {
		// An unreadable status is already surfaced by /status and by the
		// metrics alerts. Staying quiet here keeps a transient read failure
		// from becoming a message storm.
		return
	}
	// Seed on the FIRST readable status, even when it carries no action yet.
	// Seeding only once a non-empty action appears would treat a fresh setup's
	// very first trade as history and never report it — and an empty status at
	// startup is the ordinary case, because the operator can start before the
	// runner has written anything.
	// Keyed by the SOURCE, not by the profile name: two legs can legitimately
	// carry the same profile — a strategy's sell leg and a standalone agent are
	// both orca_devnet_swap_v1 — and keying on the name made them share one
	// dedup slot, so each overwrote the other and both re-announced every
	// cycle. That is a message every ten seconds, forever.
	leg := strconv.Itoa(index)
	action := announcementAction(snapshot)
	if !s.announceSeeded[leg] {
		// Seed only a SETTLED action. An action still in flight has not been
		// announced yet — announcements fire on settlement — so claiming it as
		// history means its outcome is deduped away and the operator never
		// hears about the trade they started the bot to watch.
		if announceWorthy(action.Result.Decision) {
			s.announcedAction[leg] = announceKey(action.Result)
		}
		s.announceSeeded[leg] = true
		return
	}
	if !announceWorthy(action.Result.Decision) {
		return
	}
	key := announceKey(action.Result)
	if key == s.announcedAction[leg] {
		return
	}
	// The in-memory map above is per-process; this survives a restart. Without
	// it, a restart landing while an action is in flight seeds nothing (seeding
	// takes only settled actions, deliberately), so the new process re-announces
	// that action when it settles. See announced.go.
	if s.announced.announced(action.Result.ActionID) {
		s.announcedAction[leg] = key
		return
	}
	// Render from the snapshot this decision was gated on, never a second read:
	// a fresh read can disagree and put "No trade — agent is idle" in the body
	// of a trade announcement. The report already leads with the outcome, so no
	// prefix is added — prompted and unprompted messages read identically.
	message := bounded(s.tradeReport(snapshot, s.now()))
	// Every allowed chat is attempted even when one of them fails. A blocked
	// bot or a mistyped ID is an ordinary misconfiguration, and it must not
	// silence the chats that do work, nor take the agent down: an announcement
	// is auxiliary, and the metric alerts that carry health do not depend on
	// Telegram being reachable at all.
	delivered := 0
	for chatID := range s.allowed {
		if err := s.bot.Send(ctx, chatID, message); err != nil {
			if ctx.Err() != nil {
				return
			}
			// The WIRE stays silent — replying to unknown chats would let anyone
			// enumerate the allowlist. The LOG must not: with no record at all, a
			// chat that never receives anything is indistinguishable from no trade
			// having happened, which is the failure mode the operator is least
			// able to diagnose and most likely to trust.
			log.Printf("telegram: announcement to chat %d failed: %v", chatID, err)
			continue
		}
		delivered++
	}
	// Record only once somebody has actually been told. If no chat could be
	// reached the announcement stays pending and the next cycle retries it,
	// rather than being marked as delivered to nobody and lost. Recording after
	// a partial delivery is the deliberate trade: the alternative re-sends to
	// the chats that already received it, every cycle, forever.
	if delivered > 0 {
		// Successes were silent while only failures logged, so an empty journal
		// meant either "announced fine" or "never fired" — indistinguishable.
		// That ambiguity produced a confident wrong diagnosis: five real trades
		// were announced, the log showed nothing, and the conclusion was that
		// alerting was broken. One line per announcement, not per chat, so a
		// busy day stays readable.
		log.Printf("telegram: announced %s action %s to %d chat(s)",
			action.Result.Decision, shortActionID(key), delivered)
		s.announcedAction[leg] = key
		// Never blocks: the message is already sent, so a write failure costs a
		// possible duplicate after a restart, not a lost announcement.
		if err := s.announced.record(action.Result.ActionID); err != nil {
			log.Printf("telegram: %v", err)
		}
	}
}

// announcementAction normally reports the durable last action. A refusal can
// fail before an action ID exists, so it cannot become LastAction; in that one
// case the current result is the only operator-visible record of the failure.
func announcementAction(snapshot operatorstatus.Snapshot) operatorstatus.Action {
	if snapshot.Result.ActionID == "" &&
		(snapshot.Result.Decision == "failed" || snapshot.Result.Decision == "halted") {
		return operatorstatus.Action{ObservedAt: snapshot.ObservedAt, Result: snapshot.Result}
	}
	return snapshot.LastAction
}

// announceKey identifies an outcome for deduplication.
//
// A refusal that happens BEFORE an action is minted — an exhausted daily cap, a
// signer rejection, a policy mismatch — settles as failed with no action ID at
// all. Keying on the ID alone dropped those entirely, which made the failures
// most likely to strand an unattended agent the only ones that never reached
// the operator: the runner refused every ten seconds and Telegram stayed quiet.
//
// They cannot dedup on an ID that does not exist, and announcing every cycle is
// a message every ten seconds forever. The outcome and its reason are the
// identity instead, so the operator is told once and told again only when the
// reason itself changes.
func announceKey(result operatorstatus.Result) string {
	if result.ActionID != "" {
		return result.ActionID
	}
	return result.Decision + "/" + result.Reason
}

// shortActionID trims an action ID to something a human can compare across a
// log line and a journal entry without reading 64 hex characters.
func shortActionID(id string) string {
	const shown = 12
	if len(id) <= shown {
		return id
	}
	return id[:shown]
}

// announceWorthy names the outcomes worth interrupting an operator for.
// "canceled" is deliberately absent: a window whose price triggers but never
// clears the executable minimum ends canceled every schedule window while the
// market hovers near the threshold, which is exactly the noise this filter
// exists to prevent.
// permanentSendRejection names the statuses no amount of retrying fixes: the
// chat is gone, the bot was blocked or removed, or the request could never be
// accepted. Everything else — timeouts, 429, 5xx — is worth retrying, so it
// deliberately does NOT appear here.
func permanentSendRejection(status int) bool {
	return status == 400 || status == 403
}

func announceWorthy(decision string) bool {
	switch decision {
	case "complete", "failed", "halted":
		return true
	default:
		return false
	}
}

// Reply returns bounded plain text for one message. false means that the
// update is intentionally ignored (unauthorized chat or local rate limit).
func (s *Service) Reply(ctx context.Context, chatID int64, text string) (string, bool) {
	if _, allowed := s.allowed[chatID]; !allowed {
		// Silent to the sender by design; recorded here because the overwhelmingly
		// common cause is the operator's OWN chat ID being wrong, and from their
		// side that looks like a bot that simply never answers.
		log.Printf("telegram: message from chat %d ignored: not in %s",
			chatID, AllowedIDsEnvironment)
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
	case "/start", "/help":
		return bounded(s.help(now)), true
	case "/status":
		if argument != "" {
			return bounded("Usage: /status\n" + footer(now)), true
		}
		return bounded(s.statusReports(now)), true
	case "/paper":
		if argument != "" {
			return bounded("Usage: /paper"), true
		}
		return bounded(s.paperReports()), true
	case "/last_trade":
		if argument != "" {
			return bounded("Usage: /last_trade\n" + footer(now)), true
		}
		return bounded(s.lastTrades(now)), true
	case "/price":
		if argument != "" {
			return bounded("Usage: /price\n" + footer(now)), true
		}
		return bounded(s.prices(now)), true
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

func (s *Service) help(_ time.Time) string {
	commands := "/help — commands\n/status — live strategy status\n/price — live price rule\n/last_trade — latest live action"
	if len(s.paperSources) > 0 {
		commands += "\n/paper — paper status and today's P&L"
	}
	if s.explainer != nil {
		commands += "\n/explain QUESTION — explain live status"
	}
	return "Mithril operator — read only\n" + commands +
		"\nAlerts: settled live outcomes and meaningful paper events." +
		"\nWaiting updates stay quiet. This bot cannot trade or change settings."
}

func (s *Service) paperReports() string {
	if len(s.paperSources) == 0 {
		return "Paper: not configured"
	}
	reports := make([]string, 0, len(s.paperSources))
	summaries := make([]paperstatus.CurrentSummary, 0, len(s.paperSources))
	now := s.now()
	for _, source := range s.paperSources {
		snapshot, err := source.Read()
		label := ""
		if len(s.paperSources) > 1 {
			label = paperReaderLabel(source)
		}
		if err != nil || paperstatus.ValidateSnapshot(snapshot) != nil {
			report := "PAPER ALERTS · ⚠️ Unavailable"
			if label != "" {
				report = "PAPER ALERTS · " + label + " · ⚠️ Unavailable"
			}
			reports = append(reports, report)
			continue
		}
		fresh := paperSnapshotFresh(snapshot, now)
		if fresh && snapshot.Summary != nil && snapshot.Summary.Day == now.UTC().Format("2006-01-02") {
			summaries = append(summaries, *snapshot.Summary)
		}
		if len(snapshot.Events) == 0 {
			report := paperCurrentAge(snapshot.Current, fresh)
			if report == "" {
				report = "PAPER · No events yet"
			}
			if label != "" {
				report = labelPaperMessage(report, label)
			}
			report = timestampPaperMessage(report, snapshot.ObservedAt)
			reports = append(reports, report)
			continue
		}
		if snapshot.Current != "" {
			report := timestampPaperMessage(
				labelPaperMessage(
					paperCurrentAge(snapshot.Current, fresh), label,
				), snapshot.ObservedAt,
			)
			reports = append(reports, paperReportExcerpt(report))
			continue
		}
		reports = append(reports, paperReportExcerpt(
			paperAnnouncement(snapshot.Events[len(snapshot.Events)-1], label),
		))
	}
	if aggregate := paperPortfolioSummary(summaries); aggregate != "" {
		reports = append([]string{aggregate}, reports...)
	}
	return strings.Join(reports, "\n\n")
}

func paperPortfolioSummary(summaries []paperstatus.CurrentSummary) string {
	if len(summaries) < 2 {
		return ""
	}
	var opening, equity, hold, trades uint64
	paused := 0
	for _, summary := range summaries {
		if summary.OpeningEquityMicros > math.MaxUint64-opening ||
			summary.EquityMicros > math.MaxUint64-equity ||
			summary.HoldBenchmarkMicros > math.MaxUint64-hold ||
			summary.Trades > math.MaxUint64-trades {
			return ""
		}
		opening += summary.OpeningEquityMicros
		equity += summary.EquityMicros
		hold += summary.HoldBenchmarkMicros
		trades += summary.Trades
		if summary.RiskHalted {
			paused++
		}
	}
	if opening > math.MaxInt64 || equity > math.MaxInt64 || hold > math.MaxInt64 {
		return ""
	}
	line := fmt.Sprintf("%d markets · %d trades", len(summaries), trades)
	if paused != 0 {
		line += fmt.Sprintf(" · %d paused", paused)
	}
	return "PAPER · Portfolio\nToday " + signedPaperChange(opening, equity) +
		" · vs hold " + signedPaperChange(hold, equity) + "\n" + line
}

func signedPaperChange(reference, current uint64) string {
	delta := int64(current) - int64(reference)
	sign := "+"
	if delta < 0 {
		sign = "-"
		delta = -delta
	}
	return sign + formatUSDMicros(uint64(delta))
}

func labelPaperMessage(message, label string) string {
	if label == "" {
		return message
	}
	if strings.HasPrefix(message, "PAPER ·") {
		return strings.Replace(message, "PAPER ·", "PAPER · "+label+" ·", 1)
	}
	return message
}

func paperReportExcerpt(message string) string {
	const suffix = "\n…truncated; full alert retained locally"
	if len(message) <= maxPaperReportBytes {
		return message
	}
	return truncateUTF8(message, maxPaperReportBytes-len(suffix)) + suffix
}

func (s *Service) statusReport(now time.Time) (string, operatorstatus.Snapshot, bool, bool) {
	return statusReportFor(s.primary(), now)
}

func statusReportFor(source StatusReader, now time.Time) (string, operatorstatus.Snapshot, bool, bool) {
	snapshot, err := source.Read()
	if err != nil {
		return "Status: unknown\nReason: operator status is unavailable or invalid\n" + footer(now), operatorstatus.Snapshot{}, false, false
	}
	freshness, age, usable := snapshotFreshness(snapshot, now)
	if !usable {
		return "Status: unknown\nReason: operator status timestamp is in the future\n" +
			observedFooter(snapshot.ObservedAt, now), operatorstatus.Snapshot{}, false, false
	}
	attention := operatorstatus.RequiresAttentionForCluster(
		snapshot.Result, snapshot.Control, snapshot.Cluster, now,
	)
	report := fmt.Sprintf(
		"Status: %s\nFreshness: %s (%ds old)\nControl: %s\nAttention required: %s\nSubmitted: %s",
		safeField(describeDecision(snapshot.Result.Decision), 64), freshness, age,
		safeField(snapshot.Control.Mode, 32), yesNo(attention), yesNo(snapshot.Result.Submitted),
	)
	if snapshot.Result.Verdict != "" {
		report += "\nOutcome: " + safeField(describeVerdict(snapshot.Result.Verdict), 64)
	}
	if snapshot.Result.Reason != "" {
		report += "\nReason: " + safeField(describeReason(snapshot.Result.Reason), 160)
	}
	if snapshot.Control.RecoveryPending {
		report += "\nRecovery: Waiting for independent confirmation — do not retry"
	}
	if snapshot.Control.TerminalOutcome != "" {
		report += "\nTerminal stop: " + safeField(
			describeDecision(snapshot.Control.TerminalOutcome), 64,
		)
	}
	if trigger := snapshot.Result.PriceTrigger; trigger != nil {
		report += "\nPrice rule: " + triggerState(*trigger)
	}
	report += "\n" + observedFooter(snapshot.ObservedAt, now)
	return report, snapshot, true, freshness == "stale"
}

func (s *Service) statusReports(now time.Time) string {
	if len(s.sources) == 1 {
		report, _, _, _ := s.statusReport(now)
		return report
	}
	reports := make([]string, 0, len(s.sources))
	for index, source := range s.sources {
		report, snapshot, _, _ := statusReportFor(source, now)
		reports = append(reports, sourceLabel(index, snapshot.Profile)+"\n"+report)
	}
	return strings.Join(reports, "\n\n")
}

func (s *Service) price(now time.Time) string {
	return priceFor(s.primary(), now)
}

func priceFor(source StatusReader, now time.Time) string {
	snapshot, err := source.Read()
	if err != nil {
		return "Price: unknown\nReason: operator status is unavailable or invalid\n" + footer(now)
	}
	return priceSnapshot(snapshot, now)
}

func priceSnapshot(snapshot operatorstatus.Snapshot, now time.Time) string {
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
		// A stopped agent does not read the price at all. The read is bound to
		// a slot it only proves when it is about to act, and proving one every
		// cycle would spawn a node subprocess for a number nobody asked for.
		// Calling that "unavailable" made a deliberately idle agent look broken
		// to the person deciding whether to arm it.
		if snapshot.Result.Decision == "stopped" {
			report += "\nPrice: not being read while no trades are authorised" +
				"\nArm the strategy to start watching it, or run `swap check` for a one-off reading"
		} else {
			report += "\nPrice: temporarily unavailable"
		}
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

func (s *Service) prices(now time.Time) string {
	if len(s.sources) == 1 {
		return s.price(now)
	}
	reports := make([]string, 0, len(s.sources))
	for index, source := range s.sources {
		snapshot, err := source.Read()
		label := sourceLabel(index, snapshot.Profile)
		if err != nil {
			reports = append(reports, label+"\nPrice: unknown\nReason: operator status is unavailable or invalid\n"+footer(now))
			continue
		}
		reports = append(reports, label+"\n"+priceSnapshot(snapshot, now))
	}
	return strings.Join(reports, "\n\n")
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
	snapshot, err := s.primary().Read()
	if err != nil {
		return "Trade status unavailable\nThe operator status could not be read.\n" + footer(now)
	}
	return s.tradeReport(snapshot, now)
}

func (s *Service) lastTrades(now time.Time) string {
	if len(s.sources) == 1 {
		return s.lastTrade(now)
	}
	reports := make([]string, 0, len(s.sources))
	for index, source := range s.sources {
		snapshot, err := source.Read()
		label := sourceLabel(index, snapshot.Profile)
		if err != nil {
			reports = append(reports, label+"\nTrade status unavailable\nThe operator status could not be read.\n"+footer(now))
			continue
		}
		reports = append(reports, label+"\n"+s.tradeReport(snapshot, now))
	}
	return strings.Join(reports, "\n\n")
}

func sourceLabel(index int, profile string) string {
	switch profile {
	case orcaswap.ProfileName:
		return "Sell"
	case orcaswap.BuyProfileName:
		return "Buy"
	case agent.ProfileTreasurySweepV1:
		return "Sweep"
	default:
		return fmt.Sprintf("Setup %d", index+1)
	}
}

// tradeReport renders one snapshot. announce() gates on a snapshot and must
// render that same one; reading again between the decision and the text lets
// the two disagree.
func (s *Service) tradeReport(snapshot operatorstatus.Snapshot, now time.Time) string {
	freshness, age, usable := snapshotFreshness(snapshot, now)
	if !usable {
		return "Trade status unavailable\nThe status timestamp is in the future.\n" +
			observedFooter(snapshot.ObservedAt, now)
	}
	action := snapshot.LastAction
	if action.Result.ActionID == "" && (snapshot.Result.ActionID != "" ||
		snapshot.Result.Decision == "failed" || snapshot.Result.Decision == "halted") {
		action = operatorstatus.Action{ObservedAt: snapshot.ObservedAt, Result: snapshot.Result}
	}
	if action.Result.ActionID == "" {
		if action.Result.Decision == "failed" || action.Result.Decision == "halted" {
			report := "Agent could not start the action — nothing left your wallet."
			if reason := safeField(describeReason(action.Result.Reason), 160); reason != "" {
				report += "\n" + reason
			}
			return report + fmt.Sprintf(
				"\n\n%s, checked %ds ago\n%s",
				freshness, age, observedFooter(snapshot.ObservedAt, now),
			)
		}
		return fmt.Sprintf(
			"No trades yet\nThis setup has not made a trade.\n%s, checked %ds ago\n%s",
			freshness, age, observedFooter(snapshot.ObservedAt, now),
		)
	}
	result := action.Result
	// Outcome first, then the two numbers that answer "what did it do", then
	// one link that proves it. A reader on a phone should not have to parse a
	// field list to learn whether their money moved.
	kind := kindOf(result)
	report := tradeHeadline(kind, result.Decision, result.Verdict, result.Submitted)
	if reason := safeField(describeReason(result.Reason), 160); reason != "" && result.Decision != "complete" {
		report += "\n" + reason
	}
	// Whether the setup is actually stuck is a property of the control state,
	// not of this one outcome. Reading the latch means the sentence is right
	// for a setup locked by an earlier action and absent for a halt that left
	// the setup free to keep going.
	if snapshot.Control.TerminalActionID != "" {
		report += "\nThis setup is locked and needs you."
	}
	report += "\n"
	if result.InputAmount != 0 && result.InputAsset != "" {
		report += "\n" + amountLine(kind.spentLabel(), result.InputAmount, result.InputAsset)
	} else if result.AmountLamports != 0 {
		report += "\n" + amountLine("Sent", result.AmountLamports, "SOL")
	}
	if result.OutputAmount != 0 {
		report += "\n" + amountLine("Received", result.OutputAmount, result.OutputAsset)
	} else if result.MinimumOutput != 0 {
		report += "\n" + amountLine("At least", result.MinimumOutput, result.OutputAsset)
	}
	// The signature itself is 88 characters of noise once a link carries it.
	// Only devnet gets a link because that is the only cluster this agent
	// trades on; naming the cluster keeps a mainnet reader from assuming one.
	if result.Signature != "" && snapshot.Cluster == "devnet" {
		report += "\n\nhttps://explorer.solana.com/tx/" +
			safeField(result.Signature, 128) + "?cluster=devnet"
	} else if result.Signature != "" {
		report += "\n\nSignature " + safeField(result.Signature, 128)
	}
	report += "\n\n" + safeField(snapshot.Cluster, 16) +
		fmt.Sprintf(" · %s, checked %ds ago", freshness, age) +
		"\n" + observedFooter(snapshot.ObservedAt, now)
	return report
}

// amountLine pads the label so the numbers line up in a monospace-ish column,
// which is what makes a two-line trade readable at a glance.
func amountLine(label string, amount uint64, asset string) string {
	return fmt.Sprintf("%-9s %s", label, operatorstatus.FormatAmount(amount, asset))
}

// tradeHeadline states the outcome in the reader's terms. The decision and
// verdict are internal vocabulary; "complete/finalized" means the money moved
// and the chain agrees, and that is what the sentence should say.
// actionKind names what the agent actually did, in the words the operator
// thinks in. Every outcome used to be called a "trade": a BUY read as
// "Sold 0.10 devUSDC", which is backwards, and a sweep to the operator's own
// wallet read as "Trade complete", which is not a trade at all.
//
// The three are distinguishable from the result itself: a sweep moves lamports
// and names no input asset, a buy ends holding SOL, everything else is a sell.
type actionKind uint8

const (
	kindSell actionKind = iota
	kindBuy
	kindSweep
)

func kindOf(result operatorstatus.Result) actionKind {
	if result.InputAsset == "" && result.AmountLamports != 0 {
		return kindSweep
	}
	if result.OutputAsset == "SOL" {
		return kindBuy
	}
	return kindSell
}

// did is the past-tense verb phrase for a completed action.
func (k actionKind) did() string {
	switch k {
	case kindBuy:
		return "Bought SOL"
	case kindSweep:
		return "Sent to your wallet"
	default:
		return "Sold SOL"
	}
}

// attempted names the action for outcomes where nothing completed, so a
// canceled buy does not read as a canceled sale.
func (k actionKind) attempted() string {
	switch k {
	case kindBuy:
		return "Buy"
	case kindSweep:
		return "Transfer"
	default:
		return "Sale"
	}
}

// spentLabel is what left the wallet, which differs by direction: a sale gives
// up SOL, a buy spends dollars, a sweep sends SOL out.
func (k actionKind) spentLabel() string {
	switch k {
	case kindBuy:
		return "Spent"
	case kindSweep:
		return "Sent"
	default:
		return "Sold"
	}
}

func tradeHeadline(kind actionKind, decision, verdict string, submitted bool) string {
	switch decision {
	case "complete":
		if verdict == "finalized" {
			return kind.did() + " — confirmed on-chain"
		}
		return kind.did()
	case "canceled":
		return kind.attempted() + " canceled before it was sent"
	case "halted":
		// Only a broadcast transaction can have an unresolved on-chain
		// outcome. A halt before submission left the wallet untouched, and
		// since such a halt no longer latches the control state, calling it
		// "locked" told the operator to go fix something that was not stuck.
		if submitted || verdict != "" {
			return kind.attempted() + " halted — it was sent and the outcome is unconfirmed"
		}
		return kind.attempted() + " halted before it was sent — nothing left your wallet"
	case "failed":
		return kind.attempted() + " failed"
	case "waiting", "stopped", "degraded":
		return "No trade — agent is idle"
	default:
		return kind.attempted() + " " + safeField(decision, 32)
	}
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
	if strings.TrimSpace(value) == "" {
		return nil, errors.New(AllowedIDsEnvironment +
			" is not set; run `mithril-agent-telegram link` to find your chat ID")
	}
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

// primary is the first configured source. A single-leg deployment and the
// optional explanation provider keep the exact original report shape.
func (s *Service) primary() StatusReader { return s.sources[0] }
