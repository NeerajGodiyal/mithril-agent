package securefile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFileRoundTripAndTrustBoundary(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private.json")
	for _, content := range [][]byte{[]byte("first"), []byte("replacement")} {
		if err := ReplacePrivate(path, content, 32); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("private file mode = %o, want 600", info.Mode().Perm())
		}
		got, err := ReadPrivate(path, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("private file = %q, want %q", got, content)
		}
	}

	if _, err := ReadPrivate("relative", 32); err == nil {
		t.Fatal("relative private-file path was accepted")
	}
	if _, err := ReadPrivate(path, 1); err == nil {
		t.Fatal("oversized private file was accepted")
	}
	if err := ReplacePrivate(path, nil, 32); err == nil {
		t.Fatal("empty private-file content was accepted")
	}
	if err := ReplacePrivate(path, bytes.Repeat([]byte{'x'}, 33), 32); err == nil {
		t.Fatal("oversized private-file content was accepted")
	}
}

func TestCreatePrivateRefusesToReplaceExistingFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private.json")
	if err := CreatePrivate(path, []byte("first"), 32); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private file mode = %o, want 600", info.Mode().Perm())
	}
	if err := CreatePrivate(path, []byte("replacement"), 32); err == nil {
		t.Fatal("existing private file was replaced")
	}
	got, err := ReadPrivate(path, 32)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("private file = %q, want first", got)
	}
}

func TestRenameNoReplacePublishesOnce(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("published directory is unavailable: %v", err)
	}

	secondSource := filepath.Join(parent, "second-source")
	if err := os.Mkdir(secondSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RenameNoReplace(secondSource, target); err == nil {
		t.Fatal("existing target directory was replaced")
	}
	if _, err := os.Stat(secondSource); err != nil {
		t.Fatalf("refused source directory was removed: %v", err)
	}
}

func TestPrivateFileRejectsReplaceablePaths(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := ReadPrivate(link, 32); err == nil {
		t.Fatal("private-file symlink was accepted for reading")
	}
	if err := ReplacePrivate(link, []byte("replacement"), 32); err == nil {
		t.Fatal("private-file symlink was accepted for replacement")
	}
	if err := CreatePrivate(link, []byte("replacement"), 32); err == nil {
		t.Fatal("private-file symlink was accepted for creation")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "secret" {
		t.Fatalf("symlink target changed to %q: %v", got, err)
	}

	permissive := filepath.Join(dir, "permissive")
	if err := os.WriteFile(permissive, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(permissive, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(permissive, 32); err == nil {
		t.Fatal("group-readable private file was accepted")
	}

	openDir := filepath.Join(dir, "open")
	if err := os.Mkdir(openDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(openDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ReplacePrivate(filepath.Join(openDir, "private"), []byte("secret"), 32); err == nil {
		t.Fatal("private file under a replaceable directory was accepted")
	}
}

func TestWriteAllRejectsNoProgress(t *testing.T) {
	err := writeAll(writerFunc(func([]byte) (int, error) { return 0, nil }), []byte("data"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress write = %v, want io.ErrShortWrite", err)
	}

	var output bytes.Buffer
	err = writeAll(writerFunc(func(data []byte) (int, error) {
		return output.Write(data[:1])
	}), []byte("data"))
	if err != nil || output.String() != "data" {
		t.Fatalf("short-write retry = %q, %v", output.String(), err)
	}
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(data []byte) (int, error) { return write(data) }
