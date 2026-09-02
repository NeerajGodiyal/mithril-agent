package secureexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
)

func TestProtectedExecutablesAndFilesRejectReplaceableAncestry(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(directory, "command")
	if err := os.WriteFile(command, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutable(command); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(directory, "script.mjs")
	if err := os.WriteFile(script, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProtectedFile(script); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutable(script); err == nil {
		t.Fatal("non-executable command was accepted")
	}

	unsafe := filepath.Join(directory, "unsafe")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	unsafeCommand := filepath.Join(unsafe, "command")
	if err := os.WriteFile(unsafeCommand, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutable(unsafeCommand); err == nil {
		t.Fatal("command below a writable directory was accepted")
	}
	safeLeaf := filepath.Join(unsafe, "safe-leaf")
	if err := os.Mkdir(safeLeaf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProtectedDirectory(safeLeaf); err == nil {
		t.Fatal("safe leaf below a writable ancestor was accepted")
	}

	actual := filepath.Join(directory, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "linked")
	if err := os.Symlink(actual, linked); err != nil {
		t.Fatal(err)
	}
	linkedCommand := filepath.Join(linked, "command")
	if err := os.WriteFile(filepath.Join(actual, "command"), []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	linkedInfo, err := os.Lstat(linked)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateExecutable(linkedCommand)
	if fileowner.RootOwned(linkedInfo) {
		if err != nil {
			t.Fatalf("protected root-owned symlink was rejected: %v", err)
		}
	} else if err == nil {
		t.Fatal("command below a symlinked directory was accepted")
	}
}

func TestChildEnvironmentsExcludeAgentRPCSecrets(t *testing.T) {
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "secret-mithril")
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", "secret-primary")
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", "secret-secondary")
	t.Setenv("MITHRIL_RPC_URL", "local-node")
	t.Setenv("MITHRIL_UNUSED_SECRET", "mithril-secret")
	t.Setenv("SSH_AUTH_SOCK", "/private/agent.sock")
	t.Setenv("UNRELATED_SECRET", "secret")

	mcp := strings.Join(MCPEnvironment(nil), "\n")
	if strings.Contains(mcp, "secret-primary") || strings.Contains(mcp, "secret-secondary") ||
		strings.Contains(mcp, "UNRELATED_SECRET") || strings.Contains(mcp, "mithril-secret") ||
		strings.Contains(mcp, "SSH_AUTH_SOCK") ||
		!strings.Contains(mcp, "MITHRIL_RPC_URL=local-node") {
		t.Fatalf("unexpected MCP environment:\n%s", mcp)
	}
	signer := strings.Join(MinimalEnvironment(nil), "\n")
	if strings.Contains(signer, "MITHRIL_RPC_URL") || strings.Contains(signer, "UNRELATED_SECRET") {
		t.Fatalf("unexpected signer environment:\n%s", signer)
	}
}

func TestEnvironmentOverridesAreDeterministic(t *testing.T) {
	t.Setenv("TZ", "first")
	environment := MinimalEnvironment([]string{"TZ=second"})
	joined := strings.Join(environment, "\n")
	if strings.Count(joined, "TZ=") != 1 || !strings.Contains(joined, "TZ=second") {
		t.Fatalf("override was not applied once: %s", joined)
	}
}
