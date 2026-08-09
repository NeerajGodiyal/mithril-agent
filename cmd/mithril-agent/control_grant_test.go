package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeControlState(t *testing.T, doc map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.json")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A delegated authority nobody can see the end of is the thing scoped
// permission exists to avoid. The expiry sits in the same document as the mode,
// so reading only the mode leaves the operator unable to answer "until when?".
func TestControlGrantReportsExpiryAndRemainingActions(t *testing.T) {
	expires := time.Now().UTC().Add(90 * time.Minute)
	path := writeControlState(t, map[string]any{
		"mode":              "devnet_enabled",
		"expires_at":        expires.Format(time.RFC3339Nano),
		"remaining_actions": 3,
	})
	mode, grant, _ := controlGrantAt(path)
	if mode != "devnet_enabled" {
		t.Fatalf("mode = %q", mode)
	}
	if !strings.Contains(grant, "3 action(s) left") {
		t.Errorf("grant does not state the remaining actions: %q", grant)
	}
	if !strings.Contains(grant, expires.Format(time.RFC3339)) {
		t.Errorf("grant does not state the expiry: %q", grant)
	}
}

// A stopped setup has no standing authority, so there is nothing to report and
// claiming otherwise would be noise.
func TestControlGrantIsSilentWithoutAGrant(t *testing.T) {
	path := writeControlState(t, map[string]any{"mode": "no_new_actions"})
	mode, grant, _ := controlGrantAt(path)
	if mode != "no_new_actions" {
		t.Fatalf("mode = %q", mode)
	}
	if grant != "" {
		t.Errorf("reported a grant where none exists: %q", grant)
	}
}

// An expired grant must read as expired rather than as time remaining, or the
// operator believes an authority is live when it is not.
func TestControlGrantNamesAnExpiredAuthority(t *testing.T) {
	path := writeControlState(t, map[string]any{
		"mode":              "devnet_enabled",
		"expires_at":        time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		"remaining_actions": 2,
	})
	_, grant, _ := controlGrantAt(path)
	if !strings.HasPrefix(grant, "expired ") {
		t.Errorf("an elapsed grant must read as expired: %q", grant)
	}
}

// A missing or unreadable control file must not fabricate an authority.
func TestControlGrantFailsClosedOnUnreadableState(t *testing.T) {
	if mode, grant, _ := controlGrantAt(""); mode != "" || grant != "" {
		t.Errorf("empty path produced %q / %q", mode, grant)
	}
	missing := filepath.Join(t.TempDir(), "absent.json")
	if mode, grant, _ := controlGrantAt(missing); mode != "" || grant != "" {
		t.Errorf("missing file produced %q / %q", mode, grant)
	}
}
