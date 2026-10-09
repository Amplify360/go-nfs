package nfs

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestTemporaryFilesystemErrorsRemainRetryableOnWire(t *testing.T) {
	for _, test := range []struct {
		name   string
		status NFSStatus
		cause  error
		want   NFSStatus
	}{
		{"create-access", NFSStatusAccess, syscall.EAGAIN, NFSStatusJukebox},
		{"rename-io", NFSStatusIO, &os.PathError{Op: "rename", Path: "file", Err: syscall.EAGAIN}, NFSStatusJukebox},
		{"wrapped-setattr", NFSStatusIO, fmt.Errorf("temporary: %w", syscall.EAGAIN), NFSStatusJukebox},
		{"real-access", NFSStatusAccess, syscall.EACCES, NFSStatusAccess},
		{"real-io", NFSStatusIO, syscall.EIO, NFSStatusIO},
		{"explicit-rofs", NFSStatusROFS, syscall.EAGAIN, NFSStatusROFS},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &NFSStatusError{test.status, test.cause}
			for name, formatter := range map[string]func(error) RPCError{"basic": basicErrorFormatter, "attributes": opAttrErrorFormatter, "mutation": wccDataErrorFormatter} {
				data, marshalErr := formatter(err).MarshalBinary()
				if marshalErr != nil || len(data) < 4 || NFSStatus(binary.BigEndian.Uint32(data[:4])) != test.want {
					t.Fatalf("%s wire=%x error=%v want=%v", name, data, marshalErr, test.want)
				}
			}
		})
	}
}
