package nfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
	"github.com/willscott/go-nfs/helpers/memfs"
)

type readMetadataTestFS struct {
	billy.Filesystem
	captured    bool
	metadata    os.FileInfo
	metadataErr error
	statCalls   int
	file        *readMetadataTestFile
}
type readMetadataTestFile struct {
	billy.File
	parent *readMetadataTestFS
	closed bool
}

func (f *readMetadataTestFile) ReadMetadata() (os.FileInfo, error) {
	return f.parent.metadata, f.parent.metadataErr
}
func (f *readMetadataTestFile) Close() error { f.closed = true; return f.File.Close() }
func (f *readMetadataTestFS) Stat(name string) (os.FileInfo, error) {
	f.statCalls++
	if f.captured {
		return nil, errors.New("duplicate filesystem Stat")
	}
	return f.Filesystem.Stat(name)
}
func (f *readMetadataTestFS) Open(name string) (billy.File, error) {
	file, err := f.Filesystem.Open(name)
	if err != nil {
		return nil, err
	}
	if !f.captured {
		return file, nil
	}
	f.file = &readMetadataTestFile{File: file, parent: f}
	return f.file, nil
}
func TestReadMetadataSurface(t *testing.T) {
	for _, mode := range []string{"captured", "fallback", "error", "nil"} {
		t.Run(mode, func(t *testing.T) {
			base := memfs.New()
			file, err := base.Create("file")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write([]byte("old")); err != nil {
				t.Fatal(err)
			}
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}
			info, err := base.Stat("file")
			if err != nil {
				t.Fatal(err)
			}
			fs := &readMetadataTestFS{Filesystem: base, captured: mode != "fallback", metadata: info}
			if mode == "error" {
				fs.metadataErr = errors.New("metadata unavailable")
			}
			if mode == "nil" {
				fs.metadata = nil
			}
			body := bytes.NewBuffer(nil)
			if err = xdr.Write(body, nfsReadArgs{Handle: []byte("handle"), Count: 32}); err != nil {
				t.Fatal(err)
			}
			response := newTestResponse(body, [8]byte{1})
			handler := &fixedHandleTestHandler{filesystem: fs, path: []string{"file"}}
			err = onRead(context.Background(), response, handler)
			if mode == "error" || mode == "nil" {
				if err == nil {
					t.Fatal("invalid metadata accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				decoded := decodeTestResponseBody(t, response)
				decodeTestOKStatus(t, decoded)
				var present uint32
				if err = xdr.Read(decoded, &present); err != nil || present != 1 {
					t.Fatalf("attributes missing: %v", err)
				}
				var attrs FileAttribute
				if err = xdr.Read(decoded, &attrs); err != nil || attrs.Filesize != 3 {
					t.Fatalf("wrong captured size: %v %+v", err, attrs)
				}
				var data nfsReadResponse
				if err = xdr.Read(decoded, &data); err != nil || string(data.Data) != "old" || data.Count != 3 || data.EOF != 1 {
					t.Fatalf("wrong bytes: %v %+v", err, data)
				}
			}
			if fs.captured && (fs.statCalls != 0 || !fs.file.closed) {
				t.Fatal("duplicated metadata or leaked file")
			}
			if !fs.captured && fs.statCalls != 1 {
				t.Fatal("fallback Stat changed")
			}
		})
	}
}

type lookupMetadataTestFS struct {
	billy.Filesystem
	fileCalls int
	fail      bool
}

func (f *lookupMetadataTestFS) Lstat(name string) (os.FileInfo, error) {
	if name == "file" {
		f.fileCalls++
		if f.fail {
			return nil, errors.New("metadata unavailable")
		}
	}
	return f.Filesystem.Lstat(name)
}

type lookupMetadataTestHandler struct{ *fixedHandleTestHandler }

func (h *lookupMetadataTestHandler) ToHandle(billy.Filesystem, []string) []byte {
	return []byte("file-handle")
}
func TestLookupMetadataReusedOnlyWithinRequest(t *testing.T) {
	base := memfs.New()
	file, err := base.Create("file")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	fs := &lookupMetadataTestFS{Filesystem: base}
	handler := &lookupMetadataTestHandler{&fixedHandleTestHandler{filesystem: fs}}
	for i := 0; i < 2; i++ {
		body := bytes.NewBuffer(nil)
		if err = xdr.Write(body, DirOpArg{Handle: []byte("root"), Filename: []byte("file")}); err != nil {
			t.Fatal(err)
		}
		if err = onLookup(context.Background(), newTestResponse(body, [8]byte{1}), handler); err != nil {
			t.Fatal(err)
		}
		if fs.fileCalls != i+1 {
			t.Fatal("duplicate lookup or cached across RPCs")
		}
	}
	fs.fail = true
	body := bytes.NewBuffer(nil)
	xdr.Write(body, DirOpArg{Handle: []byte("root"), Filename: []byte("file")})
	if err = onLookup(context.Background(), newTestResponse(body, [8]byte{1}), handler); err == nil {
		t.Fatal("metadata failure accepted")
	}
}
