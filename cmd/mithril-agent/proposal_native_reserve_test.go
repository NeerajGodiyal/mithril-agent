package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestProposalNativeReserveFlagIsOptionalAndReadOnly(t *testing.T) {
	var output bytes.Buffer
	if err := runProposalRecheck(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--retained-reserve-lamports", "advisory", "neither", "execution readiness"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("help omits %q", want)
		}
	}
	for _, raw := range []string{"", "0", "-1", "+1", "01", "1.0", " 1", "0x10", "18446744073709551616"} {
		err := runProposalRecheck(t.Context(), []string{"--retained-reserve-lamports", raw}, &output)
		if err == nil || !strings.Contains(err.Error(), "positive canonical decimal") {
			t.Errorf("invalid reserve %q was not rejected before IO: %v", raw, err)
		}
	}
	for _, args := range [][]string{nil, {"--retained-reserve-lamports", "1"}, {"--retained-reserve-lamports", "18446744073709551615"}} {
		err := runProposalRecheck(t.Context(), args, &output)
		if err == nil || !strings.Contains(err.Error(), "requires a private candidate") {
			t.Errorf("optional reserve changed ordinary protected-input check: %v", err)
		}
	}
}
