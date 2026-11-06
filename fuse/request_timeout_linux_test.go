// Copyright 2026 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"context"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/internal/testutil"
)

// blockingStatfsFS does not answer STATFS until release is called.
type blockingStatfsFS struct {
	RawFileSystem
	unblock     chan struct{}
	releaseOnce sync.Once
}

func (fs *blockingStatfsFS) StatFs(ctx context.Context, in *InHeader, out *StatfsOut) Status {
	<-fs.unblock
	return OK
}

func (fs *blockingStatfsFS) release() {
	fs.releaseOnce.Do(func() { close(fs.unblock) })
}

func TestRequestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the kernel to time out a request")
	}
	mnt := t.TempDir()
	fs := &blockingStatfsFS{RawFileSystem: NewDefaultRawFileSystem(), unblock: make(chan struct{})}
	srv, err := NewServer(fs, mnt, &MountOptions{RequestTimeout: 1, Debug: testutil.VerboseTest()})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Unmount()
	defer fs.release()

	if srv.KernelSettings().Flags64()&CAP_REQUEST_TIMEOUT == 0 {
		t.Skip("kernel does not support CAP_REQUEST_TIMEOUT")
	}
	if srv.conn.out.Flags64()&CAP_REQUEST_TIMEOUT == 0 {
		t.Fatal("CAP_REQUEST_TIMEOUT was offered but not negotiated")
	}

	done := make(chan error, 1)
	go func() {
		var st syscall.Statfs_t
		done <- syscall.Statfs(mnt, &st)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Statfs succeeded, want the kernel to abort the connection")
		}
		t.Logf("Statfs: %v", err)
	case <-time.After(2 * time.Minute):
		t.Fatal("kernel did not abort the connection")
	}
}
