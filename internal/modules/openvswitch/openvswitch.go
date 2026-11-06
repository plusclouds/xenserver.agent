package openvswitch

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/executor"
)

// Module manages Open vSwitch state via ovs-vsctl/ovs-ofctl. xeBinary is
// used only to resolve a xapi network uuid to its underlying bridge name
// (the hybrid VXLAN pattern) — this package otherwise has no other
// dependency on internal/modules/xenserver.
type Module struct {
	exec        *executor.Executor
	vsctlBinary string
	ofctlBinary string
	xeBinary    string
	logger      *zap.Logger
}

// New creates an openvswitch Module.
func New(exec *executor.Executor, vsctlBinary, ofctlBinary, xeBinary string, logger *zap.Logger) *Module {
	return &Module{exec: exec, vsctlBinary: vsctlBinary, ofctlBinary: ofctlBinary, xeBinary: xeBinary, logger: logger}
}

func (m *Module) runVSCtl(ctx context.Context, args ...string) (string, error) {
	return m.run(ctx, m.vsctlBinary, args...)
}

func (m *Module) runOfctl(ctx context.Context, args ...string) (string, error) {
	return m.run(ctx, m.ofctlBinary, args...)
}

func (m *Module) run(ctx context.Context, binary string, args ...string) (string, error) {
	stdout, stderr, err := m.exec.Execute(ctx, binary, args...)
	if err != nil {
		if s := strings.TrimSpace(stderr); s != "" {
			return stdout, fmt.Errorf("%w: %s", err, s)
		}
		return stdout, err
	}
	return stdout, nil
}

// resolveBridge returns bridge unchanged if given; otherwise resolves it
// from a xapi network uuid via "xe network-param-get" — the hybrid pattern
// that keeps xcp-networkd aware of the bridge so it isn't torn down on
// reconciliation.
func (m *Module) resolveBridge(ctx context.Context, bridge, networkUUID string) (string, error) {
	if bridge != "" {
		return bridge, nil
	}
	if networkUUID == "" {
		return "", fmt.Errorf("bridge or network_uuid is required")
	}
	out, err := m.run(ctx, m.xeBinary, "network-param-get", "uuid="+networkUUID, "param-name=bridge")
	if err != nil {
		return "", fmt.Errorf("resolving bridge for network %s: %w", networkUUID, err)
	}
	return strings.TrimSpace(out), nil
}

// --- read-only ---------------------------------------------------------

// BridgeList returns every OVS bridge on the host.
func (m *Module) BridgeList(ctx context.Context) ([]BridgeInfo, error) {
	out, err := m.runVSCtl(ctx, "list-br")
	if err != nil {
		return nil, fmt.Errorf("listing bridges: %w", err)
	}
	lines := splitLines(out)
	bridges := make([]BridgeInfo, 0, len(lines))
	for _, l := range lines {
		bridges = append(bridges, BridgeInfo{Name: l})
	}
	return bridges, nil
}

// PortList returns every port on the given bridge.
func (m *Module) PortList(ctx context.Context, bridge string) ([]PortInfo, error) {
	if bridge == "" {
		return nil, fmt.Errorf("bridge is required")
	}
	out, err := m.runVSCtl(ctx, "list-ports", bridge)
	if err != nil {
		return nil, fmt.Errorf("listing ports on %s: %w", bridge, err)
	}
	lines := splitLines(out)
	ports := make([]PortInfo, 0, len(lines))
	for _, l := range lines {
		ports = append(ports, PortInfo{Name: l})
	}
	return ports, nil
}

// FlowList returns the raw flow table for the given bridge.
func (m *Module) FlowList(ctx context.Context, bridge string) ([]FlowEntry, error) {
	if bridge == "" {
		return nil, fmt.Errorf("bridge is required")
	}
	out, err := m.runOfctl(ctx, "dump-flows", bridge)
	if err != nil {
		return nil, fmt.Errorf("dumping flows on %s: %w", bridge, err)
	}
	lines := splitLines(out)
	flows := make([]FlowEntry, 0, len(lines))
	for _, l := range lines {
		flows = append(flows, FlowEntry{Raw: l})
	}
	return flows, nil
}

// --- state-changing ------------------------------------------------------

// VXLANCreate adds a VXLAN encapsulation port to a bridge, identified either
// directly or via a xapi network uuid (resolveBridge's hybrid pattern).
func (m *Module) VXLANCreate(ctx context.Context, bridge, networkUUID, portName, remoteIP, localIP string, vni int) (*ActionResult, error) {
	resolvedBridge, err := m.resolveBridge(ctx, bridge, networkUUID)
	if err != nil {
		return nil, err
	}
	if portName == "" || remoteIP == "" {
		return nil, fmt.Errorf("port_name and remote_ip are required")
	}

	args := []string{
		"add-port", resolvedBridge, portName, "--",
		"set", "interface", portName, "type=vxlan",
		"options:remote_ip=" + remoteIP,
	}
	if localIP != "" {
		args = append(args, "options:local_ip="+localIP)
	}
	if vni != 0 {
		args = append(args, "options:key="+strconv.Itoa(vni))
	}

	stdout, err := m.runVSCtl(ctx, args...)
	if err != nil {
		return &ActionResult{Operation: "vxlan-create", Target: portName, Success: false, Message: err.Error()}, nil
	}
	return &ActionResult{Operation: "vxlan-create", Target: portName, Success: true,
		Message: "VXLAN port created successfully.", Output: strings.TrimSpace(stdout)}, nil
}

// VXLANRemove removes a VXLAN port from a bridge.
func (m *Module) VXLANRemove(ctx context.Context, bridge, portName string) (*ActionResult, error) {
	if bridge == "" || portName == "" {
		return nil, fmt.Errorf("bridge and port_name are required")
	}
	if _, err := m.runVSCtl(ctx, "del-port", bridge, portName); err != nil {
		return &ActionResult{Operation: "vxlan-remove", Target: portName, Success: false, Message: err.Error()}, nil
	}
	return &ActionResult{Operation: "vxlan-remove", Target: portName, Success: true,
		Message: "VXLAN port removed successfully."}, nil
}

// FlowAdd installs a raw OVS flow rule on a bridge.
func (m *Module) FlowAdd(ctx context.Context, bridge, flowSpec string) (*ActionResult, error) {
	if bridge == "" {
		return nil, fmt.Errorf("bridge is required")
	}
	if err := validateFlowSpec(flowSpec); err != nil {
		return nil, err
	}
	if _, err := m.runOfctl(ctx, "add-flow", bridge, flowSpec); err != nil {
		return &ActionResult{Operation: "flow-add", Target: flowSpec, Success: false, Message: err.Error()}, nil
	}
	return &ActionResult{Operation: "flow-add", Target: flowSpec, Success: true,
		Message: "flow added successfully."}, nil
}

// FlowRemove removes OVS flow rules matching matchSpec on a bridge.
func (m *Module) FlowRemove(ctx context.Context, bridge, matchSpec string) (*ActionResult, error) {
	if bridge == "" {
		return nil, fmt.Errorf("bridge is required")
	}
	if err := validateFlowSpec(matchSpec); err != nil {
		return nil, err
	}
	if _, err := m.runOfctl(ctx, "del-flows", bridge, matchSpec); err != nil {
		return &ActionResult{Operation: "flow-remove", Target: matchSpec, Success: false, Message: err.Error()}, nil
	}
	return &ActionResult{Operation: "flow-remove", Target: matchSpec, Success: true,
		Message: "flow(s) removed successfully."}, nil
}
