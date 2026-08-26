//go:build !darwin && !linux

package securefile

import "errors"

// RenameNoReplace fails closed on platforms without a supported atomic primitive.
func RenameNoReplace(_, _ string) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
