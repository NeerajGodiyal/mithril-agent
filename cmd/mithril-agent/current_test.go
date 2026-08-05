package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The recorded location is what lets a reviewer run a bare `mithril-agent
// doctor` instead of remembering a path.
func TestCurrentConfigRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "profile", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordCurrentConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if got := discoverCurrentConfig(); got != configPath {
		t.Fatalf("discovered %q, want %q", got, configPath)
	}
	pointer, err := currentPointerPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pointer mode = %o, want 0600", perm)
	}
}

// A pointer is a hint about where to look, never an instruction to trust. A
// stale, relative, or non-file target must read as "nothing configured" so the
// operator gets the normal setup guidance instead of a confusing failure.
func TestCurrentConfigIgnoresAnythingSuspicious(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pointer, err := currentPointerPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "a-directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"deleted since setup": filepath.Join(home, "gone", "config.json"),
		"relative":            "profile/config.json",
		"unclean":             home + "/profile/../profile/config.json",
		"a directory":         directory,
		"empty":               "",
		"oversized":           strings.Repeat("/a", maxAnswerBytes),
	} {
		if err := os.WriteFile(pointer, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := discoverCurrentConfig(); got != "" {
			t.Errorf("%s pointer was used: %q", name, got)
		}
	}
}

// recordCurrentConfig must refuse to write a path the discovery side would
// reject, rather than leaving a pointer that silently never works.
func TestRecordCurrentConfigRejectsUnusablePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, path := range []string{"", "relative/config.json", "/a/../a/config.json"} {
		if err := recordCurrentConfig(path); err == nil {
			t.Errorf("recorded an unusable path: %q", path)
		}
	}
}

// With nothing recorded, doctor must still give the normal getting-started
// answer rather than an error about a missing pointer.
func TestDoctorWithNoRecordedConfigStillGuides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runDoctor(t.Context(), nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mithril-agent") {
		t.Fatalf("doctor gave no next step:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Using ") {
		t.Error("doctor claimed to be using a configuration that does not exist")
	}
}

// A symlink planted where the pointer lives must not redirect the read. The
// path it names is re-validated downstream regardless, but the pointer itself
// should follow the same hardened file discipline as everything else here.
func TestCurrentPointerDoesNotFollowASymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pointer, err := currentPointerPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(home, "planted.txt")
	if err := os.WriteFile(elsewhere, []byte(filepath.Join(home, "config.json")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, pointer); err != nil {
		t.Fatal(err)
	}
	if got := discoverCurrentConfig(); got != "" {
		t.Fatalf("a symlinked pointer was followed to %q", got)
	}
	// Writing must not follow it into the planted file either.
	target := filepath.Join(home, "profile", "config.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = recordCurrentConfig(target)
	planted, err := os.ReadFile(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(planted), "profile") {
		t.Fatal("recording the pointer wrote through the symlink")
	}
}

// A pointer with loose permissions must not be trusted, matching how every
// other private file in this codebase is read.
func TestCurrentPointerRejectsLoosePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "config.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordCurrentConfig(target); err != nil {
		t.Fatal(err)
	}
	pointer, err := currentPointerPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pointer, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := discoverCurrentConfig(); got != "" {
		t.Fatalf("a world-readable pointer was trusted: %q", got)
	}
}

// A reviewer must never have to know a path. The read-only commands and the
// demonstration all find the configuration the same way, so "one or two
// commands" is actually true rather than true-with-a-footnote.
func TestReviewerCommandsFindTheConfigWithoutAPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "profile", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordCurrentConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if got := discoverCurrentConfig(); got != configPath {
		t.Fatalf("discovery returned %q, want %q", got, configPath)
	}

	// Each of these must get past "which configuration?" without --config.
	// They will fail later on the empty config, which is fine — what matters
	// is that the failure is about the configuration's contents, not about a
	// missing flag the reviewer cannot supply.
	for name, run := range map[string]func() error{
		"demo":      func() error { return runSwapDemo(t.Context(), nil, &bytes.Buffer{}) },
		"preflight": func() error { return runPreflight(nil, &bytes.Buffer{}) },
		"swap check": func() error {
			return runSwap(t.Context(), []string{"check"}, &bytes.Buffer{})
		},
	} {
		err := run()
		if err != nil && strings.Contains(err.Error(), "requires --config") {
			t.Errorf("%s still demands an explicit --config: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "run: mithril-agent setup") {
			t.Errorf("%s did not find the recorded configuration: %v", name, err)
		}
	}
}

// The demonstration authorises a trade, so it may use only what this user's own
// setup recorded — never the installed production configuration that happens to
// sit on the same machine.
func TestDemoNeverReachesForTheInstalledConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// discoverCurrentConfig may fall back to the installed path; recordedConfig
	// must not. demo uses the latter.
	if recordedConfig() != "" {
		t.Fatal("nothing was recorded, yet a recorded config was returned")
	}
	err := runSwapDemo(t.Context(), nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("demo ran with nothing recorded")
	}
	if !strings.Contains(err.Error(), "requires --config") {
		t.Errorf("demo did not insist on an explicit path: %v", err)
	}
}

// With nothing recorded anywhere, the error must name the command that fixes
// it rather than only the flag that is missing.
func TestMissingConfigNamesTheCommandNotTheFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runSwapDemo(t.Context(), nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("demo ran with no configuration at all")
	}
	if !strings.Contains(err.Error(), "mithril-agent setup") {
		t.Errorf("the error does not name the command that fixes it: %v", err)
	}
}
