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
		"walletless Solana program-intelligence",
		"no browser wallet, signing key, or block explorer",
		"cannot submit a\ntransaction",
		"exact interface hash",
		"hash-pinned evidence",
		"processed context slot",
		"Alpenglow rooted feed or separately labelled classic finalized feed",
		"deterministic unsigned",
		"five bounded tools",
		"or seven when rooted indexes are configured",
		"opens no network listener",
		"accepts no caller-selected path or endpoint",
		"Construction requires one\n    reviewed and pinned current interface",
		"fresh or\n    separate AccountsDB",
		"Pinning and ingest remain explicit",
		"Agent output is a\n    request for deterministic tools, never transaction authority",
		"Optional bounded trading modules",
		"Devnet",
		"installed strategy runner cannot trade on Mainnet",
		// Read-only tools inspect Mainnet, so the document must be precise
		// about which side cannot: claiming "no Mainnet code path" outright
		// would be false, and a false reassurance is worse than none.
		"no operational Mainnet execution path",
		"package-only",
		"Read-only Mainnet tools",
		"Shadow mode",
		"binds the quote venue and token pair",
		"does not approve a strategy",
		"Proposal check",
		"cannot sign",
		"holds no wallet signing key",
		"one bounded demo",
		"maximum action count",
		"daily caps",
		"no built-in market strategy",
		"may act automatically",
		"signer, and submitter use\nseparate operating-system identities",
		"runner cannot open their keys",
		"could reconstruct that one policy-approved transaction",
		"not an adversarial boundary against a compromised runner",
		"production custody boundary",
		"read and report",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("explain output no longer states %q", required)
		}
	}
	if strings.Contains(text, "The one thing that looks at Mainnet") {
		t.Error("explain output still claims shadow is the only Mainnet reader")
	}
	if strings.Contains(text, "runner never receives the raw signed transaction") {
		t.Error("explain output overstates the Devnet runner boundary")
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

func TestExplainAcceptsHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runExplain([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "What this is") {
		t.Fatalf("explain help output = %q", out.String())
	}
}
