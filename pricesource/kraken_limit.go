package pricesource

import (
	"context"
	"sync"
	"time"
)

const krakenRequestSpacing = 1100 * time.Millisecond

type krakenRequestGate interface {
	Wait(context.Context) error
}

type memoryKrakenRequestGate struct {
	mu      sync.Mutex
	next    time.Time
	spacing time.Duration
}

var processKrakenRequestGate krakenRequestGate = &memoryKrakenRequestGate{
	spacing: krakenRequestSpacing,
}

func (gate *memoryKrakenRequestGate) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	now := time.Now()
	gate.mu.Lock()
	reserved := gate.next
	if reserved.Before(now) {
		reserved = now
	}
	gate.next = reserved.Add(gate.spacing)
	gate.mu.Unlock()

	wait := time.Until(reserved)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newKrakenRequestGate(path string) krakenRequestGate {
	if path == "" {
		return processKrakenRequestGate
	}
	return newFileKrakenRequestGate(path)
}
