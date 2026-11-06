// Copyright 2026 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/internal/testutil"
)

// statfsFS answers STATFS with a fixed block count, so a test can
// tell which server answered.
type statfsFS struct {
	RawFileSystem
	blocks uint64
}

func (fs *statfsFS) StatFs(cancel <-chan struct{}, in *InHeader, out *StatfsOut) Status {
	out.Blocks = fs.blocks
	out.Bsize = 4096
	return OK
}

func statfsBlocks(t *testing.T, mnt string) uint64 {
	t.Helper()
	var st syscall.Statfs_t
	if err := syscall.Statfs(mnt, &st); err != nil {
		t.Fatalf("Statfs: %v", err)
	}
	return st.Blocks
}

const adoptHelperEnv = "GO_FUSE_TEST_ADOPT_HELPER"

// TestAdoptHelper is not a real test. TestAdopt runs it in a child
// process, which serves the connection on fd 3 and writes its
// AdoptionState to fd 4.
func TestAdoptHelper(t *testing.T) {
	if os.Getenv(adoptHelperEnv) == "" {
		t.Skip("helper process for TestAdopt")
	}
	srv, err := NewServer(&statfsFS{RawFileSystem: NewDefaultRawFileSystem(), blocks: 1}, "/dev/fd/3", &MountOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	state := os.NewFile(4, "state")
	if err := json.NewEncoder(state).Encode(srv.AdoptionState()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	state.Close()
	srv.Serve()
	os.Exit(0)
}

// TestAdopt hands a live connection from a server in a child process
// to a second server, without unmounting.
func TestAdopt(t *testing.T) {
	mnt := t.TempDir()
	fd, err := callFusermount(mnt, &MountOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unmount(mnt, &MountOptions{}) })

	dup, err := syscall.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	conn := os.NewFile(uintptr(dup), "/dev/fuse")
	stateR, stateW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stateR.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptHelper$")
	cmd.Env = append(os.Environ(), adoptHelperEnv+"=1")
	cmd.ExtraFiles = []*os.File{conn, stateW}
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	conn.Close()
	stateW.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	var state Adoption
	if err := json.NewDecoder(stateR).Decode(&state); err != nil {
		t.Fatalf("reading AdoptionState: %v", err)
	}
	if got := statfsBlocks(t, mnt); got != 1 {
		t.Fatalf("first server: got %d blocks, want 1", got)
	}

	// Stop the first server the way a crash would. The mount
	// survives, because this process still holds the connection.
	cmd.Process.Kill()
	cmd.Wait()

	srv, err := NewServer(&statfsFS{RawFileSystem: NewDefaultRawFileSystem(), blocks: 2},
		fmt.Sprintf("/dev/fd/%d", fd),
		&MountOptions{Adopt: &state, Debug: testutil.VerboseTest()})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()

	if got := statfsBlocks(t, mnt); got != 2 {
		t.Errorf("second server: got %d blocks, want 2", got)
	}
	if got := srv.AdoptionState(); got != state {
		t.Errorf("AdoptionState: got %+v, want %+v", got, state)
	}

	// srv only knows the /dev/fd/N mountpoint; see TestMountDevFd.
	srv.mountPoint = mnt
	if err := srv.Unmount(); err != nil {
		t.Error(err)
	}
}

func TestAdoptNeedsDevFd(t *testing.T) {
	_, err := NewServer(NewDefaultRawFileSystem(), t.TempDir(), &MountOptions{Adopt: &Adoption{}})
	if err == nil {
		t.Fatal("NewServer succeeded with Adopt and a directory mountpoint")
	}
}
