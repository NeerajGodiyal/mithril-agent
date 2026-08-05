package policyauthority

import (
	"math"
	"testing"
)

func TestGrantLifetimeRejectsOverflow(t *testing.T) {
	if _, err := grantLifetime(math.MaxUint64); err == nil {
		t.Fatal("accepted a grant lifetime that overflows time.Duration")
	}
}
