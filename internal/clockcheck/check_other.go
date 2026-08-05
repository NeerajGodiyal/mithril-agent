//go:build !linux

package clockcheck

import "errors"

func SystemSample() (Sample, error) {
	return Sample{}, errors.New("synchronous clock evidence is supported only on Linux")
}
