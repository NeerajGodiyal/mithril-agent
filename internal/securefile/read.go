package securefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
)

func ReadPrivate(path string, maxBytes int64) ([]byte, error) {
	if path == "" || maxBytes <= 0 {
		return nil, errors.New("private file path and positive size limit are required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("private file path must be absolute")
	}
	if err := secureexec.ValidateProtectedDirectory(filepath.Dir(path)); err != nil {
		return nil, errors.New("private file directory is not trusted")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errors.New("private file must be regular and not a symlink")
	}
	if !fileowner.Trusted(before) {
		return nil, errors.New("private file owner is not trusted")
	}
	file, err := openReadOnlyNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, errors.New("private file changed while opening")
	}
	if !after.Mode().IsRegular() || !fileowner.Trusted(after) ||
		after.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private file permissions must not grant group or other access")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read private file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("private file exceeds size limit")
	}
	if len(data) == 0 {
		return nil, errors.New("private file is empty")
	}
	final, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if final.Size() != after.Size() || !final.ModTime().Equal(after.ModTime()) ||
		final.Mode() != after.Mode() {
		return nil, errors.New("private file changed while reading")
	}
	return data, nil
}
