//go:build linux

package clockcheck

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestValidateTimex(t *testing.T) {
	valid := unix.Timex{
		Status:   unix.STA_NANO,
		Offset:   -87_680,
		Maxerror: 408_000,
	}
	offset, uncertainty, err := validateTimex(unix.TIME_OK, valid)
	if err != nil {
		t.Fatal(err)
	}
	if offset != valid.Offset ||
		uncertainty != uint64(valid.Maxerror)*uint64(time.Microsecond) {
		t.Fatalf("offset=%d uncertainty=%d", offset, uncertainty)
	}

	tests := []struct {
		name  string
		state int
		timex unix.Timex
	}{
		{
			name:  "clock state",
			state: unix.TIME_ERROR,
			timex: valid,
		},
		{
			name:  "unsynchronized",
			state: unix.TIME_OK,
			timex: func() unix.Timex {
				value := valid
				value.Status |= unix.STA_UNSYNC
				return value
			}(),
		},
		{
			name:  "offset",
			state: unix.TIME_OK,
			timex: func() unix.Timex {
				value := valid
				value.Offset = int64(MaxOffset) + 1
				return value
			}(),
		},
		{
			name:  "uncertainty",
			state: unix.TIME_OK,
			timex: func() unix.Timex {
				value := valid
				value.Maxerror = int64(MaxUncertaintyCap/time.Microsecond) + 1
				return value
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := validateTimex(test.state, test.timex); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
