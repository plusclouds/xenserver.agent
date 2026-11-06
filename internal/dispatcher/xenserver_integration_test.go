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
	"github.com/plusclouds/ubuntu-agent/internal/modules/services"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

// This test drives the real Dispatcher end-to-end against fake xe/ovs-vsctl/
// ovs-ofctl/mount/umount binaries standing in for a real XenServer/XCP-ng
// host, to catch dispatcher wiring/argument-passing bugs that the modules'
// own unit tests (which call methods directly) wouldn't. It is not a
// substitute for testing against real hardware — see README.md.

const xeStubScript = `#!/bin/bash
cmd="$1"; shift
args="$*"
case "$cmd" in
  vm-list)
    if [[ "$args" == *"--minimal"* ]]; then
      echo "vm-uuid-1"
    else
      cat <<'BLOCK'
uuid ( RO): vm-uuid-1
name-label ( RW): test-vm
power-state ( RO): running
resident-on ( RO): host-uuid-1
VCPUs-number ( RO): 2
memory-actual ( RO): 1073741824
is-control-domain ( RO): false
is-a-template ( RO): false
BLOCK
    fi
    ;;
  vm-start) exit 0 ;;
  sr-create) echo "new-sr-uuid-1" ;;
  network-param-get) echo "xenbr0" ;;
  *) echo "unhandled xe command: $cmd $args" >&2; exit 1 ;;
esac
`

const ovsVsctlStubScript = `#!/bin/bash
cmd="$1"
case "$cmd" in
  list-br) echo "xenbr0"; echo "xenbr1" ;;
  add-port) exit 0 ;;
  *) echo "unhandled ovs-vsctl: $*" >&2; exit 1 ;;
esac
`

const ovsOfctlStubScript = `#!/bin/bash
cmd="$1"
case "$cmd" in
  add-flow) exit 0 ;;
  dump-flows) echo " cookie=0x0, duration=1.2s, table=0, priority=100,ip actions=drop" ;;
  *) echo "unhandled ovs-ofctl: $*" >&2; exit 1 ;;
esac
`

const trueStubScript = "#!/bin/bash\nexit 0\n"

const ipmitoolStubScript = `#!/bin/bash
if [[ "$1" == "sensor" ]]; then
  echo "CPU1 Temp        | 45.000     | degrees C  | ok    | 0.000     | 0.000     | 0.000     | 90.000    | 95.000    | 100.000"
  exit 0
fi
echo "unhandled ipmitool args: $*" >&2
exit 1
`

func writeStub(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("writing stub %s: %v", name, err)
	}
	return path
}

// fakeServiceManager satisfies services.Manager without touching D-Bus;
// this test doesn't exercise services.* operations.
type fakeServiceManager struct{}

func (fakeServiceManager) List(context.Context) ([]services.ServiceInfo, error) { return nil, nil }
func (fakeServiceManager) Get(context.Context, string) (*services.ServiceInfo, error) {
	return nil, nil
}
func (fakeServiceManager) Start(context.Context, string) (*services.ActionResult, error) {
	return nil, nil
}
func (fakeServiceManager) Stop(context.Context, string) (*services.ActionResult, error) {
	return nil, nil
}
func (fakeServiceManager) Restart(context.Context, string) (*services.ActionResult, error) {
	return nil, nil
}
func (fakeServiceManager) Reload(context.Context, string) (*services.ActionResult, error) {
	return nil, nil
}
func (fakeServiceManager) Enable(context.Context, string) (*services.ActionResult, error) {
	return nil, nil
}
func (fakeServiceManager) Disable(context.Context, string) (*services.ActionResult, error) {
	return nil, nil
}

func buildCommand(t *testing.T, op string, params any) protocol.Envelope {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshalling params: %v", err)
	}
	env, err := protocol.New("agent-uuid", protocol.TypeCommand, protocol.CommandPayload{
		Operation: op,
		Params:    raw,
	})
	if err != nil {
		t.Fatalf("building envelope: %v", err)
	}
	return env
}

func assertCompleted(t *testing.T, env protocol.Envelope) protocol.ResultPayload {
	t.Helper()
	var payload protocol.ResultPayload
	if err := env.DecodePayload(&payload); err != nil {
		t.Fatalf("decoding result payload: %v", err)
	}
	if payload.Status != protocol.StatusCompleted {
		t.Errorf("expected status %q, got %q (message: %s)", protocol.StatusCompleted, payload.Status, payload.Message)
	}
	return payload
}

func TestDispatcher_XenServerOpenVSwitchNFSMount_Integration(t *testing.T) {
	dir := t.TempDir()
	xePath := writeStub(t, dir, "xe", xeStubScript)
	vsctlPath := writeStub(t, dir, "ovs-vsctl", ovsVsctlStubScript)
	ofctlPath := writeStub(t, dir, "ovs-ofctl", ovsOfctlStubScript)
	mountPath := writeStub(t, dir, "mount", trueStubScript)
	umountPath := writeStub(t, dir, "umount", trueStubScript)
	ipmitoolPath := writeStub(t, dir, "ipmitool", ipmitoolStubScript)

	logger := zap.NewNop()
	exec := executor.New(logger, false)

	xenMod := xenserver.New(exec, xePath, filepath.Join(dir, "no-such-inventory"), filepath.Join(dir, "no-such.sock"), ipmitoolPath, logger)
	ovsMod := openvswitch.New(exec, vsctlPath, ofctlPath, xePath, logger)
	nfsMod := nfsmount.New(exec, mountPath, umountPath, logger)
	sysMod := system.New(nil)

	allowedOps := []string{
		"xenserver.vm.list", "xenserver.vm.start", "xenserver.sr.create",
		"xenserver.ipmi.sensors",
		"openvswitch.bridge.list", "openvswitch.flow.add",
		"nfsmount.mount", "nfsmount.unmount",
	}

	disp := dispatcher.New(sysMod, fakeServiceManager{}, exec, xenMod, ovsMod, nfsMod, nil,
		"agent-uuid", allowedOps, nil, logger)

	ctx := context.Background()

	t.Run("xenserver.vm.list", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "xenserver.vm.list", map[string]any{}))
		payload := assertCompleted(t, result)
		out, _ := json.Marshal(payload.Output)
		if !json.Valid(out) {
			t.Errorf("output is not valid JSON: %s", out)
		}
	})

	t.Run("xenserver.vm.start by uuid", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "xenserver.vm.start", map[string]any{"uuid": "vm-uuid-1"}))
		assertCompleted(t, result)
	})

	t.Run("xenserver.sr.create", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "xenserver.sr.create", map[string]any{
			"name": "test-sr", "type": "nfs", "shared": true, "host_uuid": "host-uuid-1",
			"device_config": map[string]string{"server": "10.0.0.5", "serverpath": "/export/vol1"},
		}))
		assertCompleted(t, result)
	})

	t.Run("xenserver.ipmi.sensors", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "xenserver.ipmi.sensors", map[string]any{}))
		payload := assertCompleted(t, result)
		out, err := json.Marshal(payload.Output)
		if err != nil {
			t.Fatalf("marshalling output: %v", err)
		}
		if !strings.Contains(string(out), "CPU1 Temp") {
			t.Errorf("expected output to contain parsed sensor name, got: %s", out)
		}
	})

	t.Run("openvswitch.bridge.list", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "openvswitch.bridge.list", map[string]any{}))
		assertCompleted(t, result)
	})

	t.Run("openvswitch.flow.add", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "openvswitch.flow.add", map[string]any{
			"bridge": "xenbr0", "flow_spec": "priority=100,ip,actions=drop",
		}))
		assertCompleted(t, result)
	})

	mountPoint := filepath.Join(dir, "mnt")

	t.Run("nfsmount.mount", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "nfsmount.mount", map[string]any{
			"server": "10.0.0.5", "export_path": "/export/vol1", "mount_point": mountPoint,
		}))
		assertCompleted(t, result)
	})

	t.Run("nfsmount.unmount", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "nfsmount.unmount", map[string]any{"mount_point": mountPoint}))
		assertCompleted(t, result)
	})

	t.Run("disallowed operation is rejected", func(t *testing.T) {
		result := disp.Dispatch(ctx, buildCommand(t, "xenserver.vm.reboot", map[string]any{"uuid": "vm-uuid-1"}))
		var payload protocol.ResultPayload
		if err := result.DecodePayload(&payload); err != nil {
			t.Fatalf("decoding result payload: %v", err)
		}
		if payload.Status != protocol.StatusRejected {
			t.Errorf("expected status %q for an op not in allowed_operations, got %q", protocol.StatusRejected, payload.Status)
		}
	})
}
