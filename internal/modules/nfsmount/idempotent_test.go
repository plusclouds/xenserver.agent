package nfsmount

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/executor"
)

func TestMountIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, "repo")
	os.MkdirAll(mp, 0o755)
	proc := filepath.Join(dir, "mounts")
	os.WriteFile(proc, []byte("nas.example.com:/images "+mp+" nfs4 rw,vers=4.2 0 0\n"), 0o644)
	failing := filepath.Join(dir, "mount")
	os.WriteFile(failing, []byte("#!/bin/bash\necho should not run >&2\nexit 1\n"), 0o755)

	m := New(executor.New(zap.NewNop(), false), failing, failing, zap.NewNop())
	m.procMounts = proc

	r, err := m.Mount(context.Background(), "nas.example.com", "/images", mp, "")
	if err != nil || !r.Success || !strings.Contains(r.Message, "already mounted") {
		t.Fatalf("same export: %+v %v", r, err)
	}
	r, err = m.Mount(context.Background(), "nas.example.com", "/images/", mp, "")
	if err != nil || !r.Success {
		t.Fatalf("trailing slash: %+v %v", r, err)
	}
	r, err = m.Mount(context.Background(), "other.example.com", "/images", mp, "")
	if err != nil || r.Success || !strings.Contains(r.Message, "different mount") {
		t.Fatalf("different export: %+v %v", r, err)
	}
}

func TestUnmountBusyMessage(t *testing.T) {
	dir := t.TempDir()
	busy := filepath.Join(dir, "umount")
	os.WriteFile(busy, []byte("#!/bin/bash\necho 'umount: /mnt/x: target is busy.' >&2\nexit 32\n"), 0o755)
	m := New(executor.New(zap.NewNop(), false), busy, busy, zap.NewNop())
	r, _ := m.Unmount(context.Background(), "/mnt/x")
	if r.Success || !strings.Contains(r.Message, "busy") || !strings.Contains(r.Message, "retry") {
		t.Fatalf("%+v", r)
	}
}
