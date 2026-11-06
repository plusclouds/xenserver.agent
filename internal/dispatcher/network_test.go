package dispatcher_test

import (
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

// FAIL_CREATE_MAC=<mac>: a vif-create with that mac fails (to test rollback).
const networkXe = `#!/bin/bash
{ printf '%s\n' "$@"; echo '--'; } >> "$XE_LOG"
cmd="$1"; shift
case "$cmd" in
  vm-list) if [[ "$*" == *"--minimal"* ]]; then echo "vm-uuid-1"; else printf 'name-label ( RW): web\npower-state ( RO): halted\n\nname-label ( RW): web2\npower-state ( RO): running\n'; fi ;;
  template-list) printf 'uuid ( RO): tmpl-1\nname-label ( RW): Debian\n\nuuid ( RO): tmpl-2\nname-label ( RW): Ubuntu\n' ;;
  network-list) if [[ "$*" == *"--minimal"* ]]; then echo "net-1"; else printf 'uuid ( RO): net-1\nname-label ( RW): n1\nbridge ( RO): xapi1\n'; fi ;;
  vif-list) printf 'vm-uuid ( RO): vm-uuid-1\ndevice ( RO): 0\nMAC ( RO): aa:bb:cc:00:00:01\nMTU ( RO): 1500\nnetwork-uuid ( RO): net-1\nlocking-mode ( RW): locked\n' ;;
  vif-create)
    for a in "$@"; do [[ -n "$FAIL_CREATE_MAC" && "$a" == "mac=$FAIL_CREATE_MAC" ]] && { echo "MAC_INVALID" >&2; exit 1; }; done
    echo "new-vif-1" ;;
  vif-param-set|vif-destroy|network-destroy|vlan-destroy|pif-unplug|snapshot-param-set|vm-param-set|sr-scan) exit 0 ;;
  network-create) echo "new-net-1" ;;
  vlan-create) echo "new-pif-1" ;;
  vlan-list) if [[ "$*" == *"--minimal"* ]]; then echo "vlan-real-1"; else printf 'uuid ( RO): vlan-1\ntag ( RO): 100\ntagged-PIF ( RO): pif-t\nuntagged-PIF ( RO): pif-u\n'; fi ;;
  pif-list) printf 'uuid ( RO): pif-1\ndevice ( RO): eth0\nVLAN ( RO): -1\nnetwork-uuid ( RO): net-1\nhost-uuid ( RO): host-1\nMAC ( RO): aa:00\ncurrently-attached ( RO): true\nIP ( RO): 10.0.0.2\nnetmask ( RO): 255.255.255.0\ngateway ( RO): 10.0.0.1\nmanagement ( RO): true\nphysical ( RO): true\nMTU ( RO): 1500\nbond-slave-of ( RO): <not in database>\nbond-master-of ( SRO): \n' ;;
  pif-param-get) if [[ "$*" == *"param-name=management"* ]]; then echo "${PIF_MGMT:-true}"; else echo "val"; fi ;;
  pbd-list) printf 'uuid ( RO): pbd-1\nsr-uuid ( RO): sr-1\nhost-uuid ( RO): host-1\ncurrently-attached ( RO): true\ndevice-config (MRO): server: 10.0.0.9; password: hunter2; serverpath: /x\n' ;;
  task-list) printf 'uuid ( RO): t1\nname-label ( RW): Async.VM.start\nstatus ( RO): pending\nprogress ( RO): 0.420000\ncreated ( RO): 20261106T10:00:00Z\nfinished ( RO): \nresult ( RO): \nerror-info ( RO): \n' ;;
  snapshot-list) echo "snap-1" ;;
  sr-list) printf 'uuid ( RO): sr-1\nname-label ( RW): nfs1\ntype ( RO): nfs\nphysical-size ( RO): 100\nphysical-utilisation ( RO): 10\n' ;;
  pbd-param-list) printf 'uuid ( RO): pbd-1\ndevice-config (MRO): server: 10.0.0.9; cifspassword: hunter2\n' ;;
  vlan-param-list|network-param-list|pif-param-list|sr-param-list|host-param-list) printf 'uuid ( RO): x\nname-label ( RW): y\n' ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

var networkOps = []string{"xenserver.vif.create", "xenserver.vif.param_list", "xenserver.vif.param_set", "xenserver.network.create",
	"xenserver.network.destroy", "xenserver.network.param_list", "xenserver.network.list", "xenserver.vlan.list", "xenserver.vlan.create",
	"xenserver.vlan.destroy", "xenserver.pif.list", "xenserver.pif.param_get", "xenserver.pif.unplug", "xenserver.pbd.list",
	"xenserver.task.list", "xenserver.pbd.param_list", "xenserver.sr.scan", "xenserver.sr.list", "xenserver.sr.param_get", "xenserver.vm.list",
	"xenserver.vm.snapshot_promote", "xenserver.host.param_list"}

func newNetworkDispatcher(t *testing.T) (*dispatcher.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "xe.log")
	t.Setenv("XE_LOG", logPath)
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", networkXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	exec := executor.New(logger, false)
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", networkOps, nil, logger)
	return d, logPath
}

func TestVIFCreateAndParamSet(t *testing.T) {
	d, log := newNetworkDispatcher(t)
	_, o := call(t, d, "xenserver.vif.create", map[string]any{"uuid": "vm-uuid-1", "device": "2", "network_name": "n1", "mac": "AA:BB:CC:DD:EE:FF", "mtu": 1400})
	if o.(map[string]any)["data"].(map[string]any)["uuid"] != "new-vif-1" {
		t.Fatalf("vif.create: %v", o)
	}
	if got := lastCall(t, log, "vif-create"); got != "vif-create vm-uuid=vm-uuid-1 network-uuid=net-1 device=2 mac=aa:bb:cc:dd:ee:ff mtu=1400" {
		t.Errorf("vif-create args (mac must be lowercased): %q", got)
	}
	for _, bad := range []map[string]any{{"uuid": "vm-uuid-1", "network_uuid": "n"}, {"uuid": "vm-uuid-1", "device": "0", "network_uuid": "n", "mac": "zz"},
		{"uuid": "vm-uuid-1", "device": "0", "network_uuid": "n", "mtu": 10}} {
		if p, _ := call(t, d, "xenserver.vif.create", bad); p.Status != protocol.StatusFailed {
			t.Errorf("%v must fail", bad)
		}
	}

	// rate limit = plain vif-param-set calls
	call(t, d, "xenserver.vif.param_set", map[string]any{"vif_uuid": "vif-9", "set": map[string]any{"qos_algorithm_type": "ratelimit", "qos_algorithm_params:kbps": "5000"}})
	var sets []string
	for _, c := range xeCalls(t, log) {
		if c[0] == "vif-param-set" {
			sets = append(sets, strings.Join(c, " "))
		}
	}
	if len(sets) != 2 || sets[0] != "vif-param-set uuid=vif-9 qos_algorithm_params:kbps=5000" || sets[1] != "vif-param-set uuid=vif-9 qos_algorithm_type=ratelimit" {
		t.Errorf("rate limit calls: %v", sets)
	}
	for _, set := range []map[string]any{{"MAC": "nope"}, {"qos_algorithm_type": "weird"}, {"qos_algorithm_params:burst": "1"}, {"VM": "x"}, {"qos_algorithm_params:kbps": "0"}} {
		if p, _ := call(t, d, "xenserver.vif.param_set", map[string]any{"vif_uuid": "vif-9", "set": set}); p.Status != protocol.StatusFailed {
			t.Errorf("%v must be rejected", set)
		}
	}
}

func TestVIFMACChangeRecreatesAndRollsBack(t *testing.T) {
	d, log := newNetworkDispatcher(t)
	_, o := call(t, d, "xenserver.vif.param_set", map[string]any{"vif_uuid": "vif-9", "set": map[string]any{"MAC": "AA:BB:CC:00:00:99"}})
	m := o.(map[string]any)
	if m["success"] != true || m["data"].(map[string]any)["uuid"] != "new-vif-1" {
		t.Fatalf("MAC change: %v", m)
	}
	var seq []string
	for _, c := range xeCalls(t, log) {
		switch c[0] {
		case "vif-destroy", "vif-create", "vif-param-set":
			seq = append(seq, strings.Join(c, " "))
		}
	}
	want := []string{"vif-destroy uuid=vif-9",
		"vif-create vm-uuid=vm-uuid-1 network-uuid=net-1 device=0 mac=aa:bb:cc:00:00:99 mtu=1500",
		"vif-param-set uuid=new-vif-1 locking-mode=locked"}
	if strings.Join(seq, "|") != strings.Join(want, "|") {
		t.Errorf("sequence:\n got %v\nwant %v", seq, want)
	}

	// failing create restores the original MAC
	d2, log2 := newNetworkDispatcher(t)
	t.Setenv("FAIL_CREATE_MAC", "aa:bb:cc:00:00:99")
	_, o = call(t, d2, "xenserver.vif.param_set", map[string]any{"vif_uuid": "vif-9", "set": map[string]any{"MAC": "aa:bb:cc:00:00:99"}})
	m = o.(map[string]any)
	if m["success"] != false || !strings.Contains(m["message"].(string), "original card restored") {
		t.Errorf("rollback message: %v", m)
	}
	creates := 0
	for _, c := range xeCalls(t, log2) {
		if c[0] == "vif-create" {
			creates++
		}
	}
	if creates != 2 {
		t.Errorf("want failed create + restore create, got %d vif-create calls", creates)
	}
}

func TestNetworkVLANPIFSafety(t *testing.T) {
	d, log := newNetworkDispatcher(t)
	_, o := call(t, d, "xenserver.network.create", map[string]any{"new_name": "n2", "mtu": 9000})
	if o.(map[string]any)["data"].(map[string]any)["uuid"] != "new-net-1" {
		t.Fatalf("network.create: %v", o)
	}
	if got := lastCall(t, log, "network-create"); got != "network-create name-label=n2 MTU=9000" {
		t.Errorf("args: %q", got)
	}
	for _, op := range []string{"xenserver.network.destroy", "xenserver.vlan.destroy", "xenserver.pif.unplug"} {
		if p, _ := call(t, d, op, map[string]any{"uuid": "x-1"}); p.Status != protocol.StatusFailed || !strings.Contains(p.Message, "confirm=true") {
			t.Errorf("%s without confirm must be refused: %+v", op, p)
		}
	}
	for _, c := range []string{"network-destroy", "vlan-destroy", "pif-unplug"} {
		if lastCall(t, log, c) != "" {
			t.Errorf("%s must not reach xe without confirm", c)
		}
	}
	call(t, d, "xenserver.network.destroy", map[string]any{"uuid": "net-9", "confirm": true})
	call(t, d, "xenserver.vlan.destroy", map[string]any{"uuid": "vlan-9", "confirm": true})
	if lastCall(t, log, "network-destroy") != "network-destroy uuid=net-9" || lastCall(t, log, "vlan-destroy") != "vlan-destroy uuid=vlan-9" {
		t.Error("confirmed destroy calls missing")
	}

	// the management interface can never be unplugged
	if p, _ := call(t, d, "xenserver.pif.unplug", map[string]any{"uuid": "pif-1", "confirm": true}); p.Status != protocol.StatusFailed || !strings.Contains(p.Message, "management") {
		t.Errorf("management PIF must be refused: %+v", p)
	}
	if lastCall(t, log, "pif-unplug") != "" {
		t.Fatal("pif-unplug reached xe for the management interface")
	}
	t.Setenv("PIF_MGMT", "false")
	if _, o := call(t, d, "xenserver.pif.unplug", map[string]any{"uuid": "pif-2", "confirm": true}); o.(map[string]any)["success"] != true {
		t.Errorf("non-management unplug: %v", o)
	}

	for _, bad := range []map[string]any{{"pif_uuid": "p", "network_uuid": "n", "tag": 0}, {"pif_uuid": "p", "network_uuid": "n", "tag": 4095}, {"network_uuid": "n", "tag": 5}} {
		if p, _ := call(t, d, "xenserver.vlan.create", bad); p.Status != protocol.StatusFailed {
			t.Errorf("%v must fail", bad)
		}
	}
	_, vo := call(t, d, "xenserver.vlan.create", map[string]any{"pif_uuid": "pif-1", "network_uuid": "net-1", "tag": 100})
	if got := lastCall(t, log, "vlan-create"); got != "vlan-create pif-uuid=pif-1 network-uuid=net-1 vlan=100" {
		t.Errorf("vlan-create: %q", got)
	}
	data := vo.(map[string]any)["data"].(map[string]any)
	if data["uuid"] != "vlan-real-1" || data["pif_uuid"] != "new-pif-1" {
		t.Errorf("vlan.create must return the VLAN's own uuid (xe prints the PIF's): %v", data)
	}
	if got := lastCall(t, log, "vlan-list"); got != "vlan-list untagged-PIF=new-pif-1 params=uuid --minimal" {
		t.Errorf("vlan lookup: %q", got)
	}
}

func TestListsAndFilters(t *testing.T) {
	d, log := newNetworkDispatcher(t)
	_, o := call(t, d, "xenserver.pif.list", map[string]any{"vlan": -1, "network_uuid": "net-1"})
	pif := o.([]any)[0].(map[string]any)
	if pif["vlan"] != float64(-1) || pif["management"] != true || pif["device"] != "eth0" || pif["bond_slave_of"] != nil {
		t.Errorf("pif: %v", pif)
	}
	if got := lastCall(t, log, "pif-list"); !strings.HasPrefix(got, "pif-list VLAN=-1 network-uuid=net-1 params=") {
		t.Errorf("pif-list args: %q", got)
	}
	_, o = call(t, d, "xenserver.pbd.list", map[string]any{"sr_uuid": "sr-1"})
	cfg := o.([]any)[0].(map[string]any)["device_config"].(map[string]any)
	if cfg["password"] != "***" || cfg["server"] != "10.0.0.9" || cfg["serverpath"] != "/x" {
		t.Errorf("device_config must mask secrets: %v", cfg)
	}
	_, o = call(t, d, "xenserver.pbd.param_list", map[string]any{"uuid": "pbd-1"})
	pcfg := o.(map[string]any)["maps"].(map[string]any)["device-config"].(map[string]any)
	if pcfg["cifspassword"] != "***" || pcfg["server"] != "10.0.0.9" {
		t.Errorf("pbd.param_list must mask secrets like pbd.list does: %v", pcfg)
	}
	_, o = call(t, d, "xenserver.task.list", nil)
	if tk := o.([]any)[0].(map[string]any); tk["progress"] != 0.42 || tk["status"] != "pending" {
		t.Errorf("task: %v", tk)
	}
	_, o = call(t, d, "xenserver.vlan.list", map[string]any{"tag": 100})
	if v := o.([]any)[0].(map[string]any); v["tag"] != float64(100) || v["untagged_pif"] != "pif-u" {
		t.Errorf("vlan: %v", v)
	}
	if got := lastCall(t, log, "vlan-list"); !strings.HasPrefix(got, "vlan-list tag=100 ") {
		t.Errorf("vlan-list args: %q", got)
	}

	call(t, d, "xenserver.network.list", map[string]any{"bridge": "xapi1", "pif_uuid": "pif-1"})
	if got := lastCall(t, log, "network-list"); !strings.HasPrefix(got, "network-list bridge=xapi1 PIF-uuids=pif-1 params=") {
		t.Errorf("network-list: %q", got)
	}
	call(t, d, "xenserver.sr.list", map[string]any{"type": "nfs", "content_type": "iso"})
	if got := lastCall(t, log, "sr-list"); !strings.HasPrefix(got, "sr-list type=nfs content-type=iso params=") {
		t.Errorf("sr-list: %q", got)
	}
	_, o = call(t, d, "xenserver.vm.list", map[string]any{"is_a_template": true, "fields": []string{"uuid", "name-label"}})
	if items := o.([]any); len(items) != 2 || items[0].(map[string]any)["name-label"] != "Debian" {
		t.Errorf("vm.list fields: %v", o)
	}
	if got := lastCall(t, log, "template-list"); got != "template-list params=uuid,name-label" {
		t.Errorf("is_a_template=true must use template-list (vm-list hides templates): %q", got)
	}
	call(t, d, "xenserver.vm.list", map[string]any{"is_a_template": false, "is_control_domain": false, "name_label": "web"})
	if got := lastCall(t, log, "vm-list"); !strings.HasPrefix(got, "vm-list name-label=web is-control-domain=false is-a-template=false params=") {
		t.Errorf("vm-list filters: %q", got)
	}
	if p, _ := call(t, d, "xenserver.vm.list", map[string]any{"fields": []string{"uuid;x"}}); p.Status != protocol.StatusFailed {
		t.Error("bad field name must be rejected")
	}

	call(t, d, "xenserver.vm.snapshot_promote", map[string]any{"uuid": "snap-1", "new_name": "restored"})
	if lastCall(t, log, "snapshot-param-set") != "snapshot-param-set uuid=snap-1 is-a-template=false" || lastCall(t, log, "vm-param-set") != "vm-param-set uuid=snap-1 name-label=restored" {
		t.Error("snapshot_promote calls wrong")
	}
	call(t, d, "xenserver.sr.scan", map[string]any{"uuid": "sr-1"})
	if lastCall(t, log, "sr-scan") != "sr-scan uuid=sr-1" {
		t.Error("sr.scan call wrong")
	}
	if p, o := call(t, d, "xenserver.sr.scan", map[string]any{"uuid": "sr-1", "async": true}); p.Status != protocol.StatusRejected {
		t.Errorf("without a job manager async must be rejected, got %s %v", p.Status, o)
	}
}
