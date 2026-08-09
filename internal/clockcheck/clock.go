package clockcheck

import "time"

// ErrClockUnusable marks every refusal that comes from the host clock rather
// than from anything the agent did.
//
// It exists because of a real morning: a runner that could not read a tight
// enough clock reported "operation_failed" on all three legs, every ten
// seconds, for five minutes. The cause — an NTP poll interval that let the
// kernel's uncertainty bound drift past policy — was invisible in that word,
// and only `preflight` named it. A category an operator cannot act on is worse
// than no category, because it looks like broken trading code.
//
// Wrapped, not replaced: the specific sentence still says WHICH clock property
// failed, while errors.Is lets the runner say "clock", and doctor says how to
// fix it.
var ErrClockUnusable = errUnusableClock{}

type errUnusableClock struct{}

func (errUnusableClock) Error() string { return "host clock is not usable for signing" }

const (
	MaxOffset             = 50 * time.Millisecond
	InitialMaxUncertainty = 100 * time.Millisecond
	MaxUncertaintyCap     = 2 * time.Second
	MaxSampleAge          = 2 * time.Second
)

type Sample struct {
	WallTime         time.Time `json:"wall_time"`
	BootID           string    `json:"boot_id"`
	MonotonicNanos   uint64    `json:"monotonic_nanos"`
	OffsetNanos      int64     `json:"offset_nanos"`
	UncertaintyNanos uint64    `json:"uncertainty_nanos"`
}
