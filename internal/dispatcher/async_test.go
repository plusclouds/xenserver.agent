package dispatcher_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/dispatcher"
	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"github.com/plusclouds/ubuntu-agent/internal/jobs"
	"github.com/plusclouds/ubuntu-agent/internal/modules/nfsmount"
	"github.com/plusclouds/ubuntu-agent/internal/modules/openvswitch"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

// xe stub whose vm-export writes a file slowly, then exits with the code the
// test asks for via the EXPORT_FAIL marker file next to the output.
const slowExportXe = `#!/bin/bash
cmd="$1"; shift
case "$cmd" in
  vm-list) echo "vm-uuid-1" ;;
  vbd-list|vdi-list|snapshot-list) ;;
  vm-export)
    for a in "$@"; do case "$a" in filename=*) f="${a#filename=}";; esac; done
    printf 'part1' > "$f"; sleep 1; printf 'part2' >> "$f"
    if [[ -e "${f%.part}.fail" ]]; then echo "disk full" >&2; exit 1; fi
    ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

func newAsyncDispatcher(t *testing.T, withJobs bool) (*dispatcher.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", slowExportXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	exec := executor.New(logger, false)
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", []string{"xenserver.vm.export", "xenserver.vm.list", "agent.job.status", "agent.job.list", "agent.job.cancel"}, nil, logger)
	if withJobs {
		m := jobs.New(logger, 2, 10)
		t.Cleanup(m.Shutdown)
		d.SetJobs(m)
	}
	return d, dir
}

func decode(t *testing.T, env protocol.Envelope) (protocol.ResultPayload, map[string]any) {
	t.Helper()
	var p protocol.ResultPayload
	if err := env.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	m, _ := p.Output.(map[string]any)
	return p, m
}

func TestAsyncExportReturnsImmediatelyThenCompletes(t *testing.T) {
	d, dir := newAsyncDispatcher(t, true)
	out := filepath.Join(dir, "vm.xva")

	start := time.Now()
	p, job := decode(t, d.Dispatch(context.Background(),
		buildCommand(t, "xenserver.vm.export", map[string]any{"uuid": "vm-uuid-1", "filename": out, "async": true})))
	if p.Status != protocol.StatusAccepted {
		t.Fatalf("want accepted, got %q (%s)", p.Status, p.Message)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("async call blocked for %v", time.Since(start))
	}
	id, _ := job["job_id"].(string)
	if id == "" || job["status"] != "running" {
		t.Fatalf("job: %v", job)
	}

	var final map[string]any
	for i := 0; i < 100; i++ {
		_, final = decode(t, d.Dispatch(context.Background(), buildCommand(t, "agent.job.status", map[string]any{"job_id": id})))
		if final["status"] != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final["status"] != "completed" {
		t.Fatalf("want completed, got %v", final)
	}
	res, _ := final["result"].(map[string]any)
	if res["success"] != true {
		t.Fatalf("result: %v", res)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the finished export must exist under its final name: %v", err)
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Errorf("no .part file may remain after success: %v", err)
	}
}

func TestAsyncLogicalFailureMarksJobFailedAndRemovesPartial(t *testing.T) {
	d, dir := newAsyncDispatcher(t, true)
	out := filepath.Join(dir, "bad.xva")
	writeStub(t, dir, "bad.xva.fail", "x") // makes the xe stub exit 1 after writing
	_, job := decode(t, d.Dispatch(context.Background(),
		buildCommand(t, "xenserver.vm.export", map[string]any{"uuid": "vm-uuid-1", "filename": out, "async": true})))
	id := job["job_id"].(string)
	var final map[string]any
	for i := 0; i < 100; i++ {
		_, final = decode(t, d.Dispatch(context.Background(), buildCommand(t, "agent.job.status", map[string]any{"job_id": id})))
		if final["status"] != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if final["status"] != "failed" {
		t.Fatalf("want failed, got %v", final)
	}
	if msg, _ := final["error"].(string); msg == "" {
		t.Fatalf("failed job must carry an error: %v", final)
	}
	for _, f := range []string{out, out + ".part"} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("neither the final nor the partial export file may exist after a failure: %s (%v)", f, err)
		}
	}
	if _, err := json.Marshal(final); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncRejections(t *testing.T) {
	d, _ := newAsyncDispatcher(t, true)
	p, _ := decode(t, d.Dispatch(context.Background(), buildCommand(t, "xenserver.vm.list", map[string]any{"async": true})))
	if p.Status != protocol.StatusRejected {
		t.Fatalf("non-async op: want rejected, got %q", p.Status)
	}
	p, _ = decode(t, d.Dispatch(context.Background(), buildCommand(t, "agent.job.status", map[string]any{"job_id": "nope"})))
	if p.Status != protocol.StatusFailed {
		t.Fatalf("unknown job: want failed, got %q (%s)", p.Status, p.Message)
	}

	d2, _ := newAsyncDispatcher(t, false)
	p, _ = decode(t, d2.Dispatch(context.Background(),
		buildCommand(t, "xenserver.vm.export", map[string]any{"uuid": "u", "filename": "/tmp/x", "async": true})))
	if p.Status != protocol.StatusRejected {
		t.Fatalf("jobs disabled: want rejected, got %q", p.Status)
	}
}
