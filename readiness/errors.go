package readiness

import "errors"

// These are construction faults, not operator conditions. A report that
// violates them would quietly mislead whoever reads it.
var (
	errMissingIdentity      = errors.New("readiness check needs a name and a title")
	errBlockedWithoutAction = errors.New("a blocked check must tell the operator what to do")
	errReadyWithAction      = errors.New("a ready check must not demand an action")
)
