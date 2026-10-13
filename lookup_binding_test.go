package nfs

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
	"github.com/willscott/go-nfs/helpers/memfs"
)

type bindingMetadataHandler struct {
	Handler
	fs         billy.Filesystem
	bindingErr error
}

func (h *bindingMetadataHandler) FromHandle(handle []byte) (billy.Filesystem, []string, error) {
	if string(handle) == "root" {
		return h.fs, nil, nil
	}
	return h.fs, []string{"file"}, h.bindingErr
}

func (h *bindingMetadataHandler) ToHandle(fs billy.Filesystem, path []string) []byte {
	f, err := fs.OpenFile(fs.Join(path...), os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		panic(err)
	}
	if _, err = f.Write([]byte("new-version")); err != nil {
		panic(err)
	}
	f.Close()
	return []byte("bound")
}

func TestLookupBoundHandleMetadata(t *testing.T) {
	base := memfs.New()
	f, err := base.Create("file")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("old"))
	f.Close()
	h := &bindingMetadataHandler{fs: base}
	body := bytes.NewBuffer(nil)
	xdr.Write(body, DirOpArg{Handle: []byte("root"), Filename: []byte("file")})
	response := newTestResponse(body, [8]byte{1})
	if err := onLookup(context.Background(), response, h); err != nil {
		t.Fatal(err)
	}
	reply := decodeTestResponseBody(t, response)
	decodeTestOKStatus(t, reply)
	var handle []byte
	var present uint32
	var info FileAttribute
	if err := xdr.Read(reply, &handle); err != nil {
		t.Fatal(err)
	}
	if err := xdr.Read(reply, &present); err != nil || present != 1 {
		t.Fatal("metadata absent", err)
	}
	if err := xdr.Read(reply, &info); err != nil {
		t.Fatal(err)
	}
	if info.Filesize != 11 {
		t.Fatalf("LOOKUP labels its bound handle with pre-binding metadata: size=%d, want 11", info.Filesize)
	}
}
