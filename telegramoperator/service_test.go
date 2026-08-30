package telegramoperator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

type statusStub struct {
	snapshot operatorstatus.Snapshot
	err      error
	reads    int
}

func (s *statusStub) Read() (operatorstatus.Snapshot, error) {
	s.reads++
	return s.snapshot, s.err
}

type paperStatusStub struct {
	snapshot paperstatus.Snapshot
	err      error
}

func (s *paperStatusStub) Read() (paperstatus.Snapshot, error) {
	return s.snapshot, s.err
}

func TestPaperAnnouncementsAreExplicitPersistentAndReadOnly(t *testing.T) {
	now := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	id := strings.Repeat("b", 64)
	reader := &paperStatusStub{snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now,
		Events: []paperstatus.Event{{
			ID: id, At: now, Kind: paperstatus.KindOrderFilled,
			Message: "PAPER SIMULATION — order filled. No transaction was signed or submitted.",
		}},
	}}
	path := filepath.Join(protectedTempDir(t), "announced.json")
	bot := &botStub{}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AnnouncedPath: path,
		AllowedChatIDs: []int64{123, 456},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	service.announce(t.Context())
	if len(bot.sent) != 2 {
		t.Fatalf("paper sends = %+v", bot.sent)
	}
	for _, sent := range bot.sent {
		if !strings.HasPrefix(sent.Text, "PAPER SIMULATION") ||
			!strings.Contains(sent.Text, "No transaction was signed or submitted") ||
			strings.Contains(sent.Text, "explorer.solana.com") {
			t.Fatalf("ambiguous paper alert = %q", sent.Text)
		}
	}

	restartedBot := &botStub{}
	restarted, err := New(Config{
		Bot: restartedBot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AnnouncedPath: path,
		AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.announce(t.Context())
	if len(restartedBot.sent) != 0 {
		t.Fatalf("restart repeated paper alert: %+v", restartedBot.sent)
	}
}

func TestPaperAnnouncementRetriesOnlyWhenNobodyWasTold(t *testing.T) {
	now := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	reader := &paperStatusStub{snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now,
		Events: []paperstatus.Event{{
			ID: strings.Repeat("c", 64), At: now, Kind: paperstatus.KindOrderMissed,
			Message: "PAPER SIMULATION — order missed. No transaction was signed or submitted.",
		}},
	}}
	bot := &botStub{sendErr: errors.New("unavailable")}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	bot.sendErr = nil
	service.announce(t.Context())
	if len(bot.sent) != 1 {
		t.Fatalf("paper retry sends = %+v", bot.sent)
	}
}

func TestPaperAnnouncementDeduplicatesPerChatAfterPartialDelivery(t *testing.T) {
	now := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	reader := &paperStatusStub{snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: now,
		Events: []paperstatus.Event{{
			ID: strings.Repeat("d", 64), At: now, Kind: paperstatus.KindOrderOpened,
			Message: "PAPER SIMULATION — order opened. No transaction was signed or submitted.",
		}},
	}}
	bot := &botStub{unreachable: 456}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AllowedChatIDs: []int64{123, 456},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	bot.unreachable = 0
	service.announce(t.Context())
	if len(bot.sent) != 2 || bot.sent[0].ChatID == bot.sent[1].ChatID {
		t.Fatalf("per-chat paper deliveries = %+v", bot.sent)
	}
}

func TestPaperAnnouncementDedupIsScopedToItsSource(t *testing.T) {
	now := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	id := strings.Repeat("e", 64)
	build := func(message string) *paperStatusStub {
		return &paperStatusStub{snapshot: paperstatus.Snapshot{
			Version: paperstatus.Version, ObservedAt: now,
			Events: []paperstatus.Event{{
				ID: id, At: now, Kind: paperstatus.KindStrategyChanged,
				Message: "PAPER SIMULATION — " + message + ". No transaction was signed or submitted.",
			}},
		}}
	}
	bot := &botStub{}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources:   []PaperStatusReader{build("champion changed"), build("challenger changed")},
		AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	service.announce(t.Context())
	if len(bot.sent) != 2 || bot.sent[0].Text == bot.sent[1].Text {
		t.Fatalf("paper source deliveries = %+v", bot.sent)
	}
}

func TestPaperAnnouncementFullSnapshotDoesNotEvictItsOwnDeliveries(t *testing.T) {
	now := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	events := make([]paperstatus.Event, paperstatus.MaxEvents)
	for index := range events {
		events[index] = paperstatus.Event{
			ID: fmt.Sprintf("%064x", index+1), At: now.Add(time.Duration(index) * time.Second),
			Kind:    paperstatus.KindOrderFilled,
			Message: "PAPER SIMULATION — order filled. No transaction was signed or submitted.",
		}
	}
	reader := &paperStatusStub{snapshot: paperstatus.Snapshot{
		Version: paperstatus.Version, ObservedAt: events[len(events)-1].At,
		DroppedEvents: 1, Events: events,
	}}
	path := filepath.Join(protectedTempDir(t), "announced.json")
	chats := []int64{101, 102, 103, 104}
	bot := &botStub{}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AnnouncedPath: path,
		AllowedChatIDs: chats,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	if want := (paperstatus.MaxEvents + 1) * len(chats); len(bot.sent) != want || want <= maxAnnouncedEntries {
		t.Fatalf("paper sends = %d, want %d above legacy cap %d", len(bot.sent), want, maxAnnouncedEntries)
	}
	service.announce(t.Context())
	if len(bot.sent) != (paperstatus.MaxEvents+1)*len(chats) {
		t.Fatalf("unchanged full snapshot was resent: %d", len(bot.sent))
	}
	restartedBot := &botStub{}
	restarted, err := New(Config{
		Bot: restartedBot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AnnouncedPath: path,
		AllowedChatIDs: chats,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.announce(t.Context())
	if len(restartedBot.sent) != 0 {
		t.Fatalf("restart resent full paper snapshot: %d", len(restartedBot.sent))
	}
}

func TestPaperSourceLogsOnlyAvailabilityTransitions(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	reader := &paperStatusStub{err: errors.New("private source detail")}
	service, err := New(Config{
		Bot: &botStub{}, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		PaperSources: []PaperStatusReader{reader}, AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.announce(t.Context())
	service.announce(t.Context())
	if got := strings.Count(logs.String(), "paper status source 1 unavailable"); got != 1 ||
		strings.Contains(logs.String(), "private source detail") {
		t.Fatalf("unavailable logs = %q", logs.String())
	}
	now := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	reader.err = nil
	reader.snapshot = paperstatus.Snapshot{Version: paperstatus.Version, ObservedAt: now, Events: []paperstatus.Event{}}
	service.announce(t.Context())
	service.announce(t.Context())
	if got := strings.Count(logs.String(), "paper status source 1 available again"); got != 1 {
		t.Fatalf("recovery logs = %q", logs.String())
	}
}

type explainerStub struct {
	response string
	err      error
	request  ExplanationRequest
	calls    int
	wait     <-chan struct{}
}

type budgetStub struct {
	calls int
	err   error
}

func (b *budgetStub) Reserve(time.Time) error {
	b.calls++
	return b.err
}

func (e *explainerStub) Explain(ctx context.Context, request ExplanationRequest) (string, error) {
	e.calls++
	e.request = request
	if e.wait != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-e.wait:
		}
	}
	return e.response, e.err
}

type botStub struct {
	mu          sync.Mutex
	polls       []int64
	updates     [][]Update
	sent        []Update
	sendErr     error
	unreachable int64
	cancelAfter int
	cancel      context.CancelFunc
}

type blockingBot struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingBot) Poll(ctx context.Context, _ int64, _ time.Duration) ([]Update, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*blockingBot) Send(context.Context, int64, string) error { return nil }

type cursorStub struct {
	offset int64
	stores []int64
	err    error
}

func (c *cursorStub) Load() (int64, error) { return c.offset, c.err }

func (c *cursorStub) Store(offset int64) error {
	if c.err != nil {
		return c.err
	}
	c.offset = offset
	c.stores = append(c.stores, offset)
	return nil
}

func (b *botStub) Poll(ctx context.Context, offset int64, _ time.Duration) ([]Update, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.polls = append(b.polls, offset)
	index := len(b.polls) - 1
	if b.cancelAfter > 0 && len(b.polls) >= b.cancelAfter {
		b.cancel()
		return nil, context.Canceled
	}
	if index < len(b.updates) {
		return b.updates[index], nil
	}
	return nil, ctx.Err()
}

func (b *botStub) Send(_ context.Context, chatID int64, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sendErr != nil {
		return b.sendErr
	}
	// One unreachable chat among several is the ordinary misconfiguration: a
	// blocked bot, a mistyped ID, a group it was removed from.
	if b.unreachable != 0 && chatID == b.unreachable {
		return errors.New("forbidden: bot was blocked by the user")
	}
	b.sent = append(b.sent, Update{ChatID: chatID, Text: text})
	return nil
}

func TestParseAllowedChatIDsRequiresUniqueNumericValues(t *testing.T) {
	ids, err := ParseAllowedChatIDs("123, -456")
	if err != nil || len(ids) != 2 || ids[0] != 123 || ids[1] != -456 {
		t.Fatalf("IDs = %v, %v", ids, err)
	}
	for _, input := range []string{"", "0", "abc", "1,", "1,1"} {
		if _, err := ParseAllowedChatIDs(input); err == nil {
			t.Fatalf("accepted invalid allowlist %q", input)
		}
	}
}

func TestStatusExplainsRecoveryAndTerminalStops(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now.Add(-10 * time.Second))
	snapshot.Control.RecoveryPending = true
	reader := &statusStub{snapshot: snapshot}

	recovery, _, _, _ := statusReportFor(reader, now)
	if !strings.Contains(
		recovery,
		"Recovery: Waiting for independent confirmation — do not retry",
	) {
		t.Fatalf("recovery status is not actionable: %q", recovery)
	}

	reader.snapshot.Control = control.Status{
		Mode:             control.ModeNoNewActions,
		TerminalActionID: strings.Repeat("a", 64),
		TerminalOutcome:  "failed",
	}
	terminal, _, _, _ := statusReportFor(reader, now)
	if !strings.Contains(terminal, "Terminal stop: Failed — needs review") {
		t.Fatalf("terminal status is not actionable: %q", terminal)
	}
}

func TestDeterministicCommandsUseOnlyBoundedOperatorStatus(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	reader := &statusStub{snapshot: testSnapshot(now.Add(-10 * time.Second))}
	service := testService(t, reader, nil, &now)

	help, ok := service.Reply(t.Context(), 123, "/help@mithril_bot")
	if !ok || !strings.Contains(help, "Mithril operator — read only") ||
		!strings.Contains(help, "/status") || strings.Contains(help, "/explain") ||
		reader.reads != 0 {
		t.Fatalf("help = %q, reads=%d", help, reader.reads)
	}

	now = now.Add(time.Second)
	status, ok := service.Reply(t.Context(), 123, "/status")
	for _, expected := range []string{
		"Status: Stopped — no action is authorised", "Freshness: recent (11s old)",
		"Control: no_new_actions", "Attention required: no",
		"Source: local bounded operator status", "Observed: 2026-08-02T11:59:50Z",
	} {
		if !ok || !strings.Contains(status, expected) {
			t.Fatalf("status %q omitted %q", status, expected)
		}
	}

	now = now.Add(time.Second)
	reader.snapshot.Result.PriceTrigger = &pricetrigger.Status{
		Feed: pricetrigger.FeedSOLUSD, Direction: pricetrigger.SellAtOrAbove,
		ThresholdMicros: 150_000_000, Available: true,
		ConservativePrice: 150_250_000, ConditionMet: true,
		ExecutableMinimum: 150_100_000, ExecutableCondition: true,
		ObservedAt: now.Add(-2 * time.Second), PrimaryPublishedAt: now.Add(-3 * time.Second),
		SecondaryPublishedAt: now.Add(-3 * time.Second),
	}
	price, ok := service.Reply(t.Context(), 123, "/price")
	for _, expected := range []string{
		"Price rule: sell SOL at or above", "Target: $150.00",
		"Conservative price: $150.25", "Condition: ready",
		"Minimum executable rate: 150.10 devUSDC/SOL",
	} {
		if !ok || !strings.Contains(price, expected) {
			t.Fatalf("price %q omitted %q", price, expected)
		}
	}

	now = now.Add(time.Second)
	trade, ok := service.Reply(t.Context(), 123, "/last_trade")
	for _, expected := range []string{
		"Sent to your wallet — confirmed on-chain",
		"Sent      0.001000000 SOL", "Received  1200 base units",
		"https://explorer.solana.com/tx/public-signature?cluster=devnet",
		"devnet ·",
	} {
		if !ok || !strings.Contains(trade, expected) {
			t.Fatalf("last trade %q omitted %q", trade, expected)
		}
	}
	if len(status) > maxOutputBytes || len(trade) > maxOutputBytes {
		t.Fatal("deterministic output exceeded Telegram bound")
	}

	now = now.Add(time.Second)
	if reply, send := service.Reply(t.Context(), 999, "/status"); send || reply != "" {
		t.Fatalf("unauthorized reply = %q, send=%v", reply, send)
	}
	if reader.reads != 3 {
		t.Fatalf("unauthorized chat read status; reads=%d", reader.reads)
	}
}

func TestPromptedCommandsReportEveryStrategyLeg(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	sell := testSnapshot(now)
	buy := testSnapshot(now)
	buy.Profile, buy.ProfileVersion = orcaswap.BuyProfileName, orcaswap.BuyProfileVersion
	sweep := testSnapshot(now)
	sweep.Profile = agent.ProfileTreasurySweepV1

	service, err := New(Config{
		Bot: &botStub{}, Cursor: &cursorStub{}, AllowedChatIDs: []int64{123},
		Sources: []StatusReader{
			&statusStub{snapshot: sell}, &statusStub{snapshot: buy}, &statusStub{snapshot: sweep},
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"/status", "/price", "/last_trade"} {
		reply, ok := service.Reply(t.Context(), 123, command)
		if !ok {
			t.Fatalf("%s produced no reply", command)
		}
		for _, leg := range []string{"Sell\n", "Buy\n", "Sweep\n"} {
			if !strings.Contains(reply, leg) {
				t.Errorf("%s omitted %s:\n%s", command, strings.TrimSpace(leg), reply)
			}
		}
		if len(reply) > maxOutputBytes {
			t.Fatalf("%s exceeded Telegram's output bound", command)
		}
		now = now.Add(time.Second)
	}
}

func TestStatusExplicitlyReportsStaleAndUnknown(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	reader := &statusStub{snapshot: testSnapshot(now.Add(-2 * time.Minute))}
	service := testService(t, reader, nil, &now)
	reply, ok := service.Reply(t.Context(), 123, "/status")
	if !ok || !strings.Contains(reply, "Freshness: stale (120s old)") {
		t.Fatalf("stale status = %q", reply)
	}

	now = now.Add(time.Second)
	reader.err = errors.New("private path and endpoint must not escape")
	reply, ok = service.Reply(t.Context(), 123, "/status")
	if !ok || !strings.Contains(reply, "Status: unknown") ||
		strings.Contains(reply, "private path") || strings.Contains(reply, "endpoint") {
		t.Fatalf("unknown status = %q", reply)
	}

	now = now.Add(time.Second)
	reader.err = nil
	reader.snapshot = testSnapshot(now.Add(6 * time.Second))
	reply, ok = service.Reply(t.Context(), 123, "/last_trade")
	if !ok || !strings.Contains(reply, "Trade status unavailable") ||
		!strings.Contains(reply, "timestamp is in the future") {
		t.Fatalf("future status = %q", reply)
	}
}

func TestPriceStateSeparatesMarketAndExecutableConditions(t *testing.T) {
	tests := []struct {
		name   string
		status pricetrigger.Status
		want   string
	}{
		{
			name: "market target only",
			status: pricetrigger.Status{
				Available: true, ConditionMet: true,
			},
			want: "target reached; quote not checked",
		},
		{
			name: "executable quote below target",
			status: pricetrigger.Status{
				Available: true, ConditionMet: true, ExecutableMinimum: 1,
			},
			want: "quote below target",
		},
		{
			name: "executable quote accepted",
			status: pricetrigger.Status{
				Available: true, ConditionMet: true,
				ExecutableMinimum: 1, ExecutableCondition: true,
			},
			want: "ready",
		},
		{
			name: "buy quote above limit",
			status: pricetrigger.Status{
				Direction: pricetrigger.BuyAtOrBelow,
				Available: true, ConditionMet: true, ExecutableMinimum: 1,
			},
			want: "quote above limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := triggerState(test.status); got != test.want {
				t.Fatalf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLastTradeUsesExplicitBuyAssets(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now)
	snapshot.LastAction.Result.AmountLamports = 0
	snapshot.LastAction.Result.InputAmount = 100_000
	snapshot.LastAction.Result.InputAsset = "devUSDC"
	snapshot.LastAction.Result.OutputAsset = "SOL"
	snapshot.Profile = "orca_devnet_buy_v2"
	snapshot.ProfileVersion = 2
	service := testService(t, &statusStub{snapshot: snapshot}, nil, &now)
	reply, ok := service.Reply(t.Context(), 123, "/last_trade")
	// A BUY spends dollars to acquire SOL. This used to assert "Sold ... devUSDC"
	// — the test named itself after buy assets and then enshrined the backwards
	// wording, so the bug was protected rather than caught.
	if !ok || !strings.Contains(reply, "Bought SOL") ||
		!strings.Contains(reply, "Spent     0.100000 devUSDC") ||
		!strings.Contains(reply, "Received  0.000001200 SOL") ||
		strings.Contains(reply, "0 lamports") {
		t.Fatalf("buy last trade = %q", reply)
	}
	if strings.Contains(reply, "Sold") {
		t.Errorf("a buy was described as a sale: %q", reply)
	}
}

func TestRateAndInputBoundsAreFailClosed(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	reader := &statusStub{snapshot: testSnapshot(now)}
	service := testService(t, reader, nil, &now)
	if _, ok := service.Reply(t.Context(), 123, "/status"); !ok {
		t.Fatal("first request was rate limited")
	}
	if reply, ok := service.Reply(t.Context(), 123, "/help"); ok || reply != "" {
		t.Fatal("second immediate request was not silently rate limited")
	}
	now = now.Add(time.Second)
	reply, ok := service.Reply(t.Context(), 123, strings.Repeat("x", maxInputBytes+1))
	if !ok || !strings.Contains(reply, "Request: rejected") || len(reply) > maxOutputBytes {
		t.Fatalf("oversized request reply = %q", reply)
	}
}

func TestExplanationFailureCannotAffectDeterministicCommands(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	reader := &statusStub{snapshot: testSnapshot(now)}
	explainer := &explainerStub{err: errors.New("provider and endpoint failed")}
	service := testService(t, reader, explainer, &now)

	reply, ok := service.Reply(t.Context(), 123, "/explain why is it stopped?")
	if !ok || !strings.Contains(reply, "Explanation: unavailable") ||
		strings.Contains(reply, "endpoint") || explainer.calls != 1 {
		t.Fatalf("provider failure reply = %q, calls=%d", reply, explainer.calls)
	}
	if explainer.request.Question != "why is it stopped?" ||
		!strings.Contains(explainer.request.StatusText, "Status: Stopped") ||
		explainer.request.ObservedAt != reader.snapshot.ObservedAt {
		t.Fatalf("bounded explanation request = %+v", explainer.request)
	}

	now = now.Add(time.Second)
	reply, ok = service.Reply(t.Context(), 123, "/status")
	if !ok || !strings.Contains(reply, "Status: Stopped") || explainer.calls != 1 {
		t.Fatalf("deterministic fallback = %q, calls=%d", reply, explainer.calls)
	}
}

func TestExplanationBudgetFailurePreventsProviderCall(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	explainer := &explainerStub{response: "must not be returned"}
	budget := &budgetStub{err: errors.New("private budget is corrupt")}
	service, err := New(Config{
		Bot: &botStub{}, Cursor: &cursorStub{},
		Sources: []StatusReader{&statusStub{snapshot: testSnapshot(now)}}, AllowedChatIDs: []int64{123},
		Explainer: explainer, ExplanationBudget: budget, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := service.Reply(t.Context(), 123, "/explain why")
	if !ok || !strings.Contains(reply, "budget is unavailable") ||
		strings.Contains(reply, "private budget") || budget.calls != 1 || explainer.calls != 0 {
		t.Fatalf("reply=%q reservations=%d provider calls=%d", reply, budget.calls, explainer.calls)
	}
}

func TestExplanationTimeoutAndOutputAreBounded(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	reader := &statusStub{snapshot: testSnapshot(now)}
	wait := make(chan struct{})
	explainer := &explainerStub{wait: wait}
	service, err := New(Config{
		Bot: &botStub{}, Cursor: &cursorStub{}, Sources: []StatusReader{reader}, AllowedChatIDs: []int64{123},
		Explainer: explainer, ExplanationBudget: &budgetStub{},
		Now:             func() time.Time { return now },
		MinimumInterval: 100 * time.Millisecond, ExplanationLimit: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	reply, ok := service.Reply(t.Context(), 123, "/explain wait")
	if !ok || !strings.Contains(reply, "timed out") || time.Since(started) > time.Second {
		t.Fatalf("timeout reply = %q after %s", reply, time.Since(started))
	}
	close(wait)

	now = now.Add(time.Second)
	boundedExplainer := &explainerStub{response: strings.Repeat("界", maxExplanationBytes)}
	boundedService := testService(t, reader, boundedExplainer, &now)
	reply, ok = boundedService.Reply(t.Context(), 123, "/explain summarize")
	if !ok || len(reply) > maxOutputBytes || !strings.Contains(reply, "not authoritative") ||
		!strings.Contains(reply, "cannot take action") {
		t.Fatalf("bounded explanation length=%d reply=%q", len(reply), reply)
	}
}

func TestRunUsesOneConsumerAndAdvancesOnlyAfterSendAcknowledgement(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(t.Context())
	bot := &botStub{
		updates: [][]Update{{
			{ID: 7, ChatID: 999, Text: "/status"},
			{ID: 8, ChatID: 123, Text: "/status"},
		}},
		cancelAfter: 2, cancel: cancel,
	}
	cursor := &cursorStub{}
	service, err := New(Config{
		Bot: bot, Cursor: cursor, Sources: []StatusReader{&statusStub{snapshot: testSnapshot(now)}},
		AllowedChatIDs: []int64{123}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bot.polls) != 2 || bot.polls[0] != 0 || bot.polls[1] != 9 {
		t.Fatalf("poll offsets = %v", bot.polls)
	}
	if len(bot.sent) != 1 || bot.sent[0].ChatID != 123 ||
		!strings.Contains(bot.sent[0].Text, "Status: Stopped") {
		t.Fatalf("sent replies = %+v", bot.sent)
	}
	if len(cursor.stores) != 1 || cursor.stores[0] != 9 {
		t.Fatalf("durable cursor stores = %v", cursor.stores)
	}

	failedBot := &botStub{
		updates: [][]Update{{{ID: 10, ChatID: 123, Text: "/status"}}},
		sendErr: errors.New("no positive acknowledgement"),
	}
	failedCursor := &cursorStub{}
	failed, err := New(Config{
		Bot: failedBot, Cursor: failedCursor, Sources: []StatusReader{&statusStub{snapshot: testSnapshot(now)}},
		AllowedChatIDs: []int64{123}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Run(t.Context()); err == nil || len(failedBot.polls) != 1 {
		t.Fatalf("unacknowledged send err=%v polls=%v", err, failedBot.polls)
	}
	if len(failedCursor.stores) != 0 {
		t.Fatalf("cursor advanced before send acknowledgement: %v", failedCursor.stores)
	}
}

func TestRunCoalescesIgnoredUpdateCursorWritesPerPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	bot := &botStub{
		updates: [][]Update{{
			{ID: 20, ChatID: 999, Text: "/status"},
			{ID: 21, ChatID: 998, Text: "/status"},
			{ID: 22, ChatID: 997, Text: "/status"},
		}},
		cancelAfter: 2, cancel: cancel,
	}
	cursor := &cursorStub{}
	service, err := New(Config{
		Bot: bot, Cursor: cursor, Sources: []StatusReader{&statusStub{}}, AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cursor.stores) != 1 || cursor.stores[0] != 23 {
		t.Fatalf("coalesced cursor stores = %v", cursor.stores)
	}
	if len(bot.sent) != 0 {
		t.Fatalf("ignored updates received replies: %+v", bot.sent)
	}
}

func TestRunRejectsASecondConsumer(t *testing.T) {
	bot := &blockingBot{started: make(chan struct{})}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{}, Sources: []StatusReader{&statusStub{}},
		AllowedChatIDs: []int64{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	<-bot.started
	if err := service.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "active consumer") {
		t.Fatalf("second consumer error = %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first consumer shutdown = %v", err)
	}
}

func TestFileCursorIsPrivateDurableAndMonotonic(t *testing.T) {
	path := filepath.Join(protectedTempDir(t), "telegram-offset.json")
	cursor := FileCursor(path)
	if offset, err := cursor.Load(); err != nil || offset != 0 {
		t.Fatalf("empty cursor = %d, %v", offset, err)
	}
	if err := cursor.Store(42); err != nil {
		t.Fatal(err)
	}
	if offset, err := cursor.Load(); err != nil || offset != 42 {
		t.Fatalf("stored cursor = %d, %v", offset, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cursor mode = %v", info.Mode())
	}
	if err := cursor.Store(41); err == nil {
		t.Fatal("cursor moved backwards")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"next_offset":43,"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cursor.Load(); err == nil {
		t.Fatal("cursor accepted unknown JSON field")
	}
}

func protectedTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func testService(
	t *testing.T,
	reader StatusReader,
	explainer Explainer,
	now *time.Time,
) *Service {
	t.Helper()
	service, err := New(Config{
		Bot: &botStub{}, Cursor: &cursorStub{}, Sources: []StatusReader{reader}, AllowedChatIDs: []int64{123},
		Explainer: explainer, Now: func() time.Time { return *now },
		ExplanationBudget: explanationBudgetFor(explainer),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func explanationBudgetFor(explainer Explainer) ExplanationBudget {
	if explainer == nil {
		return nil
	}
	return &budgetStub{}
}

func testSnapshot(observedAt time.Time) operatorstatus.Snapshot {
	return operatorstatus.Snapshot{
		Version: operatorstatus.Version, ObservedAt: observedAt,
		Profile: "orca_devnet_swap_v1", ProfileVersion: 1, Cluster: "devnet",
		Result: execution.Result{Decision: "stopped", Reason: "Devnet actions are not enabled"},
		LastAction: operatorstatus.Action{
			ObservedAt: observedAt.Add(-10 * time.Second),
			Result: execution.Result{
				ActionID: "public-action", Decision: "complete", Verdict: "finalized",
				Submitted: true, AmountLamports: 1_000_000, MinimumOutput: 1_100,
				OutputAmount: 1_200, Signature: "public-signature",
			},
		},
		Journal: journal.Stats{MaxRecords: 100, MaxBytes: 1024},
		Control: control.Status{Mode: control.ModeNoNewActions},
	}
}

func TestStatusAcceptsMainnetCanaryControl(t *testing.T) {
	now := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now.Add(-time.Second))
	snapshot.Cluster = "mainnet-beta"
	snapshot.Result = execution.Result{Decision: "waiting"}
	snapshot.Control = control.Status{
		Mode: control.ModeMainnetCanary, ExpectedActionID: strings.Repeat("a", 64),
		ExpiresAt:  now.Add(time.Minute),
		MaxActions: 1, RemainingActions: 1,
	}
	report, _, ok, _ := statusReportFor(&statusStub{snapshot: snapshot}, now)
	if !ok || strings.Contains(report, "Status: unknown") ||
		!strings.Contains(report, "Control: mainnet_canary") ||
		!strings.Contains(report, "Attention required: no") {
		t.Fatalf("Mainnet canary status report = %q", report)
	}
}

// A stopped agent does not read the price at all: the read is bound to a slot
// it only proves when it is about to act, and proving one every cycle would
// spawn a node subprocess for a number nobody asked for. Reporting that as
// "temporarily unavailable" makes a deliberately idle agent look broken to the
// one person who most needs a clear answer — the operator deciding whether to
// arm it. It must say what is true and what to do instead.
func TestPriceExplainsAStoppedAgentRatherThanLookingBroken(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now.Add(-5 * time.Second))
	snapshot.Result.Decision = "stopped"
	snapshot.Result.PriceTrigger = &pricetrigger.Status{
		Feed:            pricetrigger.FeedSOLUSD,
		Direction:       pricetrigger.SellAtOrAbove,
		ThresholdMicros: 150_000_000,
		Available:       false,
	}
	reader := &statusStub{snapshot: snapshot}
	service := testService(t, reader, nil, &now)

	reply, ok := service.Reply(t.Context(), 123, "/price")
	if !ok {
		t.Fatal("price command produced no reply")
	}
	if strings.Contains(reply, "temporarily unavailable") {
		t.Errorf("a stopped agent was reported as a broken feed: %q", reply)
	}
	for _, expected := range []string{"Target: $150.00", "no trades are authorised", "swap check"} {
		if !strings.Contains(reply, expected) {
			t.Errorf("price reply omitted %q: %q", expected, reply)
		}
	}
}

// A genuine feed outage while the agent IS armed must still read as a fault,
// not be softened into the stopped explanation.
func TestPriceStillReportsARealOutageWhenArmed(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now.Add(-5 * time.Second))
	snapshot.Result.Decision = "waiting"
	snapshot.Result.PriceTrigger = &pricetrigger.Status{
		Feed:            pricetrigger.FeedSOLUSD,
		Direction:       pricetrigger.SellAtOrAbove,
		ThresholdMicros: 150_000_000,
		Available:       false,
	}
	reader := &statusStub{snapshot: snapshot}
	service := testService(t, reader, nil, &now)

	reply, ok := service.Reply(t.Context(), 123, "/price")
	if !ok || !strings.Contains(reply, "temporarily unavailable") {
		t.Errorf("an armed agent's feed outage was not reported as one: %q", reply)
	}
}

// /start is the first thing anyone presses, and pressing it is what makes
// Telegram willing to deliver to that chat at all. Answering "unknown" there is
// the first impression of a bot the operator is about to trust with money.
func TestStartAnswersLikeHelp(t *testing.T) {
	service := announceService(t, &statusStub{snapshot: testSnapshot(time.Unix(1_700_000_000, 0).UTC())}, &botStub{}, 7)
	start, ok := service.Reply(t.Context(), 7, "/start")
	if !ok {
		t.Fatal("/start was ignored")
	}
	if strings.Contains(start, "unknown") {
		t.Fatalf("/start answered as an unknown command: %s", start)
	}
	// It must be the same answer as /help, not a second thing to maintain.
	// (A second Reply would hit the per-chat rate limit, so compare the source.)
	if help := bounded(service.help(service.now().UTC())); start != help {
		t.Errorf("/start and /help disagree:\n%s\n---\n%s", start, help)
	}
}

// strings.Split("", ",") yields one EMPTY element, so an unset variable used to
// report "contains an empty value" — sending the operator hunting for a stray
// comma in a variable they had never set.
func TestUnsetChatAllowlistSaysItIsUnset(t *testing.T) {
	for _, value := range []string{"", "   "} {
		_, err := ParseAllowedChatIDs(value)
		if err == nil {
			t.Fatalf("an empty allowlist %q was accepted", value)
		}
		if !strings.Contains(err.Error(), "is not set") {
			t.Errorf("unset allowlist reported as %q", err)
		}
	}
	// A genuinely malformed value must still say so rather than "not set".
	if _, err := ParseAllowedChatIDs("7,,8"); err == nil ||
		strings.Contains(err.Error(), "is not set") {
		t.Errorf("a stray comma reported as unset: %v", err)
	}
}

// flakyPollBot fails its first `failures` polls, then serves one update and
// lets the context cancel. It models the transient network error that took the
// deployed operator down seven times in one night.
type flakyPollBot struct {
	botStub
	failures int
	polls    int
}

func (b *flakyPollBot) Poll(context.Context, int64, time.Duration) ([]Update, error) {
	b.polls++
	if b.polls <= b.failures {
		return nil, StatusError{Status: 502}
	}
	b.cancel()
	return nil, context.Canceled
}

// A read-only observer must ride out a blip. Exiting on the FIRST poll error
// meant a momentary outage killed the process, and once systemd's start limit
// was reached it stopped trying — alerts then went missing with no signal.
func TestTransientPollFailuresDoNotKillTheOperator(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	bot := &flakyPollBot{failures: maxConsecutivePollFailures - 1}
	bot.cancel = cancel
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{},
		Sources: []StatusReader{&statusStub{snapshot: testSnapshot(now)}}, AllowedChatIDs: []int64{123},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	service.pollRetryDelay = time.Millisecond
	if err := service.Run(ctx); err != nil {
		t.Fatalf("the operator died on transient poll failures: %v", err)
	}
	if bot.polls <= bot.failures {
		t.Fatalf("polled %d times, want more than the %d failures", bot.polls, bot.failures)
	}
}

// It must still give up eventually: a revoked token or a deleted bot has to
// surface as a failed unit, not as a process retrying forever in silence.
func TestSustainedPollFailuresStillFail(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	bot := &flakyPollBot{failures: maxConsecutivePollFailures + 5}
	bot.cancel = func() {}
	service, err := New(Config{
		Bot: bot, Cursor: &cursorStub{},
		Sources: []StatusReader{&statusStub{snapshot: testSnapshot(now)}}, AllowedChatIDs: []int64{123},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	service.pollRetryDelay = time.Millisecond
	if err := service.Run(t.Context()); err == nil {
		t.Fatal("a permanently broken bot retried forever instead of failing")
	}
	if bot.polls != maxConsecutivePollFailures {
		t.Errorf("gave up after %d polls, want %d", bot.polls, maxConsecutivePollFailures)
	}
}

// A chat that permanently rejects a reply must not stall the cursor: Telegram
// redelivers the same update, the process fails the same way, systemd loops it,
// and because announce() runs after this loop every other chat loses its trade
// alerts too. A group migrating to a supergroup causes this unaided.
func TestAPermanentlyRejectedReplyDoesNotStallTheCursor(t *testing.T) {
	for name, test := range map[string]struct {
		status    int
		wantStall bool
	}{
		"chat gone (400)":    {400, false},
		"bot blocked (403)":  {403, false},
		"rate limited (429)": {429, true},
		"server error (500)": {500, true},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
			ctx, cancel := context.WithCancel(t.Context())
			bot := &botStub{
				updates:     [][]Update{{{ID: 7, ChatID: 123, Text: "/status"}}},
				sendErr:     StatusError{Status: test.status},
				cancelAfter: 2,
				cancel:      cancel,
			}
			cursor := &cursorStub{}
			service, err := New(Config{
				Bot: bot, Cursor: cursor,
				Sources: []StatusReader{&statusStub{snapshot: testSnapshot(now)}}, AllowedChatIDs: []int64{123},
				Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			service.pollRetryDelay = time.Millisecond
			runErr := service.Run(ctx)

			if test.wantStall {
				// Transient: the reply is still owed, so the cursor must not move.
				if len(cursor.stores) != 0 {
					t.Errorf("a retryable failure advanced the cursor to %v", cursor.stores)
				}
				if runErr == nil {
					t.Error("a retryable failure was swallowed")
				}
				return
			}
			if len(cursor.stores) == 0 {
				t.Fatal("a permanent rejection left the update unconsumed; it will redeliver forever")
			}
			if cursor.stores[len(cursor.stores)-1] != 8 {
				t.Errorf("cursor = %v, want it past update 7", cursor.stores)
			}
		})
	}
}

// Only failures were logged, so an empty journal meant either "announced fine"
// or "never fired" — indistinguishable. That ambiguity produced a confident
// wrong diagnosis on a real host: five trades WERE announced, the journal was
// empty, and the conclusion was that alerting was broken.
//
// Silence must mean nothing happened.
