package nfs

import (
	"errors"
	"math"
	"os"
	"testing"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
)

type setattrCloseTrackingFS struct {
	billy.Filesystem
	closes      int
	truncateErr error
}

func (filesystem *setattrCloseTrackingFS) OpenFile(
	filename string,
	flag int,
	perm os.FileMode,
) (billy.File, error) {
	file, err := filesystem.Filesystem.OpenFile(filename, flag, perm)
	if err != nil {
		return nil, err
	}
	return &setattrCloseTrackingFile{
		File:        file,
		filesystem:  filesystem,
		truncateErr: filesystem.truncateErr,
	}, nil
}

type setattrCloseTrackingFile struct {
	billy.File
	filesystem  *setattrCloseTrackingFS
	truncateErr error
}

func (file *setattrCloseTrackingFile) Truncate(size int64) error {
	if file.truncateErr != nil {
		return file.truncateErr
	}
	return file.File.Truncate(size)
}

func (file *setattrCloseTrackingFile) Close() error {
	file.filesystem.closes++
	return file.File.Close()
}

func TestSetFileAttributesClosesFileOnEverySetSizeReturn(t *testing.T) {
	for _, test := range []struct {
		name        string
		size        uint64
		truncateErr error
	}{
		{name: "oversized", size: math.MaxInt64 + 1},
		{name: "truncate-error", size: 1, truncateErr: errors.New("truncate failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := osfs.New(t.TempDir())
			file, err := base.Create("data")
			if err != nil {
				t.Fatalf("create data: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Fatalf("close fixture: %v", err)
			}
			filesystem := &setattrCloseTrackingFS{
				Filesystem:  base,
				truncateErr: test.truncateErr,
			}
			attributes := &SetFileAttributes{SetSize: &test.size}
			if err := attributes.Apply(nil, filesystem, "data"); err == nil {
				t.Fatal("SETATTR unexpectedly succeeded")
			}
			if filesystem.closes != 1 {
				t.Fatalf("SETATTR close count=%d, want 1", filesystem.closes)
			}
		})
	}
}
