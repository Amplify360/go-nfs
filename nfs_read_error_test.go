package nfs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs/helpers/memfs"
)

// Faults enter through the filesystem interface, not through a mocked NFS
// status helper. Each assertion checks the handler's actual formatted wire code.
type readErrorTestFS struct {
	billy.Filesystem
	phase      string
	cause      error
	fileClosed bool
	statCalls  int
}

func (f *readErrorTestFS) Lstat(name string) (os.FileInfo, error) {
	if f.phase == "getattr" || (f.phase == "lookup-parent" && (name == "." || name == "")) || (f.phase == "lookup-child" && name == "file") {
		return nil, f.cause
	}
	return f.Filesystem.Lstat(name)
}

func (f *readErrorTestFS) Stat(name string) (os.FileInfo, error) {
	f.statCalls++
	if f.phase == "read-stat" {
		return nil, f.cause
	}
	return f.Filesystem.Stat(name)
}

func (f *readErrorTestFS) ReadDir(name string) ([]os.FileInfo, error) {
	if f.phase == "readdir" || f.phase == "readdirplus" {
		return nil, f.cause
	}
	return f.Filesystem.ReadDir(name)
}

func (f *readErrorTestFS) Readlink(name string) (string, error) {
	if f.phase == "readlink" {
		return "", f.cause
	}
	return f.Filesystem.Readlink(name)
}

func (f *readErrorTestFS) Open(name string) (billy.File, error) {
	if f.phase == "read-open" {
		return nil, f.cause
	}
	file, err := f.Filesystem.Open(name)
	if err != nil {
		return nil, err
	}
	wrapped := &readErrorTestFile{File: file, parent: f}
	if f.phase == "read-metadata" {
		return &readErrorMetadataTestFile{wrapped}, nil
	}
	return wrapped, nil
}

type readErrorTestFile struct {
	billy.File
	parent *readErrorTestFS
}

func (f *readErrorTestFile) Close() error { f.parent.fileClosed = true; return f.File.Close() }
func (f *readErrorTestFile) ReadAt(data []byte, offset int64) (int, error) {
	if f.parent.phase == "read-data" {
		return 0, f.parent.cause
	}
	return f.File.ReadAt(data, offset)
}

type readErrorMetadataTestFile struct{ *readErrorTestFile }

func (f *readErrorMetadataTestFile) ReadMetadata() (os.FileInfo, error) { return nil, f.parent.cause }

func TestReadFilesystemErrorsPreserveWireStatus(t *testing.T) {
	cases := []struct {
		name   string
		cause  error
		status NFSStatus
	}{
		{"io", syscall.EIO, NFSStatusIO},
		{"deadline", context.DeadlineExceeded, NFSStatusIO},
		{"cancelled", context.Canceled, NFSStatusIO},
		{"unknown-backend", errors.New("backend unavailable"), NFSStatusIO},
		{"stale", syscall.ESTALE, NFSStatusStale},
		{"missing", syscall.ENOENT, NFSStatusNoEnt},
		{"access", syscall.EACCES, NFSStatusAccess},
		{"not-directory", syscall.ENOTDIR, NFSStatusNotDir},
		{"is-directory", syscall.EISDIR, NFSStatusIsDir},
		{"invalid", syscall.EINVAL, NFSStatusInval},
		{"temporary", syscall.EAGAIN, NFSStatusJukebox},
		{"explicit-status", &NFSStatusError{NFSStatusROFS, syscall.EIO}, NFSStatusROFS},
	}
	for _, phase := range []string{"getattr", "lookup-parent", "lookup-child", "readdir", "readdirplus", "readlink", "read-open", "read-stat", "read-metadata", "read-data"} {
		for _, test := range cases {
			t.Run(phase+"/"+test.name, func(t *testing.T) {
				base := memfs.New()
				file, err := base.Create("file")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("contents")); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				f := &readErrorTestFS{Filesystem: base, phase: phase, cause: &os.PathError{Op: phase, Path: "file", Err: fmt.Errorf("backend: %w", test.cause)}}
				h := &lookupMetadataTestHandler{&fixedHandleTestHandler{filesystem: f, path: []string{"file"}}}
				var r *response
				switch phase {
				case "getattr":
					r = newTestResponse(encodeTestRequest(t, []byte("handle")), [8]byte{1})
					err = onGetAttr(context.Background(), r, h)
				case "lookup-parent", "lookup-child":
					h.path = nil
					r = newTestResponse(encodeTestRequest(t, DirOpArg{Handle: []byte("handle"), Filename: []byte("file")}), [8]byte{1})
					err = onLookup(context.Background(), r, h)
				case "readdir":
					h.path = nil
					r = newTestResponse(encodeTestRequest(t, readDirArgs{Handle: []byte("handle"), Count: 4096}), [8]byte{1})
					err = onReadDir(context.Background(), r, h)
				case "readdirplus":
					h.path = nil
					r = newTestResponse(encodeTestRequest(t, readDirPlusArgs{Handle: []byte("handle"), DirCount: 512, MaxCount: 4096}), [8]byte{1})
					err = onReadDirPlus(context.Background(), r, h)
				case "readlink":
					r = newTestResponse(encodeTestRequest(t, []byte("handle")), [8]byte{1})
					err = onReadLink(context.Background(), r, h)
				default:
					r = newTestResponse(encodeTestRequest(t, nfsReadArgs{Handle: []byte("handle"), Count: 4}), [8]byte{1})
					err = onRead(context.Background(), r, h)
				}
				if err == nil {
					t.Fatal("filesystem failure accepted")
				}
				wire, marshalErr := r.errorFmt(err).MarshalBinary()
				if marshalErr != nil || len(wire) < 4 || NFSStatus(binary.BigEndian.Uint32(wire[:4])) != test.status {
					t.Fatalf("wire=%x error=%v want=%v", wire, marshalErr, test.status)
				}
				if !errors.Is(err, test.cause) {
					t.Fatal("original error lost", err)
				}
				if phase == "read-stat" || phase == "read-metadata" || phase == "read-data" {
					if !f.fileClosed {
						t.Fatal("failure leaked backing file")
					}
				}
				if phase == "readlink" && (errors.Is(test.cause, syscall.EIO) || errors.Is(test.cause, context.DeadlineExceeded) || errors.Is(test.cause, context.Canceled)) && f.statCalls != 0 {
					t.Fatal("storage failure started a second metadata operation")
				}
			})
		}
	}
}
