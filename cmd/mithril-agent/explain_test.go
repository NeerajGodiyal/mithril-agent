package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExplainStatesTheLimitsAReviewerMustKnow(t *testing.T) {
	var out bytes.Buffer
	if err := runExplain(nil, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	// These are the claims a reviewer is trusting; losing any of them silently
	// would make the document misleading rather than merely shorter.
	for _, required := range []string{
		"Devnet",
		"cannot touch Mainnet",
		// Shadow mode reads Mainnet, so the document must be precise about
		// which side cannot: claiming "no Mainnet code path" outright would
		// now be false, and a false reassurance is worse than none.
		"no Mainnet execution path",
		"holds no key",
		"at most ONE swap",
		"no strategy",
		"NOT custody separation",
		"read and report",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("explain output no longer states %q", required)
		}
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Error("explain output must not contain an endpoint")
	}
}

func TestExplainRejectsArguments(t *testing.T) {
	var out bytes.Buffer
	if err := runExplain([]string{"--json"}, &out); err == nil {
		t.Fatal("explain accepted an unsupported argument")
	}
}
