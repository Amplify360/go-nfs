package nfs

import (
	"errors"
	"os"
	"syscall"
)

// Read errors must describe the failed operation, not infer a file type or
// permission failure from an unavailable backend. Explicit protocol statuses
// remain authoritative; this helper has no storage or caller-specific policy.
func readStatusError(err error) *NFSStatusError {
	var protocol *NFSStatusError
	if errors.As(err, &protocol) {
		return &NFSStatusError{protocol.NFSStatus, err}
	}
	status := NFSStatusIO
	switch {
	case errors.Is(err, syscall.ESTALE):
		status = NFSStatusStale
	case errors.Is(err, os.ErrNotExist):
		status = NFSStatusNoEnt
	case errors.Is(err, os.ErrPermission):
		status = NFSStatusAccess
	case errors.Is(err, syscall.ENOTDIR):
		status = NFSStatusNotDir
	case errors.Is(err, syscall.EISDIR):
		status = NFSStatusIsDir
	case errors.Is(err, syscall.EINVAL):
		status = NFSStatusInval
	case errors.Is(err, syscall.EAGAIN):
		status = NFSStatusJukebox
	}
	return &NFSStatusError{status, err}
}
