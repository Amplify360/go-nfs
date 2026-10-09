package nfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
	"github.com/willscott/go-nfs/helpers/memfs"
)

type contextReadTestFS struct {
	billy.Filesystem
	ctx      context.Context
	observed context.Context
}

func (filesystem *contextReadTestFS) WithRequestContext(ctx context.Context) billy.Filesystem {
	filesystem.observed = ctx
	view := *filesystem
	view.ctx = ctx
	return &view
}

func (filesystem *contextReadTestFS) Lstat(string) (os.FileInfo, error) {
	<-filesystem.ctx.Done()
	return nil, filesystem.ctx.Err()
}

type requestDeadlineTestHandler struct {
	*fixedHandleTestHandler
	budgetCalls int
}

func (handler *requestDeadlineTestHandler) ReadRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	handler.budgetCalls++
	return context.WithTimeout(ctx, 20*time.Millisecond)
}

func TestRequestBudgetExpiresWorkButStillSendsNFSFailure(t *testing.T) {
	filesystem := &contextReadTestFS{Filesystem: memfs.New()}
	handler := &requestDeadlineTestHandler{fixedHandleTestHandler: &fixedHandleTestHandler{filesystem: filesystem}}
	body := bytes.NewBuffer(nil)
	if err := xdr.Write(body, []byte("handle")); err != nil {
		t.Fatal(err)
	}
	response := newTestResponse(body, [8]byte{})
	response.req.Body = &io.LimitedReader{R: body, N: int64(body.Len())}
	response.Server.Handler = handler
	response.req.Header.Prog = nfsServiceID
	response.req.Header.Proc = uint32(NFSProcedureGetAttr)
	connectionContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	if err := response.conn.handle(connectionContext, response); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("unbounded work: %v", elapsed)
	}
	if handler.budgetCalls != 1 || filesystem.observed == nil || !errors.Is(filesystem.observed.Err(), context.DeadlineExceeded) || connectionContext.Err() != nil {
		t.Fatal("deadline not scoped to filesystem work")
	}
	decoded := decodeTestResponseBody(t, response)
	var status NFSStatus
	if err := xdr.Read(decoded, &status); err != nil || status != NFSStatusIO {
		t.Fatalf("timeout wire status=%d,%v", status, err)
	}
	// The same connection remains usable to send an unrelated reply.
	next := newTestResponse(bytes.NewBuffer(nil), [8]byte{})
	next.conn = response.conn
	next.req.Body = &io.LimitedReader{R: next.req.Body, N: 0}
	next.req.Header.Prog = nfsServiceID
	next.req.Header.Proc = uint32(NFSProcedureNull)
	if err := next.conn.handle(connectionContext, next); err != nil || !next.responded || handler.budgetCalls != 1 {
		t.Fatal("read timeout disabled further replies", err)
	}
}

func TestRequestContextPreservesEarlierCancellationAndOptionalFilesystem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	filesystem := &contextReadTestFS{Filesystem: memfs.New()}
	handler := boundRequestContextHandler(&fixedHandleTestHandler{filesystem: filesystem}, ctx)
	view, _, err := handler.FromHandle([]byte("handle"))
	if err != nil || view.(*contextReadTestFS).ctx != ctx {
		t.Fatal("request cancellation was replaced", err)
	}
	plain := memfs.New()
	other := boundRequestContextHandler(&fixedHandleTestHandler{filesystem: plain}, ctx)
	view, _, err = other.FromHandle([]byte("handle"))
	if err != nil || view != plain {
		t.Fatal("ordinary filesystem behavior changed", err)
	}
}

type requestBudgetCachingFixture struct {
	Handler
	verifiers, hits int
}

func (fixture *requestBudgetCachingFixture) VerifierFor(path string, contents []fs.FileInfo) uint64 {
	fixture.verifiers++
	return 42
}

func (fixture *requestBudgetCachingFixture) DataForVerifier(path string, verifier uint64) []fs.FileInfo {
	fixture.hits++
	return []fs.FileInfo{}
}

func TestReadBudgetPreservesOptionalVerifierCache(t *testing.T) {
	fixture := &requestBudgetCachingFixture{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bounded := boundRequestContextHandler(fixture, ctx)
	caching, ok := bounded.(CachingHandler)
	if !ok {
		t.Fatal("request budget discarded directory verifier interface")
	}
	if caching.VerifierFor("dir", nil) != 42 || caching.DataForVerifier("dir", 42) == nil || fixture.verifiers != 1 || fixture.hits != 1 {
		t.Fatal("request budget bypassed original verifier state")
	}
	if bounded.(cachingRequestContextHandler).ctx != ctx {
		t.Fatal("caching wrapper lost request context")
	}
	plain := boundRequestContextHandler(struct{ Handler }{}, ctx)
	if _, ok := plain.(CachingHandler); ok {
		t.Fatal("invented verifier support for ordinary handler")
	}
}

func TestReadBudgetProcedureCoversReadsNeverWrites(t *testing.T) {
	readOnly := map[uint32]bool{1: true, 3: true, 4: true, 5: true, 6: true, 16: true, 17: true, 18: true, 19: true, 20: true}
	for procedure := uint32(0); procedure <= 22; procedure++ {
		if actual := readOnlyProcedure(procedure); actual != readOnly[procedure] {
			t.Errorf("procedure %d: budget=%v expected=%v", procedure, actual, readOnly[procedure])
		}
	}
	if readOnlyProcedure(^uint32(0)) {
		t.Fatal("unknown procedure admitted")
	}
}
