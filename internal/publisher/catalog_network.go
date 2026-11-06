package publisher

import "github.com/plusclouds/ubuntu-agent/internal/protocol"

func param(name, typ string, required bool, desc string) protocol.OperationParam {
	return protocol.OperationParam{Name: name, Type: typ, Required: required, Description: desc}
}

func withParams(base []protocol.OperationParam, extra ...protocol.OperationParam) []protocol.OperationParam {
	return append(append([]protocol.OperationParam{}, base...), extra...)
}

func add(op, desc string, params ...protocol.OperationParam) {
	operationCatalog[op] = protocol.OperationSchema{Operation: op, Description: desc, Params: params}
}

// Network, VLAN, PIF, PBD, task, SR and host operations, plus the list filters
// and snapshot promotion added with them.
func init() {
	vifSel := []protocol.OperationParam{
		param("vif_uuid", "string", false, "VIF uuid. If omitted, uuid/name plus device are used."),
		param("uuid", "string", false, "VM uuid, used with device when vif_uuid is omitted."),
		param("name", "string", false, "VM name-label, used with device when vif_uuid is omitted."),
		param("device", "string", false, "Card position (\"0\", ...), used with uuid/name when vif_uuid is omitted."),
	}
	uuidParam := func(what string) protocol.OperationParam { return param("uuid", "string", true, what+" uuid.") }
	confirm := param("confirm", "boolean", true, "Must be true.")

	add("xenserver.vif.create", "Add a network card to a VM on a network (uuid or name-label) at a device position. The VM should be halted. Returns data.uuid.",
		withParams(vmIdentifierParams,
			param("device", "string", true, "Card position, e.g. \"0\"."),
			param("network_uuid", "string", false, "Network uuid. Either network_uuid or network_name is required."),
			param("network_name", "string", false, "Network name-label."),
			param("mac", "string", false, "MAC address aa:bb:cc:dd:ee:ff (generated if omitted)."),
			param("mtu", "integer", false, "MTU 68-9216."))...)
	add("xenserver.inventory.snapshot", "One-call read of the host and pool: pool (with host_count/is_pool_member), hosts, pifs, networks, vlans, srs and pbds, each as the full xe parameter list ({uuid, params, maps, sets}, xe-style keys, secrets masked). Optionally the guest VMs too.",
		param("include_vms", "boolean", false, "Also include every guest VM's full parameter list (excludes control domains, templates and snapshots)."))
	add("xenserver.vif.plug", "Hot-attach a network card to a running VM (needs guest tools). Create it first with xenserver.vif.create.", vifSel...)
	add("xenserver.vif.unplug", "Hot-detach a network card from a running VM (needs guest tools); the card stays defined and can be plugged again or removed with xenserver.vif.remove.", vifSel...)
	add("xenserver.vif.param_list", "All parameters of a network card as JSON (params, maps, sets).", vifSel...)
	add("xenserver.vif.param_set", "Change allowlisted VIF parameters: qos_algorithm_type=ratelimit with qos_algorithm_params:kbps for rate limits, other-config:<key>, locking-mode, ipv4-allowed, ipv6-allowed, and MAC. A MAC change recreates the card (VM must be halted; the VIF uuid changes, see data.uuid).",
		withParams(vifSel, param("set", "object", true, "Map of parameter to value, e.g. {\"qos_algorithm_type\":\"ratelimit\",\"qos_algorithm_params:kbps\":\"10000\"}."))...)
	add("xenserver.network.create", "Create an internal network (no physical interface). Returns data.uuid.",
		param("new_name", "string", true, "Name-label."), param("description", "string", false, "Name-description."), param("mtu", "integer", false, "MTU 68-9216."))
	add("xenserver.network.destroy", "Delete a network. IRREVERSIBLE; requires confirm=true; refused while cards or interfaces use it.", uuidParam("Network"), confirm)
	add("xenserver.network.param_list", "All parameters of a network as JSON.", uuidParam("Network"))
	add("xenserver.vlan.list", "List VLANs: uuid, tag, tagged_pif, untagged_pif. Optional tag filter.", param("tag", "integer", false, "Only VLANs with this tag."))
	add("xenserver.vlan.create", "Create a VLAN (tag 1-4094) on a physical interface, carried by a network (create it with network.create first). May interrupt traffic on the interface. Returns data.uuid (the VLAN, for vlan.destroy) and data.pif_uuid (the new tagged interface).",
		param("pif_uuid", "string", true, "Physical interface."), param("network_uuid", "string", true, "Network that will carry the VLAN."), param("tag", "integer", true, "VLAN tag."))
	add("xenserver.vlan.destroy", "Delete a VLAN. IRREVERSIBLE; requires confirm=true.", uuidParam("VLAN"), confirm)
	add("xenserver.vlan.param_list", "All parameters of a VLAN as JSON.", uuidParam("VLAN"))
	add("xenserver.pif.list", "List network interfaces: uuid, device, vlan (-1 untagged), network_uuid, host_uuid, mac, currently_attached, ip, netmask, gateway, management, physical, mtu, bond_slave_of, bond_master_of.",
		param("vlan", "integer", false, "Only this VLAN tag (-1 = untagged interfaces)."), param("network_uuid", "string", false, "Only this network."), param("host_uuid", "string", false, "Only this host."))
	add("xenserver.pif.param_list", "All parameters of an interface as JSON.", uuidParam("Interface"))
	add("xenserver.pif.param_get", "Read one interface parameter or map key.", uuidParam("Interface"),
		param("param", "string", true, "Parameter name."), param("param_key", "string", false, "Key within a map parameter."))
	add("xenserver.pif.unplug", "Take an interface down. Requires confirm=true; the management interface is always refused.", uuidParam("Interface"), confirm)
	add("xenserver.pbd.list", "List SR attachments: uuid, sr_uuid, host_uuid, currently_attached, device_config (secrets masked).",
		param("sr_uuid", "string", false, "Only this SR."), param("host_uuid", "string", false, "Only this host."))
	add("xenserver.pbd.param_list", "All parameters of a PBD as JSON.", uuidParam("PBD"))
	add("xenserver.task.list", "List xapi tasks: uuid, name_label, status, progress, created, finished, result, error_info.")
	add("xenserver.sr.scan", "Rescan an SR so new or removed disks appear. Can be slow; use async.", withParams(nil,
		param("uuid", "string", false, "SR uuid. Either uuid or name is required."), param("name", "string", false, "SR name-label."))...)
	add("xenserver.sr.param_list", "All parameters of an SR as JSON.", param("uuid", "string", false, "SR uuid. Either uuid or name is required."), param("name", "string", false, "SR name-label."))
	add("xenserver.sr.param_get", "Read one SR parameter (e.g. type) or map key.", param("uuid", "string", false, "SR uuid. Either uuid or name is required."),
		param("name", "string", false, "SR name-label."), param("param", "string", true, "Parameter name."), param("param_key", "string", false, "Key within a map parameter."))
	add("xenserver.host.param_list", "All parameters of a host as JSON. Defaults to this host.", param("uuid", "string", false, "Host uuid (default: this host)."))
	add("xenserver.vm.snapshot_promote", "Turn a snapshot into an ordinary VM in place (snapshot-param-set is-a-template=false). The uuid stays the same.",
		param("uuid", "string", true, "Snapshot uuid."), param("new_name", "string", false, "New name-label."))

	// Filters on existing list operations.
	extend := func(op string, extra ...protocol.OperationParam) {
		if s, ok := operationCatalog[op]; ok {
			s.Params = withParams(s.Params, extra...)
			operationCatalog[op] = s
		}
	}
	extend("xenserver.vm.list",
		param("name_label", "string", false, "Only VMs with this name-label."),
		param("is_control_domain", "boolean", false, "true lists control domains (dom0); setting any filter disables the default exclusion."),
		param("is_a_template", "boolean", false, "true lists templates."),
		param("fields", "array", false, "xe field names to return per VM (e.g. [\"uuid\",\"name-label\",\"power-state\"]); returns one object per VM with exactly those fields."))
	extend("xenserver.sr.list",
		param("type", "string", false, "Only this SR type (nfs, iso, lvm, ...)."),
		param("name_label", "string", false, "Only this name-label."),
		param("content_type", "string", false, "Only this content type (iso, user, ...)."))
	extend("xenserver.network.list",
		param("name_label", "string", false, "Only this name-label."),
		param("bridge", "string", false, "Only this bridge."),
		param("pif_uuid", "string", false, "Only the network of this interface."))
}
