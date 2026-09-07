package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceVerificationStopsOnToolFailure(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	makefile := []byte(readDocumentation(t, "../../Makefile"))
	checksum := "sha256sum"
	if _, err := exec.LookPath(checksum); err != nil {
		checksum = "shasum"
	}
	for _, tool := range []string{"mktemp", "find", "awk", "sort", "cmp", checksum} {
		t.Run(tool, func(t *testing.T) {
			realTool, err := exec.LookPath(tool)
			if err != nil {
				t.Skipf("%s is unavailable", tool)
			}
			root := t.TempDir()
			bin := filepath.Join(root, "tools")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{
				"Makefile":      makefile,
				"source.sha256": []byte(fmt.Sprintf("%x  ./Makefile\n", sha256.Sum256(makefile))),
			} {
				if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Inventory tools emit correct output before failing, so partial or
			// successful-looking output cannot hide their nonzero status.
			script := "#!/bin/sh\n"
			if tool != "mktemp" {
				script += "'" + strings.ReplaceAll(realTool, "'", "'\\''") + "' \"$@\"\n"
			}
			script += "exit 72\n"
			if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), makePath, "--no-print-directory", "verify-source", "MANIFEST=source.sha256")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if err == nil || strings.Contains(string(output), "MISMATCH:") || strings.Contains(string(output), "OK:") {
				t.Fatalf("tool failure was accepted or misreported as source evidence: %v\n%s", err, output)
			}
		})
	}
}
