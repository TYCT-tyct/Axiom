package privfile_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"axiom/internal/platform/privfile"
)

// write puts content at path with an explicit mode, whatever the process's umask.
func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// TestOpen: the rule is the opened file's own mode. 0600 and 0400 are private; anything the group or others may read is
// refused, as is a path that is not a regular file.
func TestOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not represent NTFS ACLs")
	}
	for _, c := range []struct {
		mode os.FileMode
		want error
	}{
		{0o600, nil},
		{0o400, nil},
		{0o640, privfile.ErrPermissions},
		{0o604, privfile.ErrPermissions},
		{0o620, privfile.ErrPermissions},
		{0o601, privfile.ErrPermissions},
		{0o666, privfile.ErrPermissions},
	} {
		path := filepath.Join(t.TempDir(), "private")
		write(t, path, "material", c.mode)
		file, err := privfile.Open(path, os.Open)
		if !errors.Is(err, c.want) {
			closeFiles(file)
			t.Fatalf("mode %o: %v, want %v", c.mode, err, c.want)
		}
		if c.want != nil {
			continue
		}
		read, err := io.ReadAll(file)
		closeFiles(file)
		if err != nil || string(read) != "material" {
			t.Fatalf("mode %o: read %q, %v", c.mode, read, err)
		}
	}
}

// TestOpenRefusesWhatIsNotARegularFile: a directory opens, so only the descriptor's type turns it away.
func TestOpenRefusesWhatIsNotARegularFile(t *testing.T) {
	if _, err := privfile.Open(t.TempDir(), os.Open); !errors.Is(err, privfile.ErrNotRegular) {
		t.Fatalf("a directory: %v, want ErrNotRegular", err)
	}
	if _, err := privfile.Open(filepath.Join(t.TempDir(), "absent"), os.Open); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file: %v, want ErrNotExist", err)
	}
}

// TestOpenChecksTheDescriptorNotThePath: the opener opens the 0644 file and only then puts a 0600 file at the path. The
// mode that decides must be the one of the file that was opened and will be read, not of what the path names now;
// checking by path would admit this file, which is the window the shared helper exists to close.
func TestOpenChecksTheDescriptorNotThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not represent NTFS ACLs")
	}
	dir := t.TempDir()
	path, good, bad := filepath.Join(dir, "key"), filepath.Join(dir, "good"), filepath.Join(dir, "bad")
	write(t, good, "private", 0o600)
	write(t, bad, "readable", 0o644)
	if err := os.Rename(bad, path); err != nil {
		t.Fatal(err)
	}
	open := func(name string) (*os.File, error) {
		file, err := os.Open(name) // #nosec G304 -- the test builds both paths it uses.
		if err != nil {
			return nil, err
		}
		if err := os.Rename(good, name); err != nil {
			closeFiles(file)
			return nil, err
		}
		return file, nil
	}
	file, err := privfile.Open(path, open)
	closeFiles(file)
	if !errors.Is(err, privfile.ErrPermissions) {
		t.Fatalf("a 0644 file replaced by a 0600 one at the open: %v, want ErrPermissions", err)
	}
}

// closeFiles closes the files a test opened and ignores a nil; a file opened only for reading has nothing to flush.
func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close() //nolint:errcheck // a read-only close has nothing to flush and nothing to tell the test.
		}
	}
}
