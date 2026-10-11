//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileSinkRespectsReceiverUmask(t *testing.T) {
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	dir := t.TempDir()
	s, err := newFileSink(header{Name: "report.txt", Mode: 0o644}, recvOpts{out: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.abort()
	if _, err := s.commit(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("receiver umask was bypassed: mode %o, want 600", perm)
	}
}
