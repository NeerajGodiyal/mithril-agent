//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package pricesource

import (
	"context"
	"errors"
)

type unsupportedKrakenGate struct{}

func newFileKrakenRequestGate(string) krakenRequestGate { return unsupportedKrakenGate{} }

func (unsupportedKrakenGate) Wait(context.Context) error {
	return errors.New("cross-process Kraken request limiting is unsupported")
}
