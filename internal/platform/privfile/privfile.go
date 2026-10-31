// Package privfile opens the files a node keeps to itself — its key ring, its Valkey password and its mTLS key — so
// that the checks and the read are of one file. Statting a path and then reading it by path checks one file and reads
// another: between the two, the path can be replaced with a file that is valid but readable by the group or others.
package privfile

import (
	"errors"
	"os"
	"runtime"
)

// Opener opens a path for reading. os.Open is the production one; a test passes one that replaces the path as the open
// happens, so the ordering between the check and the read is proven every run instead of raced against a writer.
type Opener func(string) (*os.File, error)

var (
	// ErrNotRegular is a file that is not a regular file: a directory, a device, or a link the caller refuses.
	ErrNotRegular = errors.New("want a regular file")
	// ErrPermissions is a file the group or others can read.
	ErrPermissions = errors.New("permissions must deny all group and other access")
)

// Open opens path with open and returns the open file once its own descriptor says it is what a private file must be: a
// regular file the group and others cannot read. The caller reads that file and must not open the path again, so a
// replacement after this returns cannot change what was checked. What the file holds, how large it may be and whether a
// link is acceptable stay the caller's rules. Windows mode bits cannot express NTFS ACLs, so there the deployment
// restricts the file's ACL instead.
func Open(path string, open Opener) (*os.File, error) {
	file, err := open(path) // #nosec G304 -- the trusted startup configuration chooses the file.
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close() //nolint:errcheck,gosec // A file opened only for reading has nothing to flush.
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close() //nolint:errcheck,gosec // see above
		return nil, ErrNotRegular
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		file.Close() //nolint:errcheck,gosec // see above
		return nil, ErrPermissions
	}
	return file, nil
}
