package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.sock")
	if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Fatal("regular file was accepted as a stale socket")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("regular file was removed: %v", err)
	}
}

func TestRemoveStaleSocketRemovesSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "collab-runner-main-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "runner.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still exists: %v", err)
	}
}
