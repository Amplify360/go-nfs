package nfs

import (
	"context"
	"io/fs"
	"net"

	billy "github.com/go-git/go-billy/v5"
)

// Handler represents the interface of the file system / vfs being exposed over NFS
type Handler interface {
	// Required methods

	Mount(context.Context, net.Conn, MountRequest) (MountStatus, billy.Filesystem, []AuthFlavor)

	// Change can return 'nil' if filesystem is read-only
	// If the returned value can be cast to `UnixChange`, mknod and link RPCs will be available.
	Change(billy.Filesystem) billy.Change

	// Optional methods - generic helpers or trivial implementations can be sufficient depending on use case.

	// Fill in information about a file system's free space.
	FSStat(context.Context, billy.Filesystem, *FSStat) error

	// represent file objects as opaque references
	// Can be safely implemented via helpers/cachinghandler.
	ToHandle(fs billy.Filesystem, path []string) []byte
	FromHandle(fh []byte) (billy.Filesystem, []string, error)
	InvalidateHandle(billy.Filesystem, []byte) error

	// How many handles can be safely maintained by the handler.
	HandleLimit() int
}

// WriteStability is the durability level requested by an NFSv3 WRITE or
// reported by its response.
type WriteStability uint32

const (
	// WriteUnstable permits the server to acknowledge a write before it has
	// reached stable storage. The client must issue COMMIT before treating the
	// data as durable.
	WriteUnstable WriteStability = iota
	// WriteDataSync requests that the written data, but not necessarily all
	// file metadata, reach stable storage before the response.
	WriteDataSync
	// WriteFileSync requests that the written data and file metadata reach
	// stable storage before the response.
	WriteFileSync
)

// WriteCommitHandler optionally overrides the default synchronous NFSv3 WRITE
// and no-op COMMIT implementation. Implementations may retain write state
// between calls, but must copy any handle, path, or data they retain after the
// method returns.
//
// Write returns the number of bytes accepted and the durability level actually
// provided. Commit must make the requested byte range stable before returning
// nil. COMMIT may arrive through a different connection than WRITE, so retained
// state must not be connection-local.
//
// Handlers that do not implement this interface retain the existing behavior:
// every WRITE opens, writes, and closes the backing file and reports
// WriteFileSync; COMMIT is a no-op.
type WriteCommitHandler interface {
	Write(
		ctx context.Context,
		filesystem billy.Filesystem,
		path []string,
		handle []byte,
		offset uint64,
		data []byte,
		stability WriteStability,
	) (written int, committed WriteStability, err error)
	Commit(
		ctx context.Context,
		filesystem billy.Filesystem,
		path []string,
		handle []byte,
		offset uint64,
		count uint32,
	) error
}

// UnixChange extends the billy `Change` interface with support for special files.
type UnixChange interface {
	billy.Change
	Mknod(path string, mode uint32, major uint32, minor uint32) error
	Mkfifo(path string, mode uint32) error
	Socket(path string) error
	Link(path string, link string) error
}

// CachingHandler represents the optional caching work that a user may wish to over-ride with
// their own implementations, but which can be otherwise provided through defaults.
type CachingHandler interface {
	VerifierFor(path string, contents []fs.FileInfo) uint64

	// fs.FileInfo needs to be sorted by Name(), nil in case of a cache-miss
	DataForVerifier(path string, verifier uint64) []fs.FileInfo
}
