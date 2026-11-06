package dispatcher_test

import (
	"context"
	"encoding/json"
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

// xe stub that records every invocation, one argument per line (arguments of a
// call separated by a "--" line), so tests can assert exactly what xe received.
const recordingXe = `#!/bin/bash
{ printf '%s\n' "$@"; echo '--'; } >> "$XE_LOG"
cmd="$1"; shift
case "$cmd" in
  vm-list)
    if [[ "$*" == *"--minimal"* ]]; then echo "vm-uuid-1"; else
      printf 'name-label ( RW): test-vm\npower-state ( RO): halted\nis-control-domain ( RO): false\n'
    fi ;;
  vm-param-set) if [[ -n "$XE_FAIL" ]]; then echo "boom" >&2; exit 1; fi ;;
  vm-param-remove|vm-memory-limits-set|vm-destroy|vm-pause) exit 0 ;;
  vm-param-get) echo "web1" ;;
  vm-param-list) printf 'uuid ( RO): vm-uuid-1\nname-label ( RW): test-vm\nxenstore-data (MRW): k: v\n' ;;
  console-list) printf 'uuid ( RO): c1\nprotocol ( RO): rfb\nlocation ( RO): https://h/console?ref=x\n' ;;
  template-list) echo "tmpl-uuid-1" ;;
  vm-install|vm-create|vm-clone) echo "new-vm-uuid" ;;
  *) echo "unhandled xe: $cmd" >&2; exit 1 ;;
esac
`

func newVMConfigDispatcher(t *testing.T) (*dispatcher.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "xe.log")
	t.Setenv("XE_LOG", logPath)
	logger := zap.NewNop()
	xe := writeStub(t, dir, "xe", recordingXe)
	ok := writeStub(t, dir, "true", trueStubScript)
	exec := executor.New(logger, false)
	ops := []string{"xenserver.vm.param_set", "xenserver.vm.param_get", "xenserver.vm.param_list",
		"xenserver.vm.memory_limits_set", "xenserver.vm.console_list", "xenserver.vm.create",
		"xenserver.vm.clone", "xenserver.vm.destroy", "xenserver.vm.pause"}
	d := dispatcher.New(system.New(nil), fakeServiceManager{}, exec,
		xenserver.New(exec, xe, filepath.Join(dir, "inv"), filepath.Join(dir, "sock"), ok, logger),
		openvswitch.New(exec, ok, ok, xe, logger), nfsmount.New(exec, ok, ok, logger), nil,
		"agent-uuid", ops, nil, logger)
	return d, logPath
}

func run(t *testing.T, d *dispatcher.Dispatcher, op string, params map[string]any) (protocol.ResultPayload, map[string]any) {
	t.Helper()
	var p protocol.ResultPayload
	res := d.Dispatch(context.Background(), buildCommand(t, op, params))
	if err := res.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	m, _ := p.Output.(map[string]any)
	return p, m
}

func xeCalls(t *testing.T, logPath string) [][]string {
	t.Helper()
	raw, _ := os.ReadFile(logPath)
	var calls [][]string
	var cur []string
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if l == "--" {
			calls = append(calls, cur)
			cur = nil
		} else {
			cur = append(cur, l)
		}
	}
	return calls
}

func TestParamSetPassesValuesAsSingleArguments(t *testing.T) {
	d, log := newVMConfigDispatcher(t)
	value := `web "one" it's; a=b  c`
	p, out := run(t, d, "xenserver.vm.param_set", map[string]any{"uuid": "vm-uuid-1",
		"set": map[string]any{"xenstore-data:vm-data/note": value, "VCPUs-at-startup": "2", "VCPUs-max": "4", "name-label": "web1"}})
	if p.Status != protocol.StatusCompleted || out["success"] != true {
		t.Fatalf("result: %+v %v", p, out)
	}
	var sets [][]string
	for _, c := range xeCalls(t, log) {
		if c[0] == "vm-param-set" {
			sets = append(sets, c)
		}
	}
	if len(sets) != 4 {
		t.Fatalf("want 4 vm-param-set calls, got %v", sets)
	}
	if sets[0][2] != "VCPUs-max=4" || sets[1][2] != "VCPUs-at-startup=2" {
		t.Errorf("VCPUs-max must be applied before VCPUs-at-startup: %v %v", sets[0], sets[1])
	}
	found := false
	for _, s := range sets {
		if len(s) != 3 {
			t.Errorf("each call must be exactly [cmd uuid key=value], got %q", s)
		}
		if s[2] == "xenstore-data:vm-data/note="+value {
			found = true
		}
	}
	if !found {
		t.Errorf("value with spaces/quotes/semicolons must arrive intact as one argument: %v", sets)
	}
}

func TestParamSetRejectsBadKeysBeforeRunningXe(t *testing.T) {
	for _, set := range []map[string]any{
		{"is-control-domain": "true"}, {"xenstore-data:a b": "x"}, {"name-label": "ok", "power-state": "x"}, {"VCPUs-max": "0"},
	} {
		d, log := newVMConfigDispatcher(t)
		p, _ := run(t, d, "xenserver.vm.param_set", map[string]any{"uuid": "vm-uuid-1", "set": set})
		if p.Status != protocol.StatusFailed {
			t.Errorf("%v: want failed, got %s", set, p.Status)
		}
		if _, err := os.Stat(log); err == nil {
			t.Errorf("%v: xe must not run when validation fails (nothing may be changed)", set)
		}
	}
}

func TestParamUnsetAndMemoryAndReadOps(t *testing.T) {
	d, log := newVMConfigDispatcher(t)
	if p, o := run(t, d, "xenserver.vm.param_set", map[string]any{"uuid": "vm-uuid-1", "unset": []string{"xenstore-data:old"}}); p.Status != protocol.StatusCompleted || o["success"] != true {
		t.Fatalf("unset: %+v", p)
	}
	last := xeCalls(t, log)
	if got := strings.Join(last[len(last)-1], " "); got != "vm-param-remove uuid=vm-uuid-1 param-name=xenstore-data param-key=old" {
		t.Errorf("unset call: %q", got)
	}
	if p, _ := run(t, d, "xenserver.vm.memory_limits_set", map[string]any{"uuid": "vm-uuid-1",
		"static_min": 512, "static_max": 100, "dynamic_min": 512, "dynamic_max": 512}); p.Status != protocol.StatusFailed {
		t.Error("static_max < dynamic_max must be rejected")
	}
	if p, o := run(t, d, "xenserver.vm.memory_limits_set", map[string]any{"uuid": "vm-uuid-1",
		"static_min": 1, "static_max": 4, "dynamic_min": 2, "dynamic_max": 3}); p.Status != protocol.StatusCompleted || o["success"] != true {
		t.Errorf("valid limits: %+v", p)
	}
	if _, o := run(t, d, "xenserver.vm.memory_limits_set", map[string]any{"uuid": "vm-uuid-1", "memory_bytes": 1073741824}); o["success"] != true {
		t.Errorf("memory_bytes: %v", o)
	}
	var memCall string
	for _, c := range xeCalls(t, log) {
		if c[0] == "vm-memory-limits-set" {
			memCall = strings.Join(c, " ")
		}
	}
	if memCall != "vm-memory-limits-set uuid=vm-uuid-1 static-min=1073741824 static-max=1073741824 dynamic-min=1073741824 dynamic-max=1073741824" {
		t.Errorf("memory_bytes must set all four equal: %q", memCall)
	}
	_, got := run(t, d, "xenserver.vm.param_get", map[string]any{"uuid": "vm-uuid-1", "param": "xenstore-data", "param_key": "k"})
	if got["value"] != "web1" || got["param"] != "xenstore-data" {
		t.Errorf("param_get: %v", got)
	}
	_, pl := run(t, d, "xenserver.vm.param_list", map[string]any{"uuid": "vm-uuid-1"})
	if maps, _ := pl["maps"].(map[string]any); maps["xenstore-data"] == nil {
		t.Errorf("param_list must return map fields as objects: %v", pl)
	}
	if p, _ := run(t, d, "xenserver.vm.param_get", map[string]any{"uuid": "vm-uuid-1", "param": "name-label;rm"}); p.Status != protocol.StatusFailed {
		t.Error("bad param name must be rejected")
	}
}

func TestCreateCloneDestroyPause(t *testing.T) {
	d, log := newVMConfigDispatcher(t)
	_, o := run(t, d, "xenserver.vm.create", map[string]any{"new_name": "web1", "template": "Debian 12", "sr_uuid": "sr-1", "description": "d"})
	if data, _ := o["data"].(map[string]any); data["uuid"] != "new-vm-uuid" {
		t.Fatalf("create: %v", o)
	}
	var install []string
	for _, c := range xeCalls(t, log) {
		if c[0] == "vm-install" {
			install = c
		}
	}
	if strings.Join(install, " ") != "vm-install template=tmpl-uuid-1 new-name-label=web1 new-name-description=d sr-uuid=sr-1" {
		t.Errorf("vm-install args: %q", install)
	}
	if _, o := run(t, d, "xenserver.vm.create", map[string]any{"new_name": "empty"}); o["operation"] != "vm-create" {
		t.Errorf("no template must use vm-create: %v", o)
	}
	_, o = run(t, d, "xenserver.vm.clone", map[string]any{"uuid": "vm-uuid-1", "new_name": "copy"})
	if data, _ := o["data"].(map[string]any); data["uuid"] != "new-vm-uuid" {
		t.Errorf("clone: %v", o)
	}
	if p, _ := run(t, d, "xenserver.vm.clone", map[string]any{"uuid": "vm-uuid-1"}); p.Status != protocol.StatusFailed {
		t.Error("clone without new_name must fail")
	}

	// destroy needs confirm and a non-control-domain VM
	if p, _ := run(t, d, "xenserver.vm.destroy", map[string]any{"uuid": "vm-uuid-1"}); p.Status != protocol.StatusFailed ||
		!strings.Contains(p.Message, "confirm=true") {
		t.Errorf("destroy without confirm must be refused: %+v", p)
	}
	for _, c := range xeCalls(t, log) {
		if c[0] == "vm-destroy" {
			t.Fatal("vm-destroy must not run without confirm")
		}
	}
	if _, o := run(t, d, "xenserver.vm.destroy", map[string]any{"uuid": "vm-uuid-1", "confirm": true}); o["success"] != true {
		t.Errorf("confirmed destroy: %v", o)
	}
	if _, o := run(t, d, "xenserver.vm.pause", map[string]any{"uuid": "vm-uuid-1"}); o["success"] != true {
		t.Errorf("pause: %v", o)
	}
}

func TestFailedParamSetDoesNotLeakSecrets(t *testing.T) {
	d, _ := newVMConfigDispatcher(t)
	t.Setenv("XE_FAIL", "1")
	secret := "#cloud-config\npassword: hunter2"
	var raw protocol.ResultPayload
	res := d.Dispatch(context.Background(), buildCommand(t, "xenserver.vm.param_set", map[string]any{"uuid": "vm-uuid-1",
		"set": map[string]any{"xenstore-data:vm-data/user-data": secret}}))
	if err := res.DecodePayload(&raw); err != nil {
		t.Fatal(err)
	}
	out := raw.Output.(map[string]any)
	if out["success"] != false {
		t.Fatalf("expected a failure: %v", out)
	}
	blob, _ := json.Marshal(raw)
	for _, leak := range []string{"hunter2", "cloud-config"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("%q leaked into the result a client receives: %s", leak, blob)
		}
	}
	if cmd := out["detail"].(map[string]any)["command"].(string); !strings.Contains(cmd, "xenstore-data:vm-data/user-data=***") {
		t.Errorf("detail.command must show the masked argument: %q", cmd)
	}
}
