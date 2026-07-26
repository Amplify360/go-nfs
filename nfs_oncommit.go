package nfs

import (
	"bytes"
	"context"
	"os"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
)

type commitArgs struct {
	Handle []byte
	Offset uint64
	Count  uint32
}

// onCommit is a no-op unless the handler opts into WRITE/COMMIT coordination.
func onCommit(ctx context.Context, w *response, userHandle Handler) error {
	w.errorFmt = wccDataErrorFormatter
	var req commitArgs
	if err := xdr.Read(w.req.Body, &req); err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}

	if handler, ok := userHandle.(RawCommitHandler); ok {
		handled, err := handler.CommitHandle(ctx, req.Handle, req.Offset, req.Count)
		if err != nil {
			Log.Errorf("Error committing raw handle: %v", err)
			return writeStatusError(err)
		}
		if handled {
			return writeCommitResponse(w, nil)
		}
	}

	fs, path, err := userHandle.FromHandle(req.Handle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	if !billy.CapabilityCheck(fs, billy.WriteCapability) {
		return &NFSStatusError{NFSStatusServerFault, os.ErrPermission}
	}
	if handler, ok := userHandle.(WriteCommitHandler); ok {
		if err := handler.Commit(ctx, fs, path, req.Handle, req.Offset, req.Count); err != nil {
			Log.Errorf("Error committing: %v", err)
			return writeStatusError(err)
		}
	}

	return writeCommitResponse(w, tryStat(fs, path))
}

func writeCommitResponse(w *response, post *FileAttribute) error {
	writer := bytes.NewBuffer([]byte{})
	if err := xdr.Write(writer, uint32(NFSStatusOk)); err != nil {
		return err
	}

	// no pre-op cache data.
	if err := xdr.Write(writer, uint32(0)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := WritePostOpAttrs(writer, post); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	// write the 8 bytes of write verification.
	if err := xdr.Write(writer, w.Server.ID); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	if err := w.Write(writer.Bytes()); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	return nil
}
