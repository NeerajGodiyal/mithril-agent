package telegramoperator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/journal"
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
		"Last trade: complete", "Outcome: Confirmed on-chain", "Submitted: yes",
		"Input: 0.001000000 SOL", "Output: 1200 base units (minimum 1100 base units)",
		"Signature: public-signature",
		"Explorer: https://explorer.solana.com/tx/public-signature?cluster=devnet",
		"Trade observed: 2026-08-02T11:59:40Z",
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
	if !ok || !strings.Contains(reply, "Last trade: unknown") ||
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
	if !ok || !strings.Contains(reply, "Input: 0.100000 devUSDC") ||
		!strings.Contains(reply, "Output: 0.000001200 SOL (minimum 0.000001100 SOL)") ||
		strings.Contains(reply, "Input: 0 lamports") {
		t.Fatalf("buy last trade = %q", reply)
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
		Status: &statusStub{snapshot: testSnapshot(now)}, AllowedChatIDs: []int64{123},
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
		Bot: &botStub{}, Cursor: &cursorStub{}, Status: reader, AllowedChatIDs: []int64{123},
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
		Bot: bot, Cursor: cursor, Status: &statusStub{snapshot: testSnapshot(now)},
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
		Bot: failedBot, Cursor: failedCursor, Status: &statusStub{snapshot: testSnapshot(now)},
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
		Bot: bot, Cursor: cursor, Status: &statusStub{}, AllowedChatIDs: []int64{123},
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
		Bot: bot, Cursor: &cursorStub{}, Status: &statusStub{},
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
		Bot: &botStub{}, Cursor: &cursorStub{}, Status: reader, AllowedChatIDs: []int64{123},
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
