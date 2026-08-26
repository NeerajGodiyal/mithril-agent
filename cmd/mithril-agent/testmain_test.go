package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Hosted runners may themselves run under systemd; individual tests opt in explicitly.
	_ = os.Unsetenv("INVOCATION_ID")
	os.Exit(m.Run())
}
