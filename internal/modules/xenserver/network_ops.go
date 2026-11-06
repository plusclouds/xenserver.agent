package xenserver

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PIFInfo describes a physical (or VLAN/bond) network interface of a host.
type PIFInfo struct {
	UUID        string   `json:"uuid"`
	Device      string   `json:"device"`
	VLAN        int      `json:"vlan"` // -1 for an untagged interface
	NetworkUUID string   `json:"network_uuid"`
	HostUUID    string   `json:"host_uuid"`
	MAC         string   `json:"mac"`
	Attached    bool     `json:"currently_attached"`
	IP          string   `json:"ip,omitempty"`
	Netmask     string   `json:"netmask,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	Management  bool     `json:"management"`
	Physical    bool     `json:"physical"`
	MTU         int      `json:"mtu"`
	BondSlaveOf string   `json:"bond_slave_of,omitempty"`
	BondMaster  []string `json:"bond_master_of,omitempty"`
}

// VLANInfo describes a VLAN: the tagged PIF carries the tag on top of the
// untagged PIF (the physical interface).
type VLANInfo struct {
	UUID        string `json:"uuid"`
	Tag         int    `json:"tag"`
	TaggedPIF   string `json:"tagged_pif"`
	UntaggedPIF string `json:"untagged_pif"`
}

// PBDInfo describes a storage repository's attachment to a host.
type PBDInfo struct {
	UUID         string            `json:"uuid"`
	SRUUID       string            `json:"sr_uuid"`
	HostUUID     string            `json:"host_uuid"`
	Attached     bool              `json:"currently_attached"`
	DeviceConfig map[string]string `json:"device_config"`
}

// TaskInfo describes a xapi task.
type TaskInfo struct {
	UUID      string  `json:"uuid"`
	NameLabel string  `json:"name_label"`
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	Created   string  `json:"created"`
	Finished  string  `json:"finished,omitempty"`
	Result    string  `json:"result,omitempty"`
	ErrorInfo string  `json:"error_info,omitempty"`
}

var macRe = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func (m *Module) requireUUID(class, uuid string) error {
	if strings.TrimSpace(uuid) == "" {
		return fmt.Errorf("%s uuid is required", class)
	}
	return nil
}

// ---- generic read helpers --------------------------------------------------

// ObjectParamList returns every parameter of an xapi object of the given class
// ("network", "vlan", "pif", "pbd", "sr", "host", "vif", ...).
func (m *Module) ObjectParamList(ctx context.Context, class, uuid string) (*ParamList, error) {
	if err := m.requireUUID(class, uuid); err != nil {
		return nil, err
	}
	return m.paramList(ctx, class, uuid)
}

// HostParamList is ObjectParamList for a host; uuid defaults to this host.
func (m *Module) HostParamList(ctx context.Context, uuid string) (*ParamList, error) {
	if uuid == "" {
		var err error
		if uuid, err = m.localHostUUID(); err != nil {
			return nil, fmt.Errorf("determining local host uuid: %w", err)
		}
	}
	return m.paramList(ctx, "host", uuid)
}

// ObjectParamGet reads one parameter (or map key) of an object of a class.
func (m *Module) ObjectParamGet(ctx context.Context, class, uuid, param, key string) (*VMParamValue, error) {
	if err := m.requireUUID(class, uuid); err != nil {
		return nil, err
	}
	if !paramNameRe.MatchString(param) {
		return nil, fmt.Errorf("param must match [A-Za-z0-9_-]+")
	}
	if key != "" && !mapKeyRe.MatchString(key) {
		return nil, fmt.Errorf("param_key %q must match [A-Za-z0-9_.-/]+", key)
	}
	args := []string{class + "-param-get", "uuid=" + uuid, "param-name=" + param}
	if key != "" {
		args = append(args, "param-key="+key)
	}
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return &VMParamValue{UUID: uuid, Param: param, Key: key, Value: maskedValue(param, key, strings.TrimRight(out, "\r\n"))}, nil
}

// ---- VIF -------------------------------------------------------------------

// VIFCreate adds a network card to a VM on a network (uuid or name-label) at a
// device position. mac and mtu are optional (xapi generates a MAC). The VM
// should be halted, or call vif.plug afterwards. The new VIF's uuid is in Data.uuid.
func (m *Module) VIFCreate(ctx context.Context, vmUUID, vmName, device, networkUUID, networkName, mac string, mtu int) (*ActionResult, error) {
	vm, err := m.resolveVM(ctx, vmUUID, vmName)
	if err != nil {
		return nil, err
	}
	if device == "" {
		return nil, fmt.Errorf("device (the card's position, e.g. \"0\") is required")
	}
	net, err := m.resolveNetwork(ctx, networkUUID, networkName)
	if err != nil {
		return nil, err
	}
	args := []string{"vif-create", "vm-uuid=" + vm, "network-uuid=" + net, "device=" + device}
	if mac != "" {
		mac = strings.ToLower(mac)
		if !macRe.MatchString(mac) {
			return nil, fmt.Errorf("mac %q must look like aa:bb:cc:dd:ee:ff", mac)
		}
		args = append(args, "mac="+mac)
	}
	if mtu != 0 {
		if mtu < 68 || mtu > 9216 {
			return nil, fmt.Errorf("mtu must be between 68 and 9216")
		}
		args = append(args, "mtu="+strconv.Itoa(mtu))
	}
	out, err := m.run(ctx, args...)
	if err != nil {
		return m.failureForVM(ctx, "vif-create", "add a network card to", vm, err), nil
	}
	id := strings.TrimSpace(out)
	return &ActionResult{Operation: "vif-create", Target: vm, Success: true, Message: "Network card created.", Output: id,
		Data: map[string]string{"uuid": id, "vm_uuid": vm, "network_uuid": net, "device": device, "mac": mac}}, nil
}

// vif.param_set allowlist: rate limiting, extra allowed addresses, other-config
// and MAC (which xapi cannot change in place, so the card is recreated).
func validateVIFParam(key, value string) error {
	switch {
	case key == "MAC":
		if !macRe.MatchString(strings.ToLower(value)) {
			return fmt.Errorf("MAC %q must look like aa:bb:cc:dd:ee:ff", value)
		}
	case key == "qos_algorithm_type":
		if value != "ratelimit" && value != "" {
			return fmt.Errorf("qos_algorithm_type must be ratelimit (or empty to remove the limit)")
		}
	case strings.HasPrefix(key, "qos_algorithm_params:"):
		k := strings.TrimPrefix(key, "qos_algorithm_params:")
		if k != "kbps" || func() bool { n, e := strconv.Atoi(value); return e != nil || n < 1 }() {
			return fmt.Errorf("only qos_algorithm_params:kbps (a positive integer) is allowed")
		}
	case strings.HasPrefix(key, "other-config:"):
		if !mapKeyRe.MatchString(strings.TrimPrefix(key, "other-config:")) {
			return fmt.Errorf("invalid other-config key in %q", key)
		}
	case key == "ipv4-allowed" || key == "ipv6-allowed" || key == "locking-mode":
		if key == "locking-mode" && value != "locked" && value != "unlocked" && value != "disabled" && value != "network_default" {
			return fmt.Errorf("locking-mode must be locked, unlocked, disabled or network_default")
		}
	default:
		return fmt.Errorf("VIF parameter %q is not allowed (allowed: MAC, qos_algorithm_type, qos_algorithm_params:kbps, other-config:<key>, locking-mode, ipv4-allowed, ipv6-allowed)", key)
	}
	if len(value) > maxParamValueLen || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid value for %q", key)
	}
	return nil
}

// VIFParamSet changes allowlisted VIF parameters, e.g. a rate limit
// {"qos_algorithm_type":"ratelimit","qos_algorithm_params:kbps":"10000"}.
// A MAC change recreates the card (same VM, device, network, MTU, locking mode;
// the VM must be halted, and the VIF uuid changes — the new one is in Data.uuid).
func (m *Module) VIFParamSet(ctx context.Context, vifUUID, vmUUID, vmName, device string, set map[string]string) (*ActionResult, error) {
	if len(set) == 0 {
		return nil, fmt.Errorf("set is required")
	}
	for k, v := range set {
		if err := validateVIFParam(k, v); err != nil {
			return nil, err
		}
	}
	vif, err := m.resolveVIF(ctx, vifUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, k := range orderedKeys(set) {
		if k == "MAC" {
			continue
		}
		if _, err := m.run(ctx, "vif-param-set", "uuid="+vif, k+"="+set[k]); err != nil {
			r := m.failure("vif-param-set", vif, err)
			r.Message = fmt.Sprintf("setting %s failed after applying %d change(s): %s", k, len(applied), err)
			r.Data = map[string]any{"applied": applied, "failed": k}
			return r, nil
		}
		applied = append(applied, k)
	}
	data := map[string]any{"applied": applied, "uuid": vif}
	if mac, ok := set["MAC"]; ok {
		newUUID, rerr := m.vifRecreateWithMAC(ctx, vif, strings.ToLower(mac))
		if rerr != nil {
			r := &ActionResult{Operation: "vif-param-set", Target: vif, Success: false, Message: rerr.Error(),
				Data: map[string]any{"applied": applied, "failed": "MAC"}}
			return r, nil
		}
		applied = append(applied, "MAC")
		data = map[string]any{"applied": applied, "uuid": newUUID}
	}
	return &ActionResult{Operation: "vif-param-set", Target: vif, Success: true,
		Message: fmt.Sprintf("%d change(s) applied.", len(applied)), Data: data}, nil
}

func orderedKeys(set map[string]string) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	// stable alphabetical order
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// vifRecreateWithMAC recreates a VIF with a new MAC, restoring the original on failure.
func (m *Module) vifRecreateWithMAC(ctx context.Context, vif, mac string) (string, error) {
	out, err := m.run(ctx, "vif-list", "uuid="+vif, "params=vm-uuid,device,MAC,MTU,network-uuid,locking-mode")
	if err != nil {
		return "", fmt.Errorf("reading VIF %s: %w", vif, err)
	}
	b := parseXEBlocks(out)
	if len(b) == 0 {
		return "", fmt.Errorf("VIF %s not found", vif)
	}
	v := b[0]
	create := func(macAddr string) []string {
		a := []string{"vif-create", "vm-uuid=" + v["vm-uuid"], "network-uuid=" + v["network-uuid"], "device=" + v["device"], "mac=" + macAddr}
		if v["MTU"] != "" {
			a = append(a, "mtu="+v["MTU"])
		}
		return a
	}
	if _, err := m.run(ctx, "vif-destroy", "uuid="+vif); err != nil {
		vmName, state := m.vmNameAndState(ctx, v["vm-uuid"])
		var xe *XEError
		if asXE(err, &xe) {
			return "", fmt.Errorf("%s", friendlyDeviceError("change the MAC of a network card on", vmName, state, xe))
		}
		return "", err
	}
	newOut, err := m.run(ctx, create(mac)...)
	if err != nil {
		msg := fmt.Sprintf("creating the card with MAC %s failed: %v", mac, err)
		if _, rerr := m.run(ctx, create(v["MAC"])...); rerr != nil {
			return "", fmt.Errorf("%s — ROLLBACK ALSO FAILED, device %s (mac %s) is now missing: %v", msg, v["device"], v["MAC"], rerr)
		}
		return "", fmt.Errorf("%s — original card restored", msg)
	}
	newUUID := strings.TrimSpace(newOut)
	if mode := v["locking-mode"]; mode != "" && mode != "network_default" {
		_, _ = m.run(ctx, "vif-param-set", "uuid="+newUUID, "locking-mode="+mode)
	}
	return newUUID, nil
}

// ---- networks --------------------------------------------------------------

// NetworkCreate creates an internal network (no physical interface). Attach
// VLANs or cards to it afterwards. The new network's uuid is in Data.uuid.
func (m *Module) NetworkCreate(ctx context.Context, name, description string, mtu int) (*ActionResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("new_name is required")
	}
	args := []string{"network-create", "name-label=" + name}
	if description != "" {
		args = append(args, "name-description="+description)
	}
	if mtu != 0 {
		if mtu < 68 || mtu > 9216 {
			return nil, fmt.Errorf("mtu must be between 68 and 9216")
		}
		args = append(args, "MTU="+strconv.Itoa(mtu))
	}
	out, err := m.run(ctx, args...)
	if err != nil {
		return m.failure("network-create", name, err), nil
	}
	id := strings.TrimSpace(out)
	return &ActionResult{Operation: "network-create", Target: name, Success: true, Message: "Network created.", Output: id,
		Data: map[string]string{"uuid": id}}, nil
}

// NetworkDestroy deletes a network. Irreversible; confirm must be true. xapi
// refuses while VIFs or PIFs still use it.
func (m *Module) NetworkDestroy(ctx context.Context, uuid string, confirm bool) (*ActionResult, error) {
	if err := m.requireUUID("network", uuid); err != nil {
		return nil, err
	}
	if !confirm {
		return nil, fmt.Errorf("destroying network %s is irreversible: pass confirm=true", uuid)
	}
	if _, err := m.run(ctx, "network-destroy", "uuid="+uuid); err != nil {
		r := m.failure("network-destroy", uuid, err)
		var xe *XEError
		if asXE(err, &xe) && strings.Contains(strings.ToLower(xe.Stderr), "in use") {
			r.Message = fmt.Sprintf("Cannot destroy network %s: it still has network cards or interfaces attached; remove them first. (%s)", uuid, strings.TrimSpace(xe.Stderr))
		}
		return r, nil
	}
	return &ActionResult{Operation: "network-destroy", Target: uuid, Success: true, Message: "Network destroyed."}, nil
}

// ---- VLAN ------------------------------------------------------------------

// VLANList lists VLANs, optionally only those with a given tag (0 = all).
func (m *Module) VLANList(ctx context.Context, tag int) ([]VLANInfo, error) {
	args := []string{"vlan-list"}
	if tag > 0 {
		args = append(args, "tag="+strconv.Itoa(tag))
	}
	args = append(args, "params=uuid,tag,tagged-PIF,untagged-PIF")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	vlans := make([]VLANInfo, 0)
	for _, b := range parseXEBlocks(out) {
		vlans = append(vlans, VLANInfo{UUID: b["uuid"], Tag: atoiDefault(b["tag"], 0), TaggedPIF: b["tagged-PIF"], UntaggedPIF: b["untagged-PIF"]})
	}
	return vlans, nil
}

// VLANCreate creates a VLAN with a tag (1-4094) on a physical interface (pif)
// and the network that will carry it (create it with network.create first).
// This can interrupt traffic on the interface. The result's uuid is in Data.uuid.
func (m *Module) VLANCreate(ctx context.Context, pifUUID, networkUUID string, tag int) (*ActionResult, error) {
	if pifUUID == "" || networkUUID == "" {
		return nil, fmt.Errorf("pif_uuid and network_uuid are required")
	}
	if tag < 1 || tag > 4094 {
		return nil, fmt.Errorf("tag must be between 1 and 4094")
	}
	out, err := m.run(ctx, "vlan-create", "pif-uuid="+pifUUID, "network-uuid="+networkUUID, "vlan="+strconv.Itoa(tag))
	if err != nil {
		return m.failure("vlan-create", pifUUID, err), nil
	}
	// xe prints the new VLAN interface's uuid, not the VLAN object's; look the
	// VLAN up so that data.uuid is what vlan.destroy and vlan.param_list expect.
	pifID := strings.TrimSpace(out)
	vlanID := pifID
	if lo, lerr := m.run(ctx, "vlan-list", "untagged-PIF="+pifID, "params=uuid", "--minimal"); lerr == nil {
		if ids := splitMinimalList(lo); len(ids) > 0 {
			vlanID = ids[0]
		}
	}
	return &ActionResult{Operation: "vlan-create", Target: pifUUID, Success: true, Message: "VLAN created.", Output: vlanID,
		Data: map[string]any{"uuid": vlanID, "tag": tag, "pif_uuid": pifID}}, nil
}

// VLANDestroy deletes a VLAN. Irreversible; confirm must be true.
func (m *Module) VLANDestroy(ctx context.Context, uuid string, confirm bool) (*ActionResult, error) {
	if err := m.requireUUID("vlan", uuid); err != nil {
		return nil, err
	}
	if !confirm {
		return nil, fmt.Errorf("destroying VLAN %s may interrupt traffic and is irreversible: pass confirm=true", uuid)
	}
	if _, err := m.run(ctx, "vlan-destroy", "uuid="+uuid); err != nil {
		return m.failure("vlan-destroy", uuid, err), nil
	}
	return &ActionResult{Operation: "vlan-destroy", Target: uuid, Success: true, Message: "VLAN destroyed."}, nil
}

// ---- PIF -------------------------------------------------------------------

// PIFList lists network interfaces, optionally filtered. vlan < -1 means "do
// not filter" (-1 selects untagged interfaces); empty strings do not filter.
func (m *Module) PIFList(ctx context.Context, vlan *int, networkUUID, hostUUID string) ([]PIFInfo, error) {
	args := []string{"pif-list"}
	if vlan != nil {
		args = append(args, "VLAN="+strconv.Itoa(*vlan))
	}
	if networkUUID != "" {
		args = append(args, "network-uuid="+networkUUID)
	}
	if hostUUID != "" {
		args = append(args, "host-uuid="+hostUUID)
	}
	args = append(args, "params=uuid,device,VLAN,network-uuid,host-uuid,MAC,currently-attached,IP,netmask,gateway,management,physical,MTU,bond-slave-of,bond-master-of")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	pifs := make([]PIFInfo, 0)
	for _, b := range parseXEBlocks(out) {
		p := PIFInfo{UUID: b["uuid"], Device: b["device"], VLAN: atoiDefault(b["VLAN"], -1), NetworkUUID: b["network-uuid"],
			HostUUID: b["host-uuid"], MAC: b["MAC"], Attached: b["currently-attached"] == "true", IP: b["IP"],
			Netmask: b["netmask"], Gateway: b["gateway"], Management: b["management"] == "true",
			Physical: b["physical"] == "true", MTU: atoiDefault(b["MTU"], 0)}
		if s := b["bond-slave-of"]; s != "" && s != "<not in database>" {
			p.BondSlaveOf = s
		}
		p.BondMaster = splitSetField(b["bond-master-of"])
		pifs = append(pifs, p)
	}
	return pifs, nil
}

// PIFUnplug takes an interface down. Refused for the management interface
// (that would cut the agent off) and requires confirm=true.
func (m *Module) PIFUnplug(ctx context.Context, uuid string, confirm bool) (*ActionResult, error) {
	if err := m.requireUUID("pif", uuid); err != nil {
		return nil, err
	}
	if !confirm {
		return nil, fmt.Errorf("unplugging interface %s drops its traffic: pass confirm=true", uuid)
	}
	mg, err := m.run(ctx, "pif-param-get", "uuid="+uuid, "param-name=management")
	if err != nil {
		return nil, fmt.Errorf("reading interface %s: %w", uuid, err)
	}
	if strings.TrimSpace(mg) == "true" {
		return nil, fmt.Errorf("refusing to unplug management interface %s: the agent would lose its connection", uuid)
	}
	if _, err := m.run(ctx, "pif-unplug", "uuid="+uuid); err != nil {
		return m.failure("pif-unplug", uuid, err), nil
	}
	return &ActionResult{Operation: "pif-unplug", Target: uuid, Success: true, Message: "Interface unplugged."}, nil
}

// ---- PBD / tasks / SR ------------------------------------------------------

// PBDList lists SR attachments, optionally for one SR and/or host.
func (m *Module) PBDList(ctx context.Context, srUUID, hostUUID string) ([]PBDInfo, error) {
	args := []string{"pbd-list"}
	if srUUID != "" {
		args = append(args, "sr-uuid="+srUUID)
	}
	if hostUUID != "" {
		args = append(args, "host-uuid="+hostUUID)
	}
	args = append(args, "params=uuid,sr-uuid,host-uuid,currently-attached,device-config")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	pbds := make([]PBDInfo, 0)
	for _, b := range parseXEBlocks(out) {
		cfg := parseMapValue(b["device-config"])
		for k := range cfg {
			if secretKey(k) {
				cfg[k] = "***"
			}
		}
		pbds = append(pbds, PBDInfo{UUID: b["uuid"], SRUUID: b["sr-uuid"], HostUUID: b["host-uuid"],
			Attached: b["currently-attached"] == "true", DeviceConfig: cfg})
	}
	return pbds, nil
}

func secretKey(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "passwd")
}

// TaskList lists xapi tasks.
func (m *Module) TaskList(ctx context.Context) ([]TaskInfo, error) {
	out, err := m.run(ctx, "task-list", "params=uuid,name-label,status,progress,created,finished,result,error-info")
	if err != nil {
		return nil, err
	}
	tasks := make([]TaskInfo, 0)
	for _, b := range parseXEBlocks(out) {
		pr, _ := strconv.ParseFloat(strings.TrimSpace(b["progress"]), 64)
		tasks = append(tasks, TaskInfo{UUID: b["uuid"], NameLabel: b["name-label"], Status: b["status"], Progress: pr,
			Created: b["created"], Finished: b["finished"], Result: b["result"], ErrorInfo: b["error-info"]})
	}
	return tasks, nil
}

// SRScan rescans an SR so new or removed disks appear. Can take long on large
// SRs; run it async.
func (m *Module) SRScan(ctx context.Context, uuid, name string) (*ActionResult, error) {
	resolved, err := m.resolveSR(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	if _, err := m.runTimeout(ctx, timeoutLong, "sr-scan", "uuid="+resolved); err != nil {
		return m.failure("sr-scan", resolved, err), nil
	}
	return &ActionResult{Operation: "sr-scan", Target: resolved, Success: true, Message: "SR rescanned."}, nil
}

// ResolveVIF is resolveVIF for callers outside the package.
func (m *Module) ResolveVIF(ctx context.Context, vifUUID, vmUUID, vmName, device string) (string, error) {
	return m.resolveVIF(ctx, vifUUID, vmUUID, vmName, device)
}

// ResolveSR is resolveSR for callers outside the package.
func (m *Module) ResolveSR(ctx context.Context, uuid, name string) (string, error) {
	return m.resolveSR(ctx, uuid, name)
}

// vifOwner returns the name and power state of the VM a VIF belongs to.
func (m *Module) vifOwner(ctx context.Context, vif string) (name, state string) {
	vm, err := m.run(ctx, "vif-param-get", "uuid="+vif, "param-name=vm-uuid")
	if err != nil {
		return "", ""
	}
	return m.vmNameAndState(ctx, strings.TrimSpace(vm))
}

func (m *Module) vifAction(ctx context.Context, op, verb, done, vifUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	vif, err := m.resolveVIF(ctx, vifUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	if _, err := m.runTimeout(ctx, timeoutMedium, op, "uuid="+vif); err != nil {
		r := m.failure(op, vif, err)
		var xe *XEError
		if asXE(err, &xe) {
			name, state := m.vifOwner(ctx, vif)
			r.Message = friendlyDeviceError(verb, name, state, xe)
		}
		return r, nil
	}
	return &ActionResult{Operation: op, Target: vif, Success: true, Message: done}, nil
}

// VIFPlug hot-attaches a network card to a running VM.
func (m *Module) VIFPlug(ctx context.Context, vifUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	return m.vifAction(ctx, "vif-plug", "plug network card", "Network card plugged into the running VM.", vifUUID, vmUUID, vmName, device)
}

// VIFUnplug hot-detaches a network card from a running VM.
func (m *Module) VIFUnplug(ctx context.Context, vifUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	return m.vifAction(ctx, "vif-unplug", "unplug network card", "Network card unplugged from the running VM.", vifUUID, vmUUID, vmName, device)
}
