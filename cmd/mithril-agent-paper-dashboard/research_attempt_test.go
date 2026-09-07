package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseResearchAttemptNativeStates(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	id := strings.Repeat("a", 32)
	start := now.Add(-time.Minute).Format("Mon 2006-01-02 15:04:05 MST")
	end := now.Add(-time.Second).Format("Mon 2006-01-02 15:04:05 MST")
	for _, tc := range []struct {
		name, active, sub, result, pid, id, start, end, want string
		hasStart, hasEnd                                     bool
	}{
		{"preflight", "activating", "start-pre", "success", "123", id, start, end, "preparing", false, false},
		{"oneshot", "activating", "start", "success", "123", id, start, end, "running", true, false},
		{"post start", "activating", "start-post", "success", "123", id, start, end, "finishing", false, false},
		{"old exit", "activating", "start", "success", "123", id, end, start, "running", true, false},
		{"active", "active", "running", "success", "123", id, start, "", "running", true, false},
		{"stopping preflight", "deactivating", "stop-post", "exit-code", "123", id, start, end, "finishing", false, false},
		{"complete retained pid", "inactive", "dead", "success", "123", id, start, end, "completed", true, true},
		{"never run", "inactive", "dead", "success", "0", "", "", "", "idle", false, false},
		{"preflight failed", "failed", "failed", "exit-code", "123", id, start, end, "failed", false, false},
		{"incoherent active", "activating", "start", "success", "0", id, start, end, "unknown", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "LoadState=loaded\nActiveState=" + tc.active + "\nSubState=" + tc.sub + "\nInvocationID=" + tc.id + "\nExecMainPID=" + tc.pid + "\nExecMainStartTimestamp=" + tc.start + "\nExecMainExitTimestamp=" + tc.end + "\nResult=" + tc.result + "\n"
			got, err := parseResearchAttempt(raw, now)
			if err != nil || got.State != tc.want || (got.StartedAt != nil) != tc.hasStart || (got.FinishedAt != nil) != tc.hasEnd {
				t.Fatalf("state = %+v, %v", got, err)
			}
			if (got.State == "idle" || got.State == "unknown") && got.InvocationID != "" {
				t.Fatal("old invocation attached to idle/unknown")
			}
		})
	}
}

func TestParseResearchAttemptRejectsMalformedNativeOutput(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	base := "LoadState=loaded\nActiveState=inactive\nSubState=dead\nInvocationID=\nExecMainPID=0\nExecMainStartTimestamp=\nExecMainExitTimestamp=\nResult=success\n"
	for _, raw := range []string{
		base + "Result=success\n", strings.Replace(base, "Result=success\n", "", 1), base + "Secret=forbidden\n", strings.Repeat("x", 4097),
		strings.Replace(base, "InvocationID=", "InvocationID=XYZ", 1), strings.Replace(base, "ExecMainPID=0", "ExecMainPID=01", 1),
		strings.Replace(base, "ExecMainStartTimestamp=", "ExecMainStartTimestamp=Tue 2026-09-08 12:01:00 UTC", 1),
		strings.Replace(base, "ExecMainStartTimestamp=", "ExecMainStartTimestamp=Tue 2026-09-08 12:00:00 CEST", 1),
	} {
		if _, err := parseResearchAttempt(raw, now); err == nil {
			t.Fatal("malformed native output accepted")
		}
	}
}

func TestResearchAttemptOutputIsBounded(t *testing.T) {
	var output researchAttemptOutput
	if _, ok := any(&output).(io.ReaderFrom); ok {
		t.Fatal("copy may bypass bounded Write")
	}
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 4097))); err == nil || output.Len() > 4096 {
		t.Fatal("copy exceeded output limit")
	}
	if n, err := output.Write([]byte(strings.Repeat("x", 4096))); n != 4096 || err != nil {
		t.Fatal("maximum output rejected")
	}
	if _, err := output.Write([]byte("x")); err == nil || output.Len() != 4096 {
		t.Fatal("oversized output retained")
	}
}
