package dispatcher_test

import (
	"context"
	"path/filepath"
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

const inventoryXe = `#!/bin/bash
cmd="$1"; shift
case "$cmd" in
  host-list)
    if [[ "$*" == *--minimal* ]]; then echo "host-1,host-2"; else printf 'uuid ( RO)           : host-1\n          name-label ( RW): n\n  PV-drivers-up-to-date ( RO) [DEPRECATED]: true\n'; fi ;;
  pool-list) printf 'uuid ( RO)           : pool-1\n          name-label ( RW): p\n              master ( RO): host-1\n          ha-enabled ( RO): false\n' ;;
  pif-list|network-list|vlan-list|sr-list)
    if [[ "$*" != *params=all* ]]; then echo "need params=all" >&2; exit 1; fi
    printf 'uuid ( RO)           : %s-1\n          name-label ( RW): n\n  PV-drivers-up-to-date ( RO) [DEPRECATED]: true\n' "${cmd%%-*}" ;;
  pbd-list)
    printf 'uuid ( RO)           : pbd-1\n       device-config (MRO): server: nas; password: s3cret\n' ;;
  vm-list) printf 'uuid ( RO)           : vm-1\n' ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

func TestInventorySnapshot(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", inventoryXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	exec := executor.New(logger, false)
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", []string{"xenserver.inventory.snapshot", "xenserver.pool.info"}, nil, logger)
	ctx := context.Background()

	p, out := decode(t, d.Dispatch(ctx, buildCommand(t, "xenserver.inventory.snapshot", nil)))
	if p.Status != protocol.StatusCompleted {
		t.Fatalf("%+v", p)
	}
	pool := out["pool"].(map[string]any)
	if pool["is_pool_member"] != true || pool["host_count"] != float64(2) {
		t.Fatalf("pool: %v", pool)
	}
	for _, k := range []string{"hosts", "pifs", "networks", "vlans", "srs", "pbds"} {
		if l, _ := out[k].([]any); len(l) != 1 {
			t.Fatalf("%s: %v", k, out[k])
		}
	}
	if _, has := out["vms"]; has {
		t.Fatal("vms present without include_vms")
	}
	pbd := out["pbds"].([]any)[0].(map[string]any)
	if pbd["maps"].(map[string]any)["device-config"].(map[string]any)["password"] != "***" {
		t.Fatalf("pbd secret not masked: %v", pbd)
	}
	host := out["hosts"].([]any)[0].(map[string]any)
	if _, ok := host["params"].(map[string]any)["PV-drivers-up-to-date"]; !ok {
		t.Fatalf("annotation not stripped: %v", host["params"])
	}

	_, out = decode(t, d.Dispatch(ctx, buildCommand(t, "xenserver.inventory.snapshot", map[string]any{"include_vms": true})))
	if l, _ := out["vms"].([]any); len(l) != 1 {
		t.Fatalf("vms: %v", out["vms"])
	}
}
