package telegramoperator

import (
	"bytes"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
)

func announceService(t *testing.T, status *statusStub, bot *botStub, chats ...int64) *Service {
	t.Helper()
	if len(chats) == 0 {
		chats = []int64{11}
	}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{status},
		AllowedChatIDs: chats,
		Now:            func() time.Time { return status.snapshot.ObservedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// Whatever is already current when the consumer starts is history. Announcing
// it would mean a restart re-reports a trade the operator has already seen,
// and a crashloop would report it repeatedly.
func TestAnnounceSeedsSilentlyOnFirstPass(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{}
	service := announceService(t, status, bot)

	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatalf("startup announced %d messages, want 0: %+v", len(bot.sent), bot.sent)
	}
}

// A settled action nobody asked about is the whole point: the operator learns a
// trade happened without having to poll for it, and the message carries the
// explorer link so the claim can be checked against the chain.
func TestAnnounceReportsNewSettledActionWithExplorerLink(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{}
	service := announceService(t, status, bot, 11, 22)
	service.announce(t.Context())

	next := testSnapshot(now)
	next.LastAction.Result.ActionID = "second-action"
	next.LastAction.Result.Signature = "second-signature"
	status.snapshot = next
	service.announce(t.Context())

	if len(bot.sent) != 2 {
		t.Fatalf("announced to %d chats, want 2 (one per allowed chat)", len(bot.sent))
	}
	for _, sent := range bot.sent {
		// The fixture moves lamports and names no input asset, so it is a
		// transfer to the operator's own wallet — not a "trade".
		if !strings.Contains(sent.Text, "Sent to your wallet") {
			t.Errorf("message lacks a headline: %q", sent.Text)
		}
		if !strings.Contains(sent.Text, "explorer.solana.com/tx/second-signature") {
			t.Errorf("message lacks the explorer link: %q", sent.Text)
		}
	}

	// Re-announcing the same action would turn a useful channel into noise.
	before := len(bot.sent)
	service.announce(t.Context())
	if len(bot.sent) != before {
		t.Errorf("the same action was announced twice: %+v", bot.sent[before:])
	}
}

// Waiting and stopped are what a healthy agent with nothing to do looks like.
// Narrating them trains the operator to ignore the channel.
func TestAnnounceStaysQuietForRoutineDecisions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{}
	service := announceService(t, status, bot)
	service.announce(t.Context())

	// "canceled" is here on purpose and is the one that is easy to lose: a
	// window whose price triggers but never clears the executable minimum ends
	// canceled EVERY schedule window while the market hovers near the
	// threshold, which is precisely the noise the filter exists to prevent.
	for _, decision := range []string{
		"waiting", "stopped", "degraded", "pending", "executing", "canceled",
	} {
		next := testSnapshot(now)
		next.LastAction = operatorstatus.Action{
			ObservedAt: now.Add(-time.Second),
			Result: execution.Result{
				ActionID: "action-" + decision, Decision: decision,
			},
		}
		status.snapshot = next
		service.announce(t.Context())
	}
	if len(bot.sent) != 0 {
		t.Fatalf("routine decisions announced %d messages, want 0: %+v", len(bot.sent), bot.sent)
	}
}

// The two halts read very differently to the person holding the phone. A halt
// after submission leaves money in flight and latches the setup; a halt before
// submission left the wallet untouched and no longer latches anything. Telling
// the second one it is locked sends the operator to fix nothing.
func TestAnnounceDistinguishesTheTwoHalts(t *testing.T) {
	for _, test := range []struct {
		name           string
		submitted      bool
		latched        string
		wantContains   string
		wantNotContain string
	}{
		{
			name: "sent and unresolved", submitted: true, latched: "halted-action",
			wantContains: "needs you",
		},
		{
			name: "never sent", submitted: false, latched: "",
			wantContains: "nothing left your wallet", wantNotContain: "needs you",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			status := &statusStub{snapshot: testSnapshot(now)}
			bot := &botStub{}
			service := announceService(t, status, bot)
			service.announce(t.Context())

			next := testSnapshot(now)
			next.LastAction.Result.ActionID = "halted-action"
			next.LastAction.Result.Decision = "halted"
			next.LastAction.Result.Submitted = test.submitted
			next.Control.TerminalActionID = test.latched
			if !test.submitted {
				// A quarantined transaction was never broadcast, so it can have
				// neither a signature on the chain nor a reconciliation verdict.
				next.LastAction.Result.Verdict = ""
				next.LastAction.Result.Signature = ""
			}
			status.snapshot = next
			service.announce(t.Context())
			if len(bot.sent) != 1 {
				t.Fatalf("halted announced %d messages, want 1", len(bot.sent))
			}
			text := bot.sent[0].Text
			if !strings.Contains(text, test.wantContains) {
				t.Errorf("halted message missing %q: %q", test.wantContains, text)
			}
			if test.wantNotContain != "" && strings.Contains(text, test.wantNotContain) {
				t.Errorf("unsent halt claimed %q: %q", test.wantNotContain, text)
			}
		})
	}
}

// An unreadable status is already covered by /status and the metric alerts.
// Turning every transient read failure into a message would be a storm.
func TestAnnounceStaysQuietWhenStatusIsUnreadable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{}
	service := announceService(t, status, bot)
	service.announce(t.Context())

	status.err = errors.New("private path and endpoint must not escape")
	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatalf("unreadable status announced %d messages, want 0", len(bot.sent))
	}
}

// The operator can start the bot at any moment, including while a trade is
// already in flight. Seeding unconditionally on whatever is current takes that
// in-flight action as history, so when it settles the dedup suppresses it and
// the operator never hears about the very trade they were waiting on.
//
// Only a settled action is history. An unsettled one has not been reported yet
// by definition, because announcements only fire for settled outcomes.
func TestAnnounceDoesNotSwallowATradeInFlightAtStartup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	inFlight := testSnapshot(now)
	inFlight.LastAction.Result.ActionID = "in-flight"
	inFlight.LastAction.Result.Decision = "pending"
	inFlight.LastAction.Result.Signature = ""
	inFlight.LastAction.Result.Verdict = ""

	status := &statusStub{snapshot: inFlight}
	bot := &botStub{}
	service := announceService(t, status, bot)
	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatalf("an unsettled action was announced: %+v", bot.sent)
	}

	settled := testSnapshot(now)
	settled.LastAction.Result.ActionID = "in-flight" // same action, now resolved
	settled.LastAction.Result.Decision = "complete"
	status.snapshot = settled
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("the trade that settled after startup was never announced: %+v", bot.sent)
	}
}

// A blocked bot, a mistyped ID, or a group the bot was removed from must not
// take the agent down, and must not stop the chats that ARE reachable from
// being told. Announcements are auxiliary; the metrics alerts carry health and
// do not depend on Telegram being reachable.
func TestOneUnreachableChatNeitherStopsDeliveryNorTheProcess(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{unreachable: 22}
	service := announceService(t, status, bot, 11, 22, 33)
	service.announce(t.Context())

	next := testSnapshot(now)
	next.LastAction.Result.ActionID = "settled-action"
	status.snapshot = next
	service.announce(t.Context())
	if len(bot.sent) != 2 {
		t.Fatalf("reachable chats received %d of 2 messages: %+v", len(bot.sent), bot.sent)
	}
	if !strings.Contains(logs.String(), "to 2 chat(s)") || strings.Contains(logs.String(), "to 3 chat(s)") {
		t.Fatalf("delivery log reported the allowlist rather than successful sends: %s", logs.String())
	}
}

// When no chat could be reached the announcement is not lost: it stays
// unrecorded so the next cycle retries it, rather than being marked delivered
// to nobody.
func TestATotallyUndeliverableAnnouncementIsRetried(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{unreachable: 11}
	service := announceService(t, status, bot, 11)
	service.announce(t.Context())

	next := testSnapshot(now)
	next.LastAction.Result.ActionID = "settled-action"
	status.snapshot = next
	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatal("unreachable chat somehow received a message")
	}

	// The chat comes back; the pending announcement must still arrive.
	bot.unreachable = 0
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("the announcement was dropped instead of retried: %+v", bot.sent)
	}
}

// One bot token permits one long-poller, so one process reads every leg. The
// dedup state must be PER LEG: a single shared cursor lets whichever leg seeds
// first silence the others, and a restart then re-announces the rest.
func TestAnnounceReportsEveryLegAndDedupesEachSeparately(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	legs := map[string]string{
		"orca_devnet_swap_v1": "sell-action",
		"orca_devnet_buy_v2":  "buy-action",
		"treasury_sweep_v1":   "sweep-action",
	}
	var sources []StatusReader
	for profile, actionID := range legs {
		snapshot := testSnapshot(now)
		snapshot.Profile = profile
		snapshot.LastAction = operatorstatus.Action{
			ObservedAt: now.Add(-time.Second),
			Result:     execution.Result{ActionID: actionID, Decision: "complete"},
		}
		sources = append(sources, &statusStub{snapshot: snapshot})
	}
	bot := &botStub{}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: sources,
		AllowedChatIDs: []int64{7}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	// First pass seeds every leg from settled history and must stay silent.
	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatalf("seeding announced %d messages: %+v", len(bot.sent), bot.sent)
	}
	// A restart re-seeds; still silent, on every leg, not just the first.
	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatalf("a second pass re-announced settled history: %+v", bot.sent)
	}

	// A new action on EACH leg must be reported — three legs, three messages.
	for index, source := range sources {
		stub := source.(*statusStub)
		next := stub.snapshot
		next.LastAction.Result.ActionID = "fresh-" + next.Profile
		stub.snapshot = next
		service.announce(t.Context())
		if len(bot.sent) != index+1 {
			t.Fatalf("after leg %d the bot had sent %d message(s), want %d",
				index+1, len(bot.sent), index+1)
		}
	}
}

// A leg whose status cannot be read must not silence the legs that can.
func TestAnUnreadableLegDoesNotSilenceTheOthers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	working := testSnapshot(now)
	working.Profile = "orca_devnet_swap_v1"
	working.LastAction = operatorstatus.Action{
		ObservedAt: now.Add(-time.Second),
		Result:     execution.Result{ActionID: "sell-1", Decision: "complete"},
	}
	broken := &statusStub{err: errors.New("socket is gone")}
	stub := &statusStub{snapshot: working}
	bot := &botStub{}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{broken, stub},
		AllowedChatIDs: []int64{7}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	next := stub.snapshot
	next.LastAction.Result.ActionID = "sell-2"
	stub.snapshot = next
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("the readable leg sent %d message(s), want 1: %+v", len(bot.sent), bot.sent)
	}
}

// TWO SOURCES CAN CARRY THE SAME PROFILE NAME: a strategy's sell leg and a
// standalone agent are both orca_devnet_swap_v1. Keying the dedup on the name
// made them share one slot, so each overwrote the other and both re-announced
// every single cycle — a message every ten seconds, forever. This is the spam
// that reached a real phone.
func TestTwoSourcesSharingAProfileNameDoNotSpam(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	build := func(actionID string) *statusStub {
		snapshot := testSnapshot(now)
		snapshot.Profile = "orca_devnet_swap_v1" // identical on purpose
		snapshot.LastAction = operatorstatus.Action{
			ObservedAt: now.Add(-time.Second),
			Result:     execution.Result{ActionID: actionID, Decision: "complete"},
		}
		return &statusStub{snapshot: snapshot}
	}
	bot := &botStub{}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{},
		Sources:        []StatusReader{build("agent-action"), build("strategy-action")},
		AllowedChatIDs: []int64{7}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Seed, then run many cycles with nothing new. A correct dedup says nothing.
	for i := 0; i < 25; i++ {
		service.announce(t.Context())
	}
	if len(bot.sent) != 0 {
		t.Fatalf("two sources sharing a profile name produced %d messages: %+v",
			len(bot.sent), bot.sent)
	}
}

// Only failures were logged, so an empty journal meant either "announced fine"
// or "never fired" — indistinguishable. That ambiguity produced a confident
// wrong diagnosis on a real host: five trades WERE announced, `journalctl`
// showed nothing since startup, and the conclusion was that alerting had not
// fired at all.
//
// Silence has to mean nothing happened.
func TestADeliveredAnnouncementLeavesATraceInTheLog(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previous); log.SetFlags(previousFlags) })

	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{}
	service := announceService(t, status, bot, 11, 22)
	service.announce(t.Context()) // seeds silently

	next := testSnapshot(now)
	next.LastAction.Result.ActionID = "abcdef0123456789abcdef"
	next.LastAction.Result.Signature = "second-signature"
	status.snapshot = next
	service.announce(t.Context())

	if len(bot.sent) == 0 {
		t.Fatal("nothing was announced, so this test proves nothing")
	}
	line := logged.String()
	if !strings.Contains(line, "telegram: announced") {
		t.Fatalf("a delivered announcement left no trace:\n%s", line)
	}
	// The action ID ties the log line to the journal entry and the chain.
	if !strings.Contains(line, "abcdef012345") {
		t.Errorf("the log line does not identify which action: %q", line)
	}
	// One line per announcement, not per chat: two chats received this.
	if strings.Count(line, "telegram: announced") != 1 {
		t.Errorf("logged once per chat rather than once per announcement: %q", line)
	}
}

// A refusal that happens before an action is minted — an exhausted daily cap, a
// signer rejection — settles as failed with no action ID at all. Keying dedup on
// the ID alone dropped exactly those, so the failures most likely to strand an
// unattended agent were the only ones the operator never heard about: the runner
// refused every ten seconds and Telegram stayed silent.
func TestAnnounceReportsAFailureThatNeverGotAnActionID(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	status := &statusStub{snapshot: testSnapshot(now)}
	bot := &botStub{}
	service := announceService(t, status, bot)
	service.announce(t.Context())

	refused := testSnapshot(now)
	refused.LastAction.Result = operatorstatus.Result{Decision: "failed", Reason: "signer_refused"}
	status.snapshot = refused
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("a signer refusal produced %d messages, want 1: %+v", len(bot.sent), bot.sent)
	}

	// It cannot dedup on an ID that does not exist, and the runner republishes
	// the same refusal every cycle. Repeating it is a message every ten seconds
	// forever, which trains the operator to mute the bot.
	service.announce(t.Context())
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("the same refusal was repeated: %+v", bot.sent)
	}

	// A DIFFERENT reason is different news and must get through.
	changed := testSnapshot(now)
	changed.LastAction.Result = operatorstatus.Result{Decision: "failed", Reason: "quote_unavailable"}
	status.snapshot = changed
	service.announce(t.Context())
	if len(bot.sent) != 2 {
		t.Fatalf("a new failure reason was suppressed: %+v", bot.sent)
	}
}

// A refusal already standing when the consumer starts is history like any other
// settled outcome: a restart must not re-report it.
func TestAnnounceSeedsAnIDLessFailureAsHistory(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	standing := testSnapshot(now)
	standing.LastAction.Result = operatorstatus.Result{Decision: "failed", Reason: "signer_refused"}
	status := &statusStub{snapshot: standing}
	bot := &botStub{}
	service := announceService(t, status, bot)

	service.announce(t.Context())
	service.announce(t.Context())
	if len(bot.sent) != 0 {
		t.Fatalf("a restart re-announced a standing refusal: %+v", bot.sent)
	}
}

// A restart must not repeat a message the operator already received.
//
// This reproduces a sequence seen live: action 6652b6b3932c was announced at
// 20:00:46, the alerts service restarted at 20:26:50, and the SAME action was
// announced again at 20:31:52. The in-memory dedup map does not survive a
// restart, and the seeding heuristic cannot cover it — seeding adopts only
// SETTLED actions on purpose, so a restart landing while an action is in flight
// seeds nothing, and the new process announces that action again when it
// settles. Only persisted state can tell "already announced by my predecessor"
// apart from "settled while I was down".
func TestARestartDoesNotRepeatAnAlreadyAnnouncedAction(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	record := filepath.Join(t.TempDir(), "announced.json")
	const actionID = "6652b6b3932c"

	build := func(status *statusStub, bot *botStub) *Service {
		t.Helper()
		service, err := New(Config{
			Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{status},
			AllowedChatIDs: []int64{11}, AnnouncedPath: record,
			Now: func() time.Time { return status.snapshot.ObservedAt },
		})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}

	// First process: seeds, then announces the settled action once.
	first := testSnapshot(now)
	first.LastAction.Result.ActionID = actionID
	first.LastAction.Result.Decision = "pending"
	first.LastAction.Result.Signature = ""
	first.LastAction.Result.Verdict = ""
	status, bot := &statusStub{snapshot: first}, &botStub{}
	service := build(status, bot)
	service.announce(t.Context())

	settled := testSnapshot(now)
	settled.LastAction.Result.ActionID = actionID
	settled.LastAction.Result.Decision = "complete"
	status.snapshot = settled
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("the settled trade was announced %d times, want 1", len(bot.sent))
	}

	// The restart: a fresh process, same record on disk. Its first poll sees an
	// action still in flight, so seeding stores nothing — the exact window the
	// in-memory map cannot cover.
	restartStatus := &statusStub{snapshot: first}
	restartBot := &botStub{}
	restarted := build(restartStatus, restartBot)
	restarted.announce(t.Context())

	restartStatus.snapshot = settled
	restarted.announce(t.Context())
	if len(restartBot.sent) != 0 {
		t.Fatalf("a restart repeated an already-announced action: %+v", restartBot.sent)
	}

	// A genuinely NEW action must still get through — dedup must not become mute.
	next := testSnapshot(now)
	next.LastAction.Result.ActionID = "a81307f166ae"
	next.LastAction.Result.Decision = "complete"
	restartStatus.snapshot = next
	restarted.announce(t.Context())
	if len(restartBot.sent) != 1 {
		t.Fatalf("a new action after a restart was swallowed: %+v", restartBot.sent)
	}
}
