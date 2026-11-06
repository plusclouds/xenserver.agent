package dispatcher_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/dispatcher"
	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"github.com/plusclouds/ubuntu-agent/internal/modules/nfsmount"
	"github.com/plusclouds/ubuntu-agent/internal/modules/openvswitch"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

func TestDryRunValidatesButNeverExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "xe-was-called")
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", "#!/bin/bash\ntouch "+marker+"\nexit 1\n")
	ok := writeStub(t, dir, "true", trueStubScript)
	exec := executor.New(logger, false)
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", []string{"xenserver.vdi.destroy", "xenserver.vdi.create", "xenserver.vm.list"}, nil, logger)
	d.SetDryRun(true)
	ctx := context.Background()

	p, out := decode(t, d.Dispatch(ctx, buildCommand(t, "xenserver.vdi.destroy", map[string]any{"vdi_uuid": "v", "confirm": true})))
	if p.Status != protocol.StatusCompleted || out["dry_run"] != true || out["executed"] != false {
		t.Fatalf("valid destroy: %+v %+v", p, out)
	}
	p, _ = decode(t, d.Dispatch(ctx, buildCommand(t, "xenserver.vdi.destroy", map[string]any{"vdi_uuid": "v"})))
	if p.Status != protocol.StatusRejected || !strings.Contains(p.Message, "confirm") {
		t.Fatalf("missing confirm: %+v", p)
	}
	p, _ = decode(t, d.Dispatch(ctx, buildCommand(t, "xenserver.vdi.create", map[string]any{"new_name": "x"})))
	if p.Status != protocol.StatusRejected || !strings.Contains(p.Message, "sr_uuid") {
		t.Fatalf("missing params: %+v", p)
	}
	p, _ = decode(t, d.Dispatch(ctx, buildCommand(t, "xenserver.vm.start", map[string]any{"uuid": "u"})))
	if p.Status != protocol.StatusRejected {
		t.Fatalf("not allowed op must still be rejected: %+v", p)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("xe was executed during dry run")
	}
}
