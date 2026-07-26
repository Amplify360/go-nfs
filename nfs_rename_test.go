package nfs

import (
	"bytes"
	"context"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs/helpers/memfs"
)

type renameInvalidationTestHandler struct {
	*fixedHandleTestHandler
	invalidated [][]byte
}

func (handler *renameInvalidationTestHandler) ToHandle(_ billy.Filesystem, path []string) []byte {
	return []byte(handler.filesystem.Join(path...))
}

func (handler *renameInvalidationTestHandler) InvalidateHandle(_ billy.Filesystem, handle []byte) error {
	handler.invalidated = append(handler.invalidated, append([]byte(nil), handle...))
	return nil
}

func TestRenameInvalidatesSourceAndReplacedDestinationHandles(t *testing.T) {
	filesystem := memfs.New()
	for _, name := range []string{"source", "destination"} {
		file, err := filesystem.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close %s: %v", name, err)
		}
	}
	handler := &renameInvalidationTestHandler{
		fixedHandleTestHandler: &fixedHandleTestHandler{
			filesystem: filesystem,
			path:       nil,
		},
	}
	request := bytes.NewBuffer(nil)
	for _, argument := range []DirOpArg{
		{Handle: []byte("from-directory"), Filename: []byte("source")},
		{Handle: []byte("to-directory"), Filename: []byte("destination")},
	} {
		encoded := encodeTestRequest(t, argument)
		request.Write(encoded.Bytes())
	}
	response := newTestResponse(request, [8]byte{})

	if err := onRename(context.Background(), response, handler); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(handler.invalidated) != 2 ||
		!bytes.Equal(handler.invalidated[0], []byte("source")) ||
		!bytes.Equal(handler.invalidated[1], []byte("destination")) {
		t.Fatalf("invalidated handles=%q, want source and replaced destination", handler.invalidated)
	}
}
