package xenserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CDInfo describes one CD/DVD drive of a VM.
type CDInfo struct {
	VBDUUID  string `json:"vbd_uuid"`
	Position string `json:"position"`
	Empty    bool   `json:"empty"`
	ISOName  string `json:"iso_name,omitempty"`
	ISOUUID  string `json:"iso_uuid,omitempty"`
	Attached bool   `json:"currently_attached"`
	Mode     string `json:"mode"`
}

// VDICreate creates a virtual disk of sizeBytes on an SR. diskType defaults
// to "user". The new disk's uuid is in Data.uuid.
func (m *Module) VDICreate(ctx context.Context, srUUID, name, description string, sizeBytes int64, diskType string, sharable, readOnly bool) (*ActionResult, error) {
	if srUUID == "" || strings.TrimSpace(name) == "" || sizeBytes <= 0 {
		return nil, fmt.Errorf("sr_uuid, new_name and size_bytes (> 0) are required")
	}
	if diskType == "" {
		diskType = "user"
	}
	args := []string{"vdi-create", "sr-uuid=" + srUUID, "name-label=" + name,
		"type=" + diskType, "virtual-size=" + strconv.FormatInt(sizeBytes, 10)}
	if description != "" {
		args = append(args, "name-description="+description)
	}
	if sharable {
		args = append(args, "sharable=true")
	}
	if readOnly {
		args = append(args, "read-only=true")
	}
	out, err := m.runTimeout(ctx, timeoutMedium, args...)
	if err != nil {
		return m.failure("vdi-create", name, err), nil
	}
	id := strings.TrimSpace(out)
	return &ActionResult{Operation: "vdi-create", Target: name, Success: true, Message: "Disk created.", Output: id,
		Data: map[string]any{"uuid": id, "size_bytes": sizeBytes, "sr_uuid": srUUID}}, nil
}

// VDIDestroy permanently deletes a disk and its data. Irreversible, so
// confirm must be true. A disk still attached to a VM is refused unless
// allowAttached is set (xapi itself would delete it from under a halted VM).
func (m *Module) VDIDestroy(ctx context.Context, vdiUUID string, confirm, allowAttached bool) (*ActionResult, error) {
	if vdiUUID == "" {
		return nil, fmt.Errorf("vdi_uuid is required")
	}
	if !confirm {
		return nil, fmt.Errorf("destroying disk %s deletes its data and is irreversible: pass confirm=true", vdiUUID)
	}
	if !allowAttached {
		if out, err := m.run(ctx, "vdi-param-get", "uuid="+vdiUUID, "param-name=vbd-uuids"); err == nil {
			if vbds := splitSetField(strings.TrimSpace(out)); len(vbds) > 0 {
				return nil, fmt.Errorf("disk %s is still attached to a VM through %d device(s) (%s); detach it first with xenserver.vbd.destroy, or pass allow_attached=true to destroy it anyway", vdiUUID, len(vbds), strings.Join(vbds, ", "))
			}
		}
	}
	label := vdiUUID
	if out, err := m.run(ctx, "vdi-list", "uuid="+vdiUUID, "params=name-label"); err == nil {
		if b := parseXEBlocks(out); len(b) > 0 && b[0]["name-label"] != "" {
			label = fmt.Sprintf("%s (%s)", b[0]["name-label"], vdiUUID)
		}
	}
	if _, err := m.runTimeout(ctx, timeoutMedium, "vdi-destroy", "uuid="+vdiUUID); err != nil {
		r := m.failure("vdi-destroy", vdiUUID, err)
		var xe *XEError
		if asXE(err, &xe) {
			r.Message = friendlyDeviceError("destroy disk", label, "", xe)
		}
		return r, nil
	}
	return &ActionResult{Operation: "vdi-destroy", Target: vdiUUID, Success: true,
		Message: fmt.Sprintf("Disk %s destroyed.", label)}, nil
}

// VDIParamList returns every parameter of a disk, structured.
func (m *Module) VDIParamList(ctx context.Context, vdiUUID string) (*ParamList, error) {
	if vdiUUID == "" {
		return nil, fmt.Errorf("vdi_uuid is required")
	}
	return m.paramList(ctx, "vdi", vdiUUID)
}

// resolveVBD returns vbdUUID unchanged, or finds the VBD at disk position
// device ("0" = first) of a VM.
func (m *Module) resolveVBD(ctx context.Context, vbdUUID, vmUUID, vmName, device string) (string, error) {
	if vbdUUID != "" {
		return vbdUUID, nil
	}
	vm, err := m.resolveVM(ctx, vmUUID, vmName)
	if err != nil {
		return "", fmt.Errorf("vbd_uuid, or VM uuid/name plus device, is required: %w", err)
	}
	if device == "" {
		return "", fmt.Errorf("vbd_uuid, or VM uuid/name plus device, is required")
	}
	out, err := m.run(ctx, "vbd-list", "vm-uuid="+vm, "userdevice="+device, "--minimal")
	if err != nil {
		return "", fmt.Errorf("finding device %s of VM %s: %w", device, vm, err)
	}
	return pickUUID(out, fmt.Sprintf("device %s of VM %s", device, vm))
}

// vbdOwner returns the VM name-label and power state owning a VBD (best effort).
func (m *Module) vbdOwner(ctx context.Context, vbd string) (name, state string) {
	vm, err := m.run(ctx, "vbd-param-get", "uuid="+vbd, "param-name=vm-uuid")
	if err != nil {
		return "", ""
	}
	return m.vmNameAndState(ctx, strings.TrimSpace(vm))
}

// VBDCreate attaches an existing disk (or, with diskType CD, a drive) to a VM
// at a position. device is the position ("0", "1", ... or "autodetect"; empty
// means autodetect). The VBD is created unplugged; for a running VM call
// vbd.plug afterwards. The new VBD's uuid is in Data.uuid.
func (m *Module) VBDCreate(ctx context.Context, vmUUID, vmName, vdiUUID, device, mode, vbdType string, bootable bool) (*ActionResult, error) {
	vm, err := m.resolveVM(ctx, vmUUID, vmName)
	if err != nil {
		return nil, err
	}
	if device == "" {
		device = "autodetect"
	}
	if mode == "" {
		mode = "RW"
	}
	if vbdType == "" {
		vbdType = "Disk"
	}
	if mode != "RW" && mode != "RO" {
		return nil, fmt.Errorf("mode must be RW or RO")
	}
	if vbdType != "Disk" && vbdType != "CD" {
		return nil, fmt.Errorf("type must be Disk or CD")
	}
	if vdiUUID == "" && vbdType == "Disk" {
		return nil, fmt.Errorf("vdi_uuid is required for a Disk")
	}
	args := []string{"vbd-create", "vm-uuid=" + vm, "device=" + device, "mode=" + mode,
		"type=" + vbdType, "bootable=" + strconv.FormatBool(bootable)}
	if vdiUUID != "" {
		args = append(args, "vdi-uuid="+vdiUUID)
	}
	out, err := m.run(ctx, args...)
	if err != nil {
		return m.failureForVM(ctx, "vbd-create", "attach disk to", vm, err), nil
	}
	id := strings.TrimSpace(out)
	return &ActionResult{Operation: "vbd-create", Target: vm, Success: true, Message: "Disk attached (not plugged yet).", Output: id,
		Data: map[string]any{"uuid": id, "vm_uuid": vm, "position": device}}, nil
}

func (m *Module) vbdAction(ctx context.Context, op, verb, vbdUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	vbd, err := m.resolveVBD(ctx, vbdUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	timeout := timeoutMedium
	if _, err := m.runTimeout(ctx, timeout, op, "uuid="+vbd); err != nil {
		r := m.failure(op, vbd, err)
		var xe *XEError
		if asXE(err, &xe) {
			name, state := m.vbdOwner(ctx, vbd)
			r.Message = friendlyDeviceError(verb, name, state, xe)
		}
		return r, nil
	}
	done := map[string]string{
		"plug disk":        "Disk plugged into the running VM.",
		"unplug disk":      "Disk unplugged from the running VM.",
		"detach disk from": "Disk detached from the VM; the disk and its data stay on the storage repository.",
	}[verb]
	if done == "" {
		done = "Done: " + verb + "."
	}
	return &ActionResult{Operation: op, Target: vbd, Success: true, Message: done}, nil
}

// VBDPlug hot-attaches a VBD to a running VM.
func (m *Module) VBDPlug(ctx context.Context, vbdUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	return m.vbdAction(ctx, "vbd-plug", "plug disk", vbdUUID, vmUUID, vmName, device)
}

// VBDUnplug hot-detaches a VBD from a running VM.
func (m *Module) VBDUnplug(ctx context.Context, vbdUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	return m.vbdAction(ctx, "vbd-unplug", "unplug disk", vbdUUID, vmUUID, vmName, device)
}

// VBDDestroy removes the attachment of a disk to a VM. The disk (VDI) and its
// data stay on the SR. The VBD must be unplugged (or the VM halted).
func (m *Module) VBDDestroy(ctx context.Context, vbdUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	return m.vbdAction(ctx, "vbd-destroy", "detach disk from", vbdUUID, vmUUID, vmName, device)
}

// VBDParamList returns every parameter of a VBD, structured.
func (m *Module) VBDParamList(ctx context.Context, vbdUUID, vmUUID, vmName, device string) (*ParamList, error) {
	vbd, err := m.resolveVBD(ctx, vbdUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	return m.paramList(ctx, "vbd", vbd)
}

// VMCDList lists a VM's CD/DVD drives and the ISO in each.
func (m *Module) VMCDList(ctx context.Context, uuid, name string) ([]CDInfo, error) {
	vm, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "vbd-list", "vm-uuid="+vm, "type=CD",
		"params=uuid,userdevice,empty,vdi-uuid,vdi-name-label,currently-attached,mode")
	if err != nil {
		return nil, err
	}
	cds := make([]CDInfo, 0)
	for _, b := range parseXEBlocks(out) {
		c := CDInfo{VBDUUID: b["uuid"], Position: b["userdevice"], Empty: b["empty"] == "true",
			Attached: b["currently-attached"] == "true", Mode: b["mode"]}
		if !c.Empty {
			if v := b["vdi-uuid"]; v != "<not in database>" {
				c.ISOUUID = v
			}
			if n := b["vdi-name-label"]; n != "<not in database>" {
				c.ISOName = n
			}
		}
		cds = append(cds, c)
	}
	return cds, nil
}

// cdAction runs vm-cd-add/insert/eject/remove.
func (m *Module) cdAction(ctx context.Context, op, verb, done string, uuid, name string, extra ...string) (*ActionResult, error) {
	vm, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	args := append([]string{op, "uuid=" + vm}, extra...)
	if _, err := m.run(ctx, args...); err != nil {
		r := m.failureForVM(ctx, op, verb, vm, err)
		if op == "vm-cd-remove" && strings.Contains(strings.ToLower(err.Error()), "disk not found") {
			r.Message = fmt.Sprintf("No CD drive of VM %s currently holds that ISO (the drive is empty or holds another ISO). Remove an empty drive with xenserver.vbd.destroy using its device position (see xenserver.vm.cd_list).", vm)
		}
		return r, nil
	}
	return &ActionResult{Operation: op, Target: vm, Success: true, Message: done}, nil
}

// VMCDAdd adds a CD drive holding the named ISO (cdName is the ISO's name in
// an ISO SR) at a device position.
func (m *Module) VMCDAdd(ctx context.Context, uuid, name, cdName, device string) (*ActionResult, error) {
	if cdName == "" {
		return nil, fmt.Errorf("cd_name (the ISO's name in an ISO SR) is required")
	}
	if device == "" {
		device = "autodetect" // xe requires a device; let xapi pick the position
	}
	args := []string{"cd-name=" + cdName, "device=" + device}
	return m.cdAction(ctx, "vm-cd-add", "add CD to", "CD drive added with "+cdName+".", uuid, name, args...)
}

// VMCDInsert puts the named ISO into the VM's existing empty CD drive.
func (m *Module) VMCDInsert(ctx context.Context, uuid, name, cdName string) (*ActionResult, error) {
	if cdName == "" {
		return nil, fmt.Errorf("cd_name (the ISO's name in an ISO SR) is required")
	}
	return m.cdAction(ctx, "vm-cd-insert", "insert CD into", cdName+" inserted into the CD drive.", uuid, name, "cd-name="+cdName)
}

// VMCDEject empties the VM's CD drive (the drive stays).
func (m *Module) VMCDEject(ctx context.Context, uuid, name string) (*ActionResult, error) {
	return m.cdAction(ctx, "vm-cd-eject", "eject CD from", "CD ejected; the drive stays.", uuid, name)
}

// VMCDRemove removes the CD drive holding the named ISO from the VM.
func (m *Module) VMCDRemove(ctx context.Context, uuid, name, cdName string) (*ActionResult, error) {
	if cdName == "" {
		return nil, fmt.Errorf("cd_name (the ISO's name in an ISO SR) is required")
	}
	return m.cdAction(ctx, "vm-cd-remove", "remove CD from", "CD drive holding "+cdName+" removed.", uuid, name, "cd-name="+cdName)
}

// failureForVM builds a failed result whose message is friendly when the
// cause is the VM's power state.
func (m *Module) failureForVM(ctx context.Context, op, verb, vm string, err error) *ActionResult {
	r := m.failure(op, vm, err)
	var xe *XEError
	if asXE(err, &xe) {
		name, state := m.vmNameAndState(ctx, vm)
		if name == "" {
			name = vm
		}
		r.Message = friendlyDeviceError(verb, name, state, xe)
	}
	return r
}
