package nfs

import (
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
)

func TestEmptyLookupRejectsEmptyHandle(t *testing.T) {
	fs := memfs.New()
	file, err := fs.Create("file")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupSuccessResponse(nil, []string{"file"}, nil, fs); err == nil {
		t.Fatal("a failed handle binding is encoded as a successful LOOKUP with an empty handle")
	}
	if _, err := lookupSuccessResponse([]byte("valid-handle"), []string{"file"}, nil, fs); err != nil {
		t.Fatal(err)
	}
}
