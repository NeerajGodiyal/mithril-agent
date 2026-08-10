package main

import (
	"os"
	"strings"
	"testing"
)

func TestTheSigningPageIsEmbeddedInTheBinary(t *testing.T) {
	if !strings.Contains(embeddedSigningPage, "Verify your payout wallet") {
		t.Fatal("the embedded wallet page is missing or incomplete")
	}
	runbook, err := os.ReadFile("../../OPERATIONS.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runbook), "deploy/"+signingPageName) {
		t.Error("the runbook still asks operators to install a separate wallet page")
	}
}

func TestTheTunnelTargetUsesTheAddressReachedBySSH(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "198.51.100.20 23102 203.0.113.10 22")
	t.Setenv("SUDO_USER", "ubuntu")
	if target := hostnameForCopy(); target != "ubuntu@203.0.113.10" {
		t.Errorf("target = %q", target)
	}
}

func TestTheTunnelTargetFallsBackToAnObviousPlaceholder(t *testing.T) {
	for _, name := range []string{"SSH_CONNECTION", "SUDO_USER", "LOGNAME", "USER"} {
		t.Setenv(name, "")
	}
	if target := hostnameForCopy(); target != "YOUR_SERVER" {
		t.Errorf("target = %q", target)
	}
}

func TestTheTunnelTargetNeverSuggestsRoot(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.0.2.1 22222 192.0.2.9 22")
	for _, name := range []string{"SUDO_USER", "LOGNAME", "USER"} {
		t.Setenv(name, "root")
	}
	if target := hostnameForCopy(); strings.Contains(target, "root@") {
		t.Errorf("target suggests a root login: %q", target)
	}
}

func TestTheTunnelTargetNeverPrintsShellSyntax(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.0.2.1 22222 192.0.2.9 22")
	t.Setenv("SUDO_USER", "operator;touch-bad")
	t.Setenv("LOGNAME", "")
	t.Setenv("USER", "")
	if target := hostnameForCopy(); target != "YOUR_SERVER" {
		t.Errorf("unsafe target was printed for copy: %q", target)
	}
}
