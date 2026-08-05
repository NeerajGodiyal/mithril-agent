package clockcheck

import "time"

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
