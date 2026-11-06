// Package openvswitch manages Open vSwitch bridges, VXLAN tunnel ports, and
// raw flow rules on the XenServer/XCP-ng host's OVS backend via ovs-vsctl
// and ovs-ofctl.
package openvswitch

// BridgeInfo is a single OVS bridge name.
type BridgeInfo struct {
	Name string `json:"name"`
}

// PortInfo is a single port name on a bridge.
type PortInfo struct {
	Name string `json:"name"`
}

// FlowEntry is one raw line of "ovs-ofctl dump-flows" output. OVS flow
// syntax is a rich DSL — kept as an opaque string rather than decomposed
// into structured fields, matching how every real OVS tool treats flow
// specs.
type FlowEntry struct {
	Raw string `json:"raw"`
}

// ActionResult is the outcome of a state-changing openvswitch.* operation.
type ActionResult struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	Output    string `json:"output,omitempty"`
}
