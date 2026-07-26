package nfs

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"github.com/willscott/go-nfs-client/nfs/xdr"
	"github.com/willscott/go-nfs/helpers/memfs"
)

type fixedHandleTestHandler struct {
	Handler
	filesystem billy.Filesystem
	path       []string
}

func (handler *fixedHandleTestHandler) FromHandle([]byte) (billy.Filesystem, []string, error) {
	return handler.filesystem, append([]string(nil), handler.path...), nil
}

type writeCall struct {
	filesystem billy.Filesystem
	path       []string
	handle     []byte
	offset     uint64
	data       []byte
	stability  WriteStability
}

type commitCall struct {
	filesystem billy.Filesystem
	path       []string
	handle     []byte
	offset     uint64
	count      uint32
}

type writeCommitTestHandler struct {
	*fixedHandleTestHandler
	writeCalls     []writeCall
	commitCalls    []commitCall
	writeCount     int
	writeStability WriteStability
}

func (handler *writeCommitTestHandler) Write(
	_ context.Context,
	filesystem billy.Filesystem,
	path []string,
	handle []byte,
	offset uint64,
	data []byte,
	stability WriteStability,
) (int, WriteStability, error) {
	handler.writeCalls = append(handler.writeCalls, writeCall{
		filesystem: filesystem,
		path:       append([]string(nil), path...),
		handle:     append([]byte(nil), handle...),
		offset:     offset,
		data:       append([]byte(nil), data...),
		stability:  stability,
	})
	return handler.writeCount, handler.writeStability, nil
}

func (handler *writeCommitTestHandler) Commit(
	_ context.Context,
	filesystem billy.Filesystem,
	path []string,
	handle []byte,
	offset uint64,
	count uint32,
) error {
	handler.commitCalls = append(handler.commitCalls, commitCall{
		filesystem: filesystem,
		path:       append([]string(nil), path...),
		handle:     append([]byte(nil), handle...),
		offset:     offset,
		count:      count,
	})
	return nil
}

func TestWriteCommitHandlerControlsWriteStability(t *testing.T) {
	tests := []struct {
		name      string
		requested WriteStability
		committed WriteStability
	}{
		{name: "unstable-to-data-sync", requested: WriteUnstable, committed: WriteDataSync},
		{name: "data-sync-to-file-sync", requested: WriteDataSync, committed: WriteFileSync},
		{name: "file-sync-to-unstable", requested: WriteFileSync, committed: WriteUnstable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filesystem, path := newWriteCommitTestFilesystem(t)
			handler := &writeCommitTestHandler{
				fixedHandleTestHandler: &fixedHandleTestHandler{
					filesystem: filesystem,
					path:       path,
				},
				writeCount:     4,
				writeStability: test.committed,
			}
			handle := []byte{1, 2, 3, 4}
			data := []byte("data")
			requestBody := encodeTestRequest(t, writeArgs{
				Handle: handle,
				Offset: 1<<40 + 17,
				Count:  uint32(len(data)),
				How:    uint32(test.requested),
				Data:   data,
			})
			serverID := [8]byte{8, 7, 6, 5, 4, 3, 2, 1}
			response := newTestResponse(requestBody, serverID)

			if err := onWrite(context.Background(), response, handler); err != nil {
				t.Fatalf("onWrite returned error: %v", err)
			}
			if len(handler.writeCalls) != 1 {
				t.Fatalf("Write call count = %d, want 1", len(handler.writeCalls))
			}
			call := handler.writeCalls[0]
			if call.filesystem != filesystem {
				t.Fatal("Write received a different filesystem")
			}
			if !reflect.DeepEqual(call.path, path) {
				t.Fatalf("Write path = %v, want %v", call.path, path)
			}
			if !bytes.Equal(call.handle, handle) {
				t.Fatalf("Write handle = %v, want %v", call.handle, handle)
			}
			if call.offset != 1<<40+17 {
				t.Fatalf("Write offset = %d, want %d", call.offset, uint64(1<<40+17))
			}
			if !bytes.Equal(call.data, data) {
				t.Fatalf("Write data = %q, want %q", call.data, data)
			}
			if call.stability != test.requested {
				t.Fatalf("Write stability = %v, want %v", call.stability, test.requested)
			}

			count, committed, verifier := decodeWriteResponse(t, response)
			if count != uint32(len(data)) {
				t.Fatalf("response count = %d, want %d", count, len(data))
			}
			if committed != test.committed {
				t.Fatalf("response stability = %v, want %v", committed, test.committed)
			}
			if verifier != serverID {
				t.Fatalf("response verifier = %x, want %x", verifier, serverID)
			}
		})
	}
}

func TestWriteWithoutOptionalHandlerRemainsFileSync(t *testing.T) {
	filesystem, path := newWriteCommitTestFilesystem(t)
	handler := &fixedHandleTestHandler{filesystem: filesystem, path: path}
	data := []byte("default write")
	requestBody := encodeTestRequest(t, writeArgs{
		Handle: []byte{4, 3, 2, 1},
		Offset: 0,
		Count:  uint32(len(data)),
		How:    uint32(WriteUnstable),
		Data:   data,
	})
	serverID := [8]byte{1, 3, 5, 7, 9, 11, 13, 15}
	response := newTestResponse(requestBody, serverID)

	if err := onWrite(context.Background(), response, handler); err != nil {
		t.Fatalf("onWrite returned error: %v", err)
	}
	count, committed, verifier := decodeWriteResponse(t, response)
	if count != uint32(len(data)) {
		t.Fatalf("response count = %d, want %d", count, len(data))
	}
	if committed != WriteFileSync {
		t.Fatalf("response stability = %v, want WriteFileSync", committed)
	}
	if verifier != serverID {
		t.Fatalf("response verifier = %x, want %x", verifier, serverID)
	}

	file, err := filesystem.Open(filesystem.Join(path...))
	if err != nil {
		t.Fatalf("open written file: %v", err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("written data = %q, want %q", got, data)
	}
}

func TestWriteCommitHandlerReceivesCompleteCommit(t *testing.T) {
	filesystem, path := newWriteCommitTestFilesystem(t)
	handler := &writeCommitTestHandler{
		fixedHandleTestHandler: &fixedHandleTestHandler{
			filesystem: filesystem,
			path:       path,
		},
	}
	handle := []byte{9, 8, 7, 6}
	args := commitArgs{
		Handle: handle,
		Offset: 1<<48 + 23,
		Count:  0xfedcba98,
	}
	requestBody := encodeTestRequest(t, args)
	serverID := [8]byte{15, 14, 13, 12, 11, 10, 9, 8}
	response := newTestResponse(requestBody, serverID)

	if err := onCommit(context.Background(), response, handler); err != nil {
		t.Fatalf("onCommit returned error: %v", err)
	}
	if requestBody.Len() != 0 {
		t.Fatalf("COMMIT left %d request bytes unread", requestBody.Len())
	}
	if len(handler.commitCalls) != 1 {
		t.Fatalf("Commit call count = %d, want 1", len(handler.commitCalls))
	}
	call := handler.commitCalls[0]
	if call.filesystem != filesystem {
		t.Fatal("Commit received a different filesystem")
	}
	if !reflect.DeepEqual(call.path, path) {
		t.Fatalf("Commit path = %v, want %v", call.path, path)
	}
	if !bytes.Equal(call.handle, handle) {
		t.Fatalf("Commit handle = %v, want %v", call.handle, handle)
	}
	if call.offset != args.Offset {
		t.Fatalf("Commit offset = %d, want %d", call.offset, args.Offset)
	}
	if call.count != args.Count {
		t.Fatalf("Commit count = %d, want %d", call.count, args.Count)
	}
	if verifier := decodeCommitResponse(t, response); verifier != serverID {
		t.Fatalf("response verifier = %x, want %x", verifier, serverID)
	}
}

func TestCommitWithoutOptionalHandlerRemainsNoOp(t *testing.T) {
	filesystem, path := newWriteCommitTestFilesystem(t)
	handler := &fixedHandleTestHandler{filesystem: filesystem, path: path}
	args := commitArgs{
		Handle: []byte{6, 7, 8, 9},
		Offset: 1234,
		Count:  5678,
	}
	requestBody := encodeTestRequest(t, args)
	serverID := [8]byte{2, 4, 6, 8, 10, 12, 14, 16}
	response := newTestResponse(requestBody, serverID)

	if err := onCommit(context.Background(), response, handler); err != nil {
		t.Fatalf("onCommit returned error: %v", err)
	}
	if requestBody.Len() != 0 {
		t.Fatalf("COMMIT left %d request bytes unread", requestBody.Len())
	}
	if verifier := decodeCommitResponse(t, response); verifier != serverID {
		t.Fatalf("response verifier = %x, want %x", verifier, serverID)
	}
}

func TestCommitRejectsIncompleteArgumentsBeforeDelegation(t *testing.T) {
	filesystem, path := newWriteCommitTestFilesystem(t)
	handler := &writeCommitTestHandler{
		fixedHandleTestHandler: &fixedHandleTestHandler{
			filesystem: filesystem,
			path:       path,
		},
	}
	requestBody := bytes.NewBuffer(nil)
	if err := xdr.Write(requestBody, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("encode incomplete request: %v", err)
	}
	response := newTestResponse(requestBody, [8]byte{})

	err := onCommit(context.Background(), response, handler)
	if err == nil {
		t.Fatal("onCommit accepted incomplete arguments")
	}
	statusErr, ok := err.(*NFSStatusError)
	if !ok {
		t.Fatalf("onCommit error type = %T, want *NFSStatusError", err)
	}
	if statusErr.NFSStatus != NFSStatusInval {
		t.Fatalf("onCommit NFS status = %v, want NFSStatusInval", statusErr.NFSStatus)
	}
	if len(handler.commitCalls) != 0 {
		t.Fatalf("Commit call count = %d, want 0", len(handler.commitCalls))
	}
}

func newWriteCommitTestFilesystem(t *testing.T) (billy.Filesystem, []string) {
	t.Helper()
	filesystem := memfs.New()
	path := []string{"file.bin"}
	file, err := filesystem.Create(filesystem.Join(path...))
	if err != nil {
		t.Fatalf("create test file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close test file: %v", err)
	}
	return filesystem, path
}

func encodeTestRequest(t *testing.T, value interface{}) *bytes.Buffer {
	t.Helper()
	body := bytes.NewBuffer(nil)
	if err := xdr.Write(body, value); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return body
}

func newTestResponse(body io.Reader, serverID [8]byte) *response {
	server := &Server{ID: serverID}
	return &response{
		conn: &conn{Server: server},
		req: &request{
			xid:  42,
			Body: body,
		},
		writer:   bytes.NewBuffer(nil),
		errorFmt: basicErrorFormatter,
	}
}

func decodeWriteResponse(t *testing.T, response *response) (uint32, WriteStability, [8]byte) {
	t.Helper()
	body := decodeTestResponseBody(t, response)
	decodeTestOKStatus(t, body)
	decodeTestWcc(t, body)

	var count uint32
	if err := xdr.Read(body, &count); err != nil {
		t.Fatalf("decode write count: %v", err)
	}
	var committed WriteStability
	if err := xdr.Read(body, &committed); err != nil {
		t.Fatalf("decode write stability: %v", err)
	}
	var verifier [8]byte
	if err := xdr.Read(body, &verifier); err != nil {
		t.Fatalf("decode write verifier: %v", err)
	}
	if body.Len() != 0 {
		t.Fatalf("WRITE response has %d trailing bytes", body.Len())
	}
	return count, committed, verifier
}

func decodeCommitResponse(t *testing.T, response *response) [8]byte {
	t.Helper()
	body := decodeTestResponseBody(t, response)
	decodeTestOKStatus(t, body)
	decodeTestWcc(t, body)

	var verifier [8]byte
	if err := xdr.Read(body, &verifier); err != nil {
		t.Fatalf("decode commit verifier: %v", err)
	}
	if body.Len() != 0 {
		t.Fatalf("COMMIT response has %d trailing bytes", body.Len())
	}
	return verifier
}

func decodeTestResponseBody(t *testing.T, response *response) *bytes.Reader {
	t.Helper()
	body := bytes.NewReader(response.writer.Bytes())
	var xid uint32
	if err := xdr.Read(body, &xid); err != nil {
		t.Fatalf("decode response xid: %v", err)
	}
	if xid != 42 {
		t.Fatalf("response xid = %d, want 42", xid)
	}
	var messageType uint32
	if err := xdr.Read(body, &messageType); err != nil {
		t.Fatalf("decode response message type: %v", err)
	}
	if messageType != 1 {
		t.Fatalf("response message type = %d, want reply", messageType)
	}
	var replyStatus uint32
	if err := xdr.Read(body, &replyStatus); err != nil {
		t.Fatalf("decode response reply status: %v", err)
	}
	if replyStatus != rpc.MsgAccepted {
		t.Fatalf("response reply status = %d, want accepted", replyStatus)
	}
	var verifier rpc.Auth
	if err := xdr.Read(body, &verifier); err != nil {
		t.Fatalf("decode response auth verifier: %v", err)
	}
	if !reflect.DeepEqual(verifier, rpc.AuthNull) {
		t.Fatalf("response auth verifier = %#v, want AuthNull", verifier)
	}
	var acceptStatus uint32
	if err := xdr.Read(body, &acceptStatus); err != nil {
		t.Fatalf("decode response accept status: %v", err)
	}
	if acceptStatus != uint32(ResponseCodeSuccess) {
		t.Fatalf("response accept status = %d, want success", acceptStatus)
	}
	return body
}

func decodeTestOKStatus(t *testing.T, body *bytes.Reader) {
	t.Helper()
	var status uint32
	if err := xdr.Read(body, &status); err != nil {
		t.Fatalf("decode NFS status: %v", err)
	}
	if status != uint32(NFSStatusOk) {
		t.Fatalf("NFS status = %d, want OK", status)
	}
}

func decodeTestWcc(t *testing.T, body *bytes.Reader) {
	t.Helper()
	var beforePresent uint32
	if err := xdr.Read(body, &beforePresent); err != nil {
		t.Fatalf("decode pre-op WCC presence: %v", err)
	}
	if beforePresent != 0 {
		var before FileCacheAttribute
		if err := xdr.Read(body, &before); err != nil {
			t.Fatalf("decode pre-op WCC: %v", err)
		}
	}
	var afterPresent uint32
	if err := xdr.Read(body, &afterPresent); err != nil {
		t.Fatalf("decode post-op WCC presence: %v", err)
	}
	if afterPresent != 0 {
		var after FileAttribute
		if err := xdr.Read(body, &after); err != nil {
			t.Fatalf("decode post-op WCC: %v", err)
		}
	}
}
