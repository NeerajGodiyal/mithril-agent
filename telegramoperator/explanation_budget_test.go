package telegramoperator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileExplanationBudgetExhaustionPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(protectedTempDir(t), "explanation-budget.json")
	day := time.Date(2026, time.August, 2, 23, 59, 0, 0, time.FixedZone("test", 5*60*60))
	budget, err := NewFileExplanationBudget(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.Reserve(day); err != nil {
		t.Fatal(err)
	}
	if err := budget.Reserve(day.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileExplanationBudget(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reserve(day); !errors.Is(err, errExplanationBudgetExhausted) {
		t.Fatalf("restart exhaustion error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("budget mode = %v", info.Mode())
	}
}

func TestFileExplanationBudgetUsesUTCDaysAndRejectsClockRollback(t *testing.T) {
	path := filepath.Join(protectedTempDir(t), "explanation-budget.json")
	budget, err := NewFileExplanationBudget(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	firstDay := time.Date(2026, time.August, 2, 23, 30, 0, 0, time.UTC)
	if err := budget.Reserve(firstDay); err != nil {
		t.Fatal(err)
	}
	if err := budget.Reserve(firstDay.In(time.FixedZone("west", -7*60*60))); !errors.Is(err, errExplanationBudgetExhausted) {
		t.Fatalf("same UTC day error = %v", err)
	}
	if err := budget.Reserve(firstDay.Add(30 * time.Minute)); err != nil {
		t.Fatalf("UTC rollover error = %v", err)
	}
	if err := budget.Reserve(firstDay); err == nil || errors.Is(err, errExplanationBudgetExhausted) {
		t.Fatalf("clock rollback did not fail closed: %v", err)
	}
}

func TestFileExplanationBudgetRejectsCorruptionWithoutOverwrite(t *testing.T) {
	corruptions := map[string]string{
		"unknown field":   `{"version":1,"day_utc":"2026-08-02","requests":1,"extra":true}`,
		"duplicate field": `{"version":1,"day_utc":"2026-08-02","requests":1,"requests":2}`,
		"wrong version":   `{"version":2,"day_utc":"2026-08-02","requests":1}`,
		"invalid day":     `{"version":1,"day_utc":"2026-8-2","requests":1}`,
		"zero requests":   `{"version":1,"day_utc":"2026-08-02","requests":0}`,
		"unbounded count": `{"version":1,"day_utc":"2026-08-02","requests":1001}`,
	}
	for name, value := range corruptions {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(protectedTempDir(t), "explanation-budget.json")
			corrupt := []byte(value)
			if err := os.WriteFile(path, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			budget, err := NewFileExplanationBudget(path, 2)
			if err != nil {
				t.Fatal(err)
			}
			if err := budget.Reserve(time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)); err == nil {
				t.Fatal("corrupt budget was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(corrupt) {
				t.Fatalf("corrupt budget was overwritten: %q", after)
			}
		})
	}
}

func TestProviderFailureStillConsumesBudgetReservation(t *testing.T) {
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	explainer := &explainerStub{err: errors.New("provider failed")}
	budget, err := NewFileExplanationBudget(
		filepath.Join(protectedTempDir(t), "explanation-budget.json"), 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		Bot: &botStub{}, Cursor: &cursorStub{},
		Status: &statusStub{snapshot: testSnapshot(now)}, AllowedChatIDs: []int64{123},
		Explainer: explainer, ExplanationBudget: budget, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	first, ok := service.Reply(context.Background(), 123, "/explain first")
	if !ok || !strings.Contains(first, "provider failed") || explainer.calls != 1 {
		t.Fatalf("first reply=%q calls=%d", first, explainer.calls)
	}
	now = now.Add(time.Second)
	second, ok := service.Reply(context.Background(), 123, "/explain second")
	if !ok || !strings.Contains(second, "budget is exhausted") || explainer.calls != 1 {
		t.Fatalf("second reply=%q calls=%d", second, explainer.calls)
	}
}
