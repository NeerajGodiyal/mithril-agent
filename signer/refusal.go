package signer

import "errors"

// A refusal is the policy deciding NO. It is not a fault, and the difference
// matters to whoever is watching: a spent daily cap means "try tomorrow", a
// closed window means "try in the next one", while an unwritable ledger or a
// malformed policy means "something is broken, go fix it".
//
// The two used to be indistinguishable. Every error out of AuthorizeAndSign
// left the signer process the same way, the caller collapsed all of them into
// "signer process failed", and the runner reported operation_failed — so a
// budget that would reset at midnight looked exactly like a broken binary.
// Marking the refusals is what lets the exit code carry that one bit.
//
// It is a TYPE rather than a wrapped sentinel so the message is unchanged: the
// operator-facing text is already the clearest statement of what happened, and
// prefixing it would only make the line longer.
type refusalError struct{ message string }

func (e *refusalError) Error() string { return e.message }

// refused marks an error as a policy decision rather than a fault.
func refused(message string) error { return &refusalError{message: message} }

// IsRefusal reports whether the policy declined, as opposed to failing. Callers
// use it to tell an operator to wait rather than to investigate.
func IsRefusal(err error) bool {
	var refusal *refusalError
	return errors.As(err, &refusal)
}
