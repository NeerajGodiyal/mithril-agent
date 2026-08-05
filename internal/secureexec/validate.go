package secureexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
)

func ValidateExecutable(path string) error {
	return validateProtectedFile(path, true)
}

// ValidateProtectedFile rejects replaceable files and directory ancestry.
func ValidateProtectedFile(path string) error {
	return validateProtectedFile(path, false)
}

// ValidateProtectedDirectory rejects replaceable directory ancestry while
// allowing sticky shared roots such as /tmp.
func ValidateProtectedDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("directory must be an absolute clean path")
	}
	return validateProtectedAncestors(path)
}

func validateProtectedFile(path string, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("command must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect command: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("command must be a regular file, not a symlink")
	}
	if !fileowner.Trusted(info) {
		return errors.New("command must be owned by the current user or root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("command must not be group- or world-writable")
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return errors.New("command must be executable")
	}
	return validateProtectedAncestors(filepath.Dir(path))
}

func validateProtectedAncestors(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect command directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !fileowner.RootOwned(info) {
				return errors.New("command directory ancestry contains an untrusted symlink")
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !filepath.IsAbs(resolved) {
				return errors.New("command directory ancestry contains an invalid symlink")
			}
			if err := validateProtectedAncestors(resolved); err != nil {
				return err
			}
			path = filepath.Dir(path)
			continue
		}
		if !info.IsDir() {
			return errors.New("command directory ancestry must not contain a symlink")
		}
		if !fileowner.Trusted(info) {
			return errors.New("command directory ancestry has an untrusted owner")
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errors.New("command directory ancestry must not be group- or world-writable")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func ValidateEnvironment(environment []string) error {
	for _, value := range environment {
		name, _, ok := strings.Cut(value, "=")
		if !ok || name == "" || strings.ContainsRune(name, '\x00') || strings.ContainsRune(value, '\x00') {
			return errors.New("environment contains an invalid entry")
		}
	}
	return nil
}

func MinimalEnvironment(overrides []string) []string {
	return buildEnvironment(false, overrides)
}

func MCPEnvironment(overrides []string) []string {
	return buildEnvironment(true, overrides)
}

func buildEnvironment(includeMithril bool, overrides []string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		allowed := name == "HOME" || name == "USER" || name == "LOGNAME" ||
			name == "PATH" || name == "TMPDIR" || name == "TEMP" || name == "TMP" ||
			name == "LANG" || name == "TZ" || name == "SYSTEMROOT" || name == "WINDIR" ||
			strings.HasPrefix(name, "LC_")
		if includeMithril && allowedMithrilEnvironment[name] {
			allowed = true
		}
		if allowed {
			values[name] = value
		}
	}
	for _, entry := range overrides {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

var allowedMithrilEnvironment = map[string]bool{
	"MITHRIL_ACCOUNTS_PATH":            true,
	"MITHRIL_BLOCK_SOURCE":             true,
	"MITHRIL_DISK_CRITICAL_PERCENT":    true,
	"MITHRIL_DISK_WARN_PERCENT":        true,
	"MITHRIL_LOG_DIR":                  true,
	"MITHRIL_MCP_MAX_CONCURRENT":       true,
	"MITHRIL_MCP_OUTPUT_BUDGET_BYTES":  true,
	"MITHRIL_MCP_PROFILE":              true,
	"MITHRIL_MCP_RATE_BURST":           true,
	"MITHRIL_MCP_RATE_PER_SECOND":      true,
	"MITHRIL_MCP_TOOL_TIMEOUT_SECONDS": true,
	"MITHRIL_METRICS_URL":              true,
	"MITHRIL_NODE_CGROUP_PATH":         true,
	"MITHRIL_PPROF_URL":                true,
	"MITHRIL_REFERENCE_RPC_URL":        true,
	"MITHRIL_REPLAY_P99_WARN_MS":       true,
	"MITHRIL_REPLAY_PATH":              true,
	"MITHRIL_RPC_URL":                  true,
	"MITHRIL_SHREDSTORE_PATH":          true,
	"MITHRIL_SLOTS_BEHIND_WARN":        true,
	"MITHRIL_SNAPSHOTS_PATH":           true,
	"MITHRIL_STATE_PATH":               true,
}

type DiscardCounter struct {
	Total uint64
}

func (w *DiscardCounter) Write(data []byte) (int, error) {
	w.Total += uint64(len(data))
	return len(data), nil
}
