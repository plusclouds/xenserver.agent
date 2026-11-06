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

const storageXe = `#!/bin/bash
{ printf '%s\n' "$@"; echo '--'; } >> "$XE_LOG"
cmd="$1"; shift
case "$cmd" in
  vm-list) echo "vm-uuid-1" ;;
  vdi-list) printf 'name-label ( RW): data1\n' ;;
  vdi-create|vbd-create) echo "new-uuid-1" ;;
  vdi-param-get) echo "$VDI_VBDS" ;;
  vdi-destroy|vbd-plug|vbd-unplug|vbd-destroy|vm-cd-add|vm-cd-insert|vm-cd-eject|vm-cd-remove) exit 0 ;;
  vbd-list)
    if [[ "$*" == *"--minimal"* ]]; then echo "vbd-uuid-1"; else
      printf 'uuid ( RO): vbd-c1\nuserdevice ( RW): 3\nempty ( RO): false\nvdi-uuid ( RO): iso-1\nvdi-name-label ( RO): debian.iso\ncurrently-attached ( RO): true\nmode ( RW): RO\n\nuuid ( RO): vbd-c2\nuserdevice ( RW): 4\nempty ( RO): true\nvdi-uuid ( RO): <not in database>\nvdi-name-label ( RO): <not in database>\ncurrently-attached ( RO): false\nmode ( RW): RO\n'
    fi ;;
  vbd-param-list) printf 'uuid ( RO): vbd-uuid-1\nbootable ( RW): true\nother-config (MRW): owner: \n' ;;
  vdi-param-list) printf 'uuid ( RO): vdi-1\nvirtual-size ( RO): 1073741824\nsm-config (MRO): type: vhd\n' ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

func newStorageDispatcher(t *testing.T) (*dispatcher.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "xe.log")
	t.Setenv("XE_LOG", logPath)
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", storageXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	exec := executor.New(logger, false)
	ops := []string{"xenserver.vdi.create", "xenserver.vdi.destroy", "xenserver.vdi.param_list", "xenserver.vbd.create",
		"xenserver.vbd.plug", "xenserver.vbd.unplug", "xenserver.vbd.destroy", "xenserver.vbd.param_list",
		"xenserver.vm.cd_list", "xenserver.vm.cd_add", "xenserver.vm.cd_insert", "xenserver.vm.cd_eject", "xenserver.vm.cd_remove"}
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", ops, nil, logger)
	return d, logPath
}

func call(t *testing.T, d *dispatcher.Dispatcher, op string, params map[string]any) (protocol.ResultPayload, any) {
	t.Helper()
	var p protocol.ResultPayload
	res := d.Dispatch(context.Background(), buildCommand(t, op, params))
	if err := res.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	return p, p.Output
}

func lastCall(t *testing.T, log, cmd string) string {
	t.Helper()
	var found string
	for _, c := range xeCalls(t, log) {
		if c[0] == cmd {
			found = strings.Join(c, " ")
		}
	}
	return found
}

func TestVDIAndVBDLifecycle(t *testing.T) {
	d, log := newStorageDispatcher(t)

	p, o := call(t, d, "xenserver.vdi.create", map[string]any{"sr_uuid": "sr-1", "new_name": "data1", "size_bytes": 1073741824, "sharable": true})
	if p.Status != protocol.StatusCompleted || o.(map[string]any)["data"].(map[string]any)["uuid"] != "new-uuid-1" {
		t.Fatalf("vdi.create: %+v", o)
	}
	if got := lastCall(t, log, "vdi-create"); got != "vdi-create sr-uuid=sr-1 name-label=data1 type=user virtual-size=1073741824 sharable=true" {
		t.Errorf("vdi-create args: %q", got)
	}
	if p, _ := call(t, d, "xenserver.vdi.create", map[string]any{"sr_uuid": "sr-1", "new_name": "x"}); p.Status != protocol.StatusFailed {
		t.Error("vdi.create without size must fail")
	}

	// destroy needs confirm and never reaches xe without it
	if p, _ := call(t, d, "xenserver.vdi.destroy", map[string]any{"vdi_uuid": "vdi-1"}); p.Status != protocol.StatusFailed || !strings.Contains(p.Message, "confirm=true") {
		t.Errorf("destroy without confirm: %+v", p)
	}
	if lastCall(t, log, "vdi-destroy") != "" {
		t.Fatal("vdi-destroy must not run without confirm")
	}
	if _, o := call(t, d, "xenserver.vdi.destroy", map[string]any{"vdi_uuid": "vdi-1", "confirm": true}); o.(map[string]any)["success"] != true {
		t.Errorf("confirmed destroy: %v", o)
	}
	// an attached disk is refused unless allow_attached
	t.Setenv("VDI_VBDS", "vbd-a; vbd-b")
	before := lastCall(t, log, "vdi-destroy")
	if p, _ := call(t, d, "xenserver.vdi.destroy", map[string]any{"vdi_uuid": "vdi-2", "confirm": true}); p.Status != protocol.StatusFailed ||
		!strings.Contains(p.Message, "still attached") || !strings.Contains(p.Message, "vbd-a") {
		t.Errorf("attached disk must be refused: %+v", p)
	}
	if lastCall(t, log, "vdi-destroy") != before {
		t.Fatal("vdi-destroy reached xe for an attached disk")
	}
	if _, o := call(t, d, "xenserver.vdi.destroy", map[string]any{"vdi_uuid": "vdi-2", "confirm": true, "allow_attached": true}); o.(map[string]any)["success"] != true {
		t.Errorf("allow_attached: %v", o)
	}
	t.Setenv("VDI_VBDS", "")

	if _, o := call(t, d, "xenserver.vbd.create", map[string]any{"uuid": "vm-uuid-1", "vdi_uuid": "vdi-1", "device": "1", "bootable": true}); o.(map[string]any)["data"].(map[string]any)["uuid"] != "new-uuid-1" {
		t.Fatalf("vbd.create: %v", o)
	}
	if got := lastCall(t, log, "vbd-create"); got != "vbd-create vm-uuid=vm-uuid-1 device=1 mode=RW type=Disk bootable=true vdi-uuid=vdi-1" {
		t.Errorf("vbd-create args: %q", got)
	}
	if p, _ := call(t, d, "xenserver.vbd.create", map[string]any{"uuid": "vm-uuid-1"}); p.Status != protocol.StatusFailed {
		t.Error("Disk vbd.create without vdi_uuid must fail")
	}
	if p, _ := call(t, d, "xenserver.vbd.create", map[string]any{"uuid": "vm-uuid-1", "vdi_uuid": "v", "mode": "XX"}); p.Status != protocol.StatusFailed {
		t.Error("bad mode must fail")
	}

	// plug/unplug/destroy by vbd_uuid and by VM+position
	call(t, d, "xenserver.vbd.plug", map[string]any{"vbd_uuid": "vbd-9"})
	if got := lastCall(t, log, "vbd-plug"); got != "vbd-plug uuid=vbd-9" {
		t.Errorf("plug: %q", got)
	}
	call(t, d, "xenserver.vbd.unplug", map[string]any{"uuid": "vm-uuid-1", "device": "2"})
	if got := lastCall(t, log, "vbd-unplug"); got != "vbd-unplug uuid=vbd-uuid-1" {
		t.Errorf("unplug by position must resolve the VBD: %q", got)
	}
	call(t, d, "xenserver.vbd.destroy", map[string]any{"vbd_uuid": "vbd-9"})
	if got := lastCall(t, log, "vbd-destroy"); got != "vbd-destroy uuid=vbd-9" {
		t.Errorf("destroy: %q", got)
	}
	if p, _ := call(t, d, "xenserver.vbd.plug", map[string]any{"uuid": "vm-uuid-1"}); p.Status != protocol.StatusFailed {
		t.Error("plug without vbd_uuid or device must fail")
	}

	_, o = call(t, d, "xenserver.vdi.param_list", map[string]any{"vdi_uuid": "vdi-1"})
	if maps := o.(map[string]any)["maps"].(map[string]any); maps["sm-config"].(map[string]any)["type"] != "vhd" {
		t.Errorf("vdi.param_list maps: %v", o)
	}
	_, o = call(t, d, "xenserver.vbd.param_list", map[string]any{"vbd_uuid": "vbd-uuid-1"})
	if o.(map[string]any)["params"].(map[string]any)["bootable"] != "true" {
		t.Errorf("vbd.param_list: %v", o)
	}
}

func TestCDOperations(t *testing.T) {
	d, log := newStorageDispatcher(t)
	_, o := call(t, d, "xenserver.vm.cd_list", map[string]any{"uuid": "vm-uuid-1"})
	cds := o.([]any)
	if len(cds) != 2 {
		t.Fatalf("cd_list: %v", o)
	}
	full, empty := cds[0].(map[string]any), cds[1].(map[string]any)
	if full["empty"] != false || full["iso_name"] != "debian.iso" || full["iso_uuid"] != "iso-1" || full["position"] != "3" {
		t.Errorf("full drive: %v", full)
	}
	if empty["empty"] != true || empty["iso_name"] != nil || empty["iso_uuid"] != nil {
		t.Errorf("empty drive must carry no ISO: %v", empty)
	}
	call(t, d, "xenserver.vm.cd_add", map[string]any{"uuid": "vm-uuid-1", "cd_name": "my iso.iso", "device": "3"})
	if got := lastCall(t, log, "vm-cd-add"); got != "vm-cd-add uuid=vm-uuid-1 cd-name=my iso.iso device=3" {
		t.Errorf("cd_add: %q", got)
	}
	call(t, d, "xenserver.vm.cd_add", map[string]any{"uuid": "vm-uuid-1", "cd_name": "b.iso"})
	if got := lastCall(t, log, "vm-cd-add"); got != "vm-cd-add uuid=vm-uuid-1 cd-name=b.iso device=autodetect" {
		t.Errorf("cd_add without device must default to autodetect (xe requires one): %q", got)
	}
	call(t, d, "xenserver.vm.cd_insert", map[string]any{"uuid": "vm-uuid-1", "cd_name": "a.iso"})
	call(t, d, "xenserver.vm.cd_eject", map[string]any{"uuid": "vm-uuid-1"})
	call(t, d, "xenserver.vm.cd_remove", map[string]any{"uuid": "vm-uuid-1", "cd_name": "a.iso"})
	for cmd, want := range map[string]string{"vm-cd-insert": "vm-cd-insert uuid=vm-uuid-1 cd-name=a.iso",
		"vm-cd-eject": "vm-cd-eject uuid=vm-uuid-1", "vm-cd-remove": "vm-cd-remove uuid=vm-uuid-1 cd-name=a.iso"} {
		if got := lastCall(t, log, cmd); got != want {
			t.Errorf("%s: %q want %q", cmd, got, want)
		}
	}
	if p, _ := call(t, d, "xenserver.vm.cd_add", map[string]any{"uuid": "vm-uuid-1"}); p.Status != protocol.StatusFailed {
		t.Error("cd_add without cd_name must fail")
	}
}

func TestSRCreateISOAndIdempotent(t *testing.T) {
	d, log := newSRCreateDispatcher(t)
	cfg := map[string]any{"type": "nfs_iso", "location": "10.0.0.5:/isos", "nfsversion": "4"}
	_, o := call(t, d, "xenserver.sr.create", map[string]any{"name_label": "ISO on repo1", "type": "iso", "content_type": "iso",
		"shared": true, "device_config": cfg, "if_not_exists": true})
	m := o.(map[string]any)
	if m["success"] != true || m["data"].(map[string]any)["existing"] != false || m["data"].(map[string]any)["uuid"] != "new-sr-uuid" {
		t.Fatalf("create: %v", m)
	}
	want := "sr-create name-label=ISO on repo1 type=iso shared=true host-uuid=host-1 content-type=iso device-config:location=10.0.0.5:/isos device-config:nfsversion=4 device-config:type=nfs_iso"
	if got := lastCall(t, log, "sr-create"); got != want {
		t.Errorf("sr-create args (device-config keys sorted):\n got %q\nwant %q", got, want)
	}
	// second time: the SR now "exists"
	t.Setenv("SR_EXISTS", "sr-existing-1")
	before := len(xeCalls(t, log))
	_, o = call(t, d, "xenserver.sr.create", map[string]any{"name_label": "ISO on repo1", "type": "iso", "if_not_exists": true, "device_config": cfg})
	m = o.(map[string]any)
	if m["success"] != true || m["data"].(map[string]any)["existing"] != true || m["data"].(map[string]any)["uuid"] != "sr-existing-1" {
		t.Fatalf("idempotent skip: %v", m)
	}
	for _, c := range xeCalls(t, log)[before:] {
		if c[0] == "sr-create" {
			t.Fatal("sr-create must not run when the SR exists")
		}
	}
	if p, _ := call(t, d, "xenserver.sr.create", map[string]any{"name": "x", "type": "iso", "device_config": map[string]any{"bad key=": "v"}}); p.Status != protocol.StatusFailed {
		t.Error("device_config key with '=' must be rejected")
	}
}

const srCreateXe = `#!/bin/bash
{ printf '%s\n' "$@"; echo '--'; } >> "$XE_LOG"
cmd="$1"; shift
case "$cmd" in
  sr-list) echo "$SR_EXISTS" ;;
  sr-create) echo "new-sr-uuid" ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

func newSRCreateDispatcher(t *testing.T) (*dispatcher.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "xe.log")
	t.Setenv("XE_LOG", logPath)
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", srCreateXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	inv := filepath.Join(dir, "inventory")
	if err := os.WriteFile(inv, []byte("INSTALLATION_UUID='host-1'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := executor.New(logger, false)
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, inv, filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", []string{"xenserver.sr.create"}, nil, logger)
	return d, logPath
}
