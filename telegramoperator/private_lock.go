package telegramoperator

import "errors"

var (
	errPrivateLockHeld        = errors.New("private state lock is already held")
	errPrivateLockUnavailable = errors.New("private state lock is unavailable")
)
