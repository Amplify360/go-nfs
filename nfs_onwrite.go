package nfs

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
)

type writeArgs struct {
	Handle []byte
	Offset uint64
	Count  uint32
	How    uint32
	Data   []byte
}

func onWrite(ctx context.Context, w *response, userHandle Handler) error {
	w.errorFmt = wccDataErrorFormatter
	var req writeArgs
	if err := xdr.Read(w.req.Body, &req); err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}

	fs, path, err := userHandle.FromHandle(req.Handle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	if !billy.CapabilityCheck(fs, billy.WriteCapability) {
		return &NFSStatusError{NFSStatusROFS, os.ErrPermission}
	}
	if len(req.Data) > math.MaxInt32 || req.Count > math.MaxInt32 {
		return &NFSStatusError{NFSStatusFBig, os.ErrInvalid}
	}
	if req.How != uint32(WriteUnstable) &&
		req.How != uint32(WriteDataSync) &&
		req.How != uint32(WriteFileSync) {
		return &NFSStatusError{NFSStatusInval, os.ErrInvalid}
	}

	// stat first for pre-op wcc.
	fullPath := fs.Join(path...)
	info, err := fs.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &NFSStatusError{NFSStatusNoEnt, err}
		}
		return &NFSStatusError{NFSStatusAccess, err}
	}
	if !info.Mode().IsRegular() {
		return &NFSStatusError{NFSStatusInval, os.ErrInvalid}
	}
	preOpCache := ToFileAttribute(info, fullPath).AsCache()

	end := req.Count
	if len(req.Data) < int(end) {
		end = uint32(len(req.Data))
	}
	data := req.Data[:end]
	writtenCount := 0
	committed := WriteFileSync
	if handler, ok := userHandle.(WriteCommitHandler); ok {
		writtenCount, committed, err = handler.Write(
			ctx,
			fs,
			path,
			req.Handle,
			req.Offset,
			data,
			WriteStability(req.How),
		)
		if err != nil {
			Log.Errorf("Error writing: %v", err)
			return writeStatusError(err)
		}
		if writtenCount < 0 || writtenCount > len(data) ||
			(committed != WriteUnstable &&
				committed != WriteDataSync &&
				committed != WriteFileSync) {
			return &NFSStatusError{NFSStatusServerFault, os.ErrInvalid}
		}
	} else {
		// Preserve the original synchronous behavior unless the handler
		// explicitly opts into WRITE/COMMIT coordination.
		file, err := fs.OpenFile(fs.Join(path...), os.O_RDWR, info.Mode().Perm())
		if err != nil {
			return &NFSStatusError{NFSStatusAccess, err}
		}
		if req.Offset > 0 {
			if _, err := file.Seek(int64(req.Offset), io.SeekStart); err != nil {
				return &NFSStatusError{NFSStatusIO, err}
			}
		}
		writtenCount, err = file.Write(data)
		if err != nil {
			Log.Errorf("Error writing: %v", err)
			return &NFSStatusError{statusFromWriteError(err), err}
		}
		if err := file.Close(); err != nil {
			Log.Errorf("error closing: %v", err)
			return &NFSStatusError{statusFromWriteError(err), err}
		}
	}

	writer := bytes.NewBuffer([]byte{})
	if err := xdr.Write(writer, uint32(NFSStatusOk)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	if err := WriteWcc(writer, preOpCache, tryStat(fs, path)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := xdr.Write(writer, uint32(writtenCount)); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := xdr.Write(writer, committed); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	if err := xdr.Write(writer, w.Server.ID); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}

	if err := w.Write(writer.Bytes()); err != nil {
		return &NFSStatusError{NFSStatusServerFault, err}
	}
	return nil
}
