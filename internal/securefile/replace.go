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

// ReplacePrivate atomically replaces a bounded private file and syncs its
// parent directory. The parent must already be private and trusted.
func ReplacePrivate(path string, data []byte, maxBytes int64) error {
	if path == "" || maxBytes <= 0 {
		return errors.New("private file path and positive size limit are required")
	}
	if !filepath.IsAbs(path) {
		return errors.New("private file path must be absolute")
	}
	if len(data) == 0 || int64(len(data)) > maxBytes {
		return errors.New("private file content is empty or exceeds size limit")
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if err := secureexec.ValidateProtectedDirectory(parent); err != nil {
		return errors.New("private file directory is not trusted")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect private file directory: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() ||
		parentInfo.Mode().Perm()&0o022 != 0 || !fileowner.Trusted(parentInfo) {
		return errors.New("private file directory is not trusted")
	}
	if targetInfo, err := os.Lstat(path); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
			return errors.New("private file target is not a regular file")
		}
		if !fileowner.Trusted(targetInfo) {
			return errors.New("private file target owner is not trusted")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private file target: %w", err)
	}

	temp, err := os.CreateTemp(parent, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create private temporary file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set private temporary file permissions: %w", err)
	}
	if err := writeAll(temp, data); err != nil {
		cleanup()
		return fmt.Errorf("write private temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync private temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close private temporary file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace private file: %w", err)
	}
	directory, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open private file directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync private file directory: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
