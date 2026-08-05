package main

import (
	"bytes"
	"strings"
	"testing"
)

// The offline walkthrough must work with no network at all, so a reviewer on a
// locked-down machine still sees the audit-chain evidence.
func TestWalkthroughOfflineProvesTheAuditChain(t *testing.T) {
	var out bytes.Buffer
	if err := runWalkthrough(t.Context(), []string{"--offline"}, &out); err != nil {
		t.Fatalf("offline walkthrough failed: %v", err)
	}
	text := out.String()
	for _, required := range []string{"VERIFIED", "REJECTED"} {
		if !strings.Contains(text, required) {
			t.Errorf("walkthrough output no longer contains %q", required)
		}
	}
	// The safety claim matters, its casing does not.
	if !strings.Contains(strings.ToLower(text), "cannot place a trade") {
		t.Error("walkthrough no longer states that it cannot place a trade")
	}
	// A missing format argument silently degrades the report into noise.
	if strings.Contains(text, "%!") {
		t.Errorf("walkthrough output has a broken format verb:\n%s", text)
	}
}

// It must never overstate what it demonstrated. Claiming the live trade path
// was proved is the specific dishonesty that would mislead a reviewer.
func TestWalkthroughStatesWhatItDidNotProve(t *testing.T) {
	var out bytes.Buffer
	if err := runWalkthrough(t.Context(), []string{"--offline"}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "did NOT prove") {
		t.Fatal("walkthrough no longer states its limits")
	}
	if !strings.Contains(text, "prepared Linux host") {
		t.Error("walkthrough does not say the real trade needs the prepared host")
	}
	// It must not imply it performed or could perform a trade.
	for _, forbidden := range []string{"trade executed", "swap completed", "funds moved"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Errorf("walkthrough implies it traded: %q", forbidden)
		}
	}
}

func TestWalkthroughRejectsArguments(t *testing.T) {
	var out bytes.Buffer
	if err := runWalkthrough(t.Context(), []string{"extra"}, &out); err == nil {
		t.Fatal("walkthrough accepted a stray argument")
	}
}
