package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/shadow"
)

func TestShadowSearchUsesTrainingOnlyAndCannotAuthorize(t *testing.T) {
	policy := validShadowPolicy()
	train := []uint64{
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
	}
	validation := slices.Clone(train)
	result, err := searchShadowCandidate(policy, train, validation, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "research_only" || result.Authorized || result.Promotable ||
		!result.PoolModelled || result.AssumedSpreadBPS != 100 ||
		result.CandidatesEvaluated != 1 || result.Candidate.SellAtMicros != 200_000_000 ||
		result.Candidate.BuyAtMicros != 100_000_000 ||
		result.Training.FullRoundTrips == 0 || result.Validation.FullRoundTrips == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestShadowSearchCLIReadsSeparateHashChainedDays(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := validShadowPolicy()
	policyPath := writeShadowPolicy(t, policy)
	prices := []uint64{
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
		200_000_000, 200_000_000, 100_000_000, 100_000_000,
	}
	writeShadowSearchDay(t, root, policy, "2026-08-17", prices)
	writeShadowSearchDay(t, root, policy, "2026-08-18", prices)

	var output bytes.Buffer
	if err := run([]string{
		"shadow", "search", "--policy", policyPath, "--dir", root,
		"--train-day", "2026-08-17", "--validation-day", "2026-08-18",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result shadowSearchResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TrainDay != "2026-08-17" || result.ValidationDay != "2026-08-18" ||
		result.Status != "research_only" || result.Authorized {
		t.Fatalf("result = %+v", result)
	}
}

func TestShadowSearchRejectsLeakyOrUnusableInputs(t *testing.T) {
	policy := validShadowPolicy()
	if _, err := searchShadowCandidate(policy, []uint64{1, 1}, []uint64{1, 2}, 100); err == nil ||
		!strings.Contains(err.Error(), "distinct") {
		t.Fatalf("single training level error = %v", err)
	}
	if _, err := searchShadowCandidate(policy, []uint64{1, 2}, []uint64{1, 2}, 10_000); err == nil ||
		!strings.Contains(err.Error(), "spread") {
		t.Fatalf("unsafe spread error = %v", err)
	}
	if err := runShadowSearch([]string{
		"--policy", "/tmp/policy", "--dir", "/tmp/journals",
		"--train-day", "2026-08-18", "--validation-day", "2026-08-18",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "later") {
		t.Fatalf("same-day split error = %v", err)
	}
}

func writeShadowSearchDay(
	t *testing.T, directory string, policy shadow.Policy, day string, prices []uint64,
) {
	t.Helper()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(filepath.Join(directory, "shadow-"+day+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(start, shadow.EventOpened, "", shadow.Opening{
		Version: shadow.JournalVersion, PolicySHA256: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	for index, price := range prices {
		at := start.Add(time.Duration(index+1) * time.Minute)
		if _, err := store.Append(at, shadow.EventWaiting, "", shadow.Tick{
			At: at, Event: shadow.EventWaiting, PriceMicros: price,
		}); err != nil {
			t.Fatal(err)
		}
	}
}
