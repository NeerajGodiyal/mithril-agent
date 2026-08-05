//go:build linux

package clockcheck

import (
	"errors"
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func SystemSample() (Sample, error) {
	var timex unix.Timex
	state, err := unix.Adjtimex(&timex)
	if err != nil {
		return Sample{}, errors.New("read kernel clock state")
	}
	offsetNanos, uncertaintyNanos, err := validateTimex(state, timex)
	if err != nil {
		return Sample{}, err
	}
	var monotonic unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &monotonic); err != nil ||
		monotonic.Sec < 0 || monotonic.Nsec < 0 ||
		uint64(monotonic.Sec) > math.MaxUint64/uint64(time.Second) {
		return Sample{}, errors.New("read monotonic boot clock")
	}
	monotonicNanos := uint64(monotonic.Sec)*uint64(time.Second) + uint64(monotonic.Nsec)
	bootIDBytes, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Sample{}, errors.New("read kernel boot identity")
	}
	bootID := strings.TrimSpace(string(bootIDBytes))
	if len(bootID) != 36 {
		return Sample{}, errors.New("kernel boot identity is invalid")
	}
	wallTime := time.Now().UTC()
	return Sample{
		WallTime:         wallTime,
		BootID:           bootID,
		MonotonicNanos:   monotonicNanos,
		OffsetNanos:      offsetNanos,
		UncertaintyNanos: uncertaintyNanos,
	}, nil
}

func validateTimex(state int, timex unix.Timex) (int64, uint64, error) {
	if state != unix.TIME_OK || timex.Status&(unix.STA_UNSYNC|unix.STA_CLOCKERR) != 0 {
		return 0, 0, errors.New("kernel clock is not synchronized with normal leap state")
	}
	offsetNanos := timex.Offset * int64(time.Microsecond)
	if timex.Status&unix.STA_NANO != 0 {
		offsetNanos = timex.Offset
	}
	if offsetNanos < -int64(MaxOffset) || offsetNanos > int64(MaxOffset) {
		return 0, 0, errors.New("kernel clock offset exceeds policy")
	}
	maxErrorMicros := int64(timex.Maxerror)
	if maxErrorMicros < 0 || uint64(maxErrorMicros) > uint64(MaxUncertaintyCap/time.Microsecond) {
		return 0, 0, errors.New("kernel clock uncertainty exceeds policy")
	}
	return offsetNanos, uint64(maxErrorMicros) * uint64(time.Microsecond), nil
}
