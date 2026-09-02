package jupiterquote

import (
	"context"
	"sync"
	"time"
)

const (
	keylessRequestSpacing = 2100 * time.Millisecond
	freeKeyRequestSpacing = 1100 * time.Millisecond
)

type requestGate interface {
	Wait(context.Context) error
}

type memoryRequestGate struct {
	mu      sync.Mutex
	next    time.Time
	spacing time.Duration
}

func (gate *memoryRequestGate) Wait(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		now := time.Now()
		gate.mu.Lock()
		if !gate.next.After(now) {
			gate.next = now.Add(gate.spacing)
			gate.mu.Unlock()
			return nil
		}
		wait := time.Until(gate.next)
		gate.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func newRequestGate(path string, keyed bool) requestGate {
	spacing := keylessRequestSpacing
	if keyed {
		spacing = freeKeyRequestSpacing
	}
	if path != "" {
		return newFileRequestGate(path, spacing)
	}
	return &memoryRequestGate{spacing: spacing}
}
