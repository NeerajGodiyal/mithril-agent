//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package jupiterquote

import (
	"context"
	"errors"
	"time"
)

type unsupportedFileRequestGate struct{}

func newFileRequestGate(string, time.Duration) requestGate { return unsupportedFileRequestGate{} }

func (unsupportedFileRequestGate) Wait(context.Context) error {
	return errors.New("cross-process Jupiter request limiting is unsupported")
}
