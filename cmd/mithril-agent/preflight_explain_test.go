package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExplainPreflightReportsOnlyFailuresInOrder(t *testing.T) {
	var out bytes.Buffer
	results := map[string]string{
		"config":     preflightOK,
		"clock":      "failed",
		"mcp_inputs": "failed",
		"providers":  preflightOK,
	}
	if err := explainPreflight(&out, results); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "config") || strings.Contains(text, "providers") {
		t.Errorf("passing checks were reported: %q", text)
	}
	// mcp_inputs is evaluated before clock, so it must be listed first.
	if i, j := strings.Index(text, "mcp_inputs"), strings.Index(text, "clock"); i < 0 || j < 0 || i > j {
		t.Errorf("failures not in evaluation order: %q", text)
	}
	if !strings.Contains(text, "trusted time") {
		t.Errorf("clock failure not explained: %q", text)
	}
	if !strings.Contains(text, "not to the accounts database") {
		t.Errorf("mcp_inputs explanation lost its least-privilege guidance: %q", text)
	}
}

func TestExplainPreflightSaysNothingWhenEverythingPassed(t *testing.T) {
	var out bytes.Buffer
	if err := explainPreflight(&out, map[string]string{"config": preflightOK, "clock": preflightOK}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("passing preflight produced output: %q", out.String())
	}
}

// A check added later must still surface rather than vanish from the report.
func TestExplainPreflightSurfacesUnknownChecks(t *testing.T) {
	var out bytes.Buffer
	if err := explainPreflight(&out, map[string]string{"a_future_check": "failed"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "a_future_check") {
		t.Fatalf("unknown failing check was dropped: %q", text)
	}
	if !strings.Contains(text, "do not skip it") {
		t.Fatalf("unknown check lost its safety instruction: %q", text)
	}
}

// Every named check must have an explanation, or the report is a lookup table
// with holes.
func TestEveryPreflightCheckHasAnExplanation(t *testing.T) {
	for _, name := range preflightOrder {
		if _, ok := preflightMeaning[name]; !ok {
			t.Errorf("check %q has no plain-language explanation", name)
		}
	}
	var empty preflightChecks
	for name := range preflightResultMap(empty) {
		if _, ok := preflightMeaning[name]; !ok {
			t.Errorf("check %q is reported but not explained", name)
		}
	}
}
