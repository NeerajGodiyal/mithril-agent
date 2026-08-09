package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
)

// A reviewer should be able to run `mithril-agent doctor` and get an answer,
// not have to remember and paste the path setup happened to choose. So setup
// records where it put the configuration, and the read-only commands look
// there when no --config is given.
//
// This is a convenience for finding a file, never an authority: the pointer
// only ever names a path, the path is validated before use, and every gate
// downstream still reads the configuration itself.
const (
	currentPointerName = "current"
	// A path and a newline. Anything larger is not a pointer.
	maxPointerBytes = int64(4096)
	// installedConfigPath is where the supervised installation puts the
	// configuration. It is a fallback, not an authority: it is used only when
	// nothing was recorded, it must still exist and be a regular file, and the
	// configuration is read and validated exactly as any other would be.
	//
	// Without it, a reviewer on a prepared host has to know and type a path
	// that the install wrote, which is the one thing they cannot be expected
	// to know.
	installedConfigPath = "/var/lib/mithril-agent/agent/config.json"
)

// currentPointerPath is the one fixed location, independent of where the
// operator chose to put the setup itself.
func currentPointerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("no home directory")
	}
	return filepath.Join(home, ".mithril-agent", currentPointerName), nil
}

// recordCurrentConfig notes the configuration setup just wrote. A failure here
// is not fatal — the operator can always pass --config — so the caller reports
// it rather than aborting a setup that otherwise succeeded.
func recordCurrentConfig(configPath string) error {
	if configPath == "" || !filepath.IsAbs(configPath) ||
		filepath.Clean(configPath) != configPath {
		return errors.New("configuration path must be absolute and clean")
	}
	pointer, err := currentPointerPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		return errors.New("could not create the agent directory")
	}
	// Written through the same hardened path the rest of the codebase uses, so
	// a pre-planted symlink at this location cannot redirect the write.
	return securefile.ReplacePrivate(pointer, []byte(configPath+"\n"), maxPointerBytes)
}

// discoverCurrentConfig returns the recorded configuration path, or "" when
// there is none to find. It returns a path only when that path exists and is a
// regular file, so a stale pointer reads as "nothing configured" rather than
// producing a confusing error from deep inside a later check.
func discoverCurrentConfig() string {
	if recorded := recordedConfig(); recorded != "" {
		return recorded
	}
	// A strategy records its legs in its own pointer and nothing else. Without
	// this fallback, `doctor`, `preflight`, `strategy show/alerts` and the swap
	// read-only commands all reported "nothing configured" while a real strategy
	// was armed and able to trade — the diagnostic surface blind exactly when it
	// was needed. The sell leg is the strategy's primary config.
	if strategy, _ := discoverStrategy(); strategy.sell != "" {
		return strategy.sell
	}
	if usableConfigPath(installedConfigPath) {
		return installedConfigPath
	}
	return ""
}

// recordedConfig returns the path setup noted, if any.
func recordedConfig() string {
	pointer, err := currentPointerPath()
	if err != nil {
		return ""
	}
	raw, err := securefile.ReadPrivate(pointer, maxPointerBytes)
	if err != nil {
		return ""
	}
	candidate := strings.TrimSpace(string(raw))
	if candidate == "" || !filepath.IsAbs(candidate) ||
		filepath.Clean(candidate) != candidate {
		return ""
	}
	if !usableConfigPath(candidate) {
		return ""
	}
	return candidate
}

// usableConfigPath reports whether a path is worth handing onward: absolute,
// clean, and an existing regular file rather than a symlink or a directory.
func usableConfigPath(candidate string) bool {
	if candidate == "" || !filepath.IsAbs(candidate) ||
		filepath.Clean(candidate) != candidate {
		return false
	}
	info, err := os.Lstat(candidate)
	return err == nil && info.Mode().IsRegular()
}
