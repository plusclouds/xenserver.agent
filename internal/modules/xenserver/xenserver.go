package xenserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"github.com/plusclouds/ubuntu-agent/internal/jobs"
)

// Module manages a XenServer/XCP-ng host via the xe CLI and, for raw VDI
// export, IPMI sensor reads, XAPI event streaming, and RRD metrics, the
// local xapi HTTP interface / ipmitool.
type Module struct {
	exec           *executor.Executor
	xeBinary       string
	inventoryPath  string
	xapiSocketPath string
	ipmiBinary     string
	isoBinary      string
	srMountDir     string
	logger         *zap.Logger
}

// SetSRMountDir overrides where SR mounts live (default /run/sr-mount); for tests.
func (m *Module) SetSRMountDir(dir string) { m.srMountDir = dir }

// SetISOBinary sets the tool used by iso.create (genisoimage or mkisofs).
func (m *Module) SetISOBinary(path string) { m.isoBinary = path }

// New creates a xenserver Module.
func New(exec *executor.Executor, xeBinary, inventoryPath, xapiSocketPath, ipmiBinary string, logger *zap.Logger) *Module {
	return &Module{
		exec:           exec,
		xeBinary:       xeBinary,
		inventoryPath:  inventoryPath,
		xapiSocketPath: xapiSocketPath,
		ipmiBinary:     ipmiBinary,
		logger:         logger,
	}
}

// run invokes xe with the given arguments and folds stderr into the error,
// since xe puts its human-readable failure reason there.
// Per-operation xe timeouts. The executor's 30 s default is right for quick
// queries but kills real work: VM power operations can take minutes and
// exports/imports of multi-GB images take much longer.
const (
	timeoutMedium = 10 * time.Minute // power ops, snapshot create, SR create
	timeoutLong   = 2 * time.Hour    // vm export/import, snapshot export, vdi copy
)

func (m *Module) run(ctx context.Context, args ...string) (string, error) {
	return m.runTimeout(ctx, 0, args...)
}

// runTimeout is run with an explicit timeout (0 = the executor default).
func (m *Module) runTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	var stdout, stderr string
	var err error
	if timeout > 0 {
		stdout, stderr, err = m.exec.ExecuteTimeout(ctx, timeout, m.xeBinary, args...)
	} else {
		stdout, stderr, err = m.exec.Execute(ctx, m.xeBinary, args...)
	}
	if err != nil {
		return stdout, newXEError(m.xeBinary, args, stdout, stderr, err)
	}
	return stdout, nil
}

// localHostUUID resolves this host's own uuid from INSTALLATION_UUID in the
// xensource-inventory file — the same file XenServer/XCP-ng's own init
// tooling uses to identify the local host record.
func (m *Module) localHostUUID() (string, error) {
	data, err := os.ReadFile(m.inventoryPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", m.inventoryPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "INSTALLATION_UUID=") {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, "INSTALLATION_UUID="), `'"`), nil
	}
	return "", fmt.Errorf("INSTALLATION_UUID not found in %s", m.inventoryPath)
}

// resolveVM returns uuid unchanged if given; otherwise resolves name (a
// VM name-label) to exactly one uuid, erroring on zero or multiple matches.
func (m *Module) resolveVM(ctx context.Context, uuid, name string) (string, error) {
	if uuid != "" {
		return uuid, nil
	}
	if name == "" {
		return "", fmt.Errorf("uuid or name is required")
	}
	out, err := m.run(ctx, "vm-list", "name-label="+name, "--minimal")
	if err != nil {
		return "", fmt.Errorf("resolving VM name-label %q: %w", name, err)
	}
	return pickUUID(out, fmt.Sprintf("name-label %q", name))
}

// resolveSR returns uuid unchanged if given; otherwise resolves name (an SR
// name-label) to exactly one uuid, erroring on zero or multiple matches.
func (m *Module) resolveSR(ctx context.Context, uuid, name string) (string, error) {
	if uuid != "" {
		return uuid, nil
	}
	if name == "" {
		return "", fmt.Errorf("uuid or name is required")
	}
	out, err := m.run(ctx, "sr-list", "name-label="+name, "--minimal")
	if err != nil {
		return "", fmt.Errorf("resolving SR name-label %q: %w", name, err)
	}
	return pickUUID(out, fmt.Sprintf("name-label %q", name))
}

// resolveVIF returns vifUUID unchanged if given; otherwise resolves the VM
// (uuid/name) plus device index to exactly one VIF uuid.
func (m *Module) resolveVIF(ctx context.Context, vifUUID, vmUUID, vmName, device string) (string, error) {
	if vifUUID != "" {
		return vifUUID, nil
	}
	resolvedVM, err := m.resolveVM(ctx, vmUUID, vmName)
	if err != nil {
		return "", err
	}
	if device == "" {
		return "", fmt.Errorf("vif_uuid, or vm uuid/name plus device, is required")
	}
	out, err := m.run(ctx, "vif-list", "vm-uuid="+resolvedVM, "device="+device, "--minimal")
	if err != nil {
		return "", fmt.Errorf("resolving VIF for VM %s device %s: %w", resolvedVM, device, err)
	}
	return pickUUID(out, fmt.Sprintf("vm=%s device=%s", resolvedVM, device))
}

// --- read-only -------------------------------------------------------------

// PoolInfo returns this host's pool record.
func (m *Module) PoolInfo(ctx context.Context) (*PoolInfo, error) {
	out, err := m.run(ctx, "pool-list", "params=uuid,name-label,master,ha-enabled")
	if err != nil {
		return nil, fmt.Errorf("listing pool: %w", err)
	}
	blocks := parseXEBlocks(out)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no pool record returned")
	}
	b := blocks[0]
	count := 0
	if hosts, herr := m.run(ctx, "host-list", "--minimal"); herr == nil {
		count = len(splitMinimalList(hosts))
	}
	return &PoolInfo{
		UUID:         b["uuid"],
		NameLabel:    b["name-label"],
		Master:       b["master"],
		HAEnabled:    b["ha-enabled"] == "true",
		HostCount:    count,
		IsPoolMember: count > 1,
	}, nil
}

// HostList returns every host in the pool.
func (m *Module) HostList(ctx context.Context) ([]HostSummary, error) {
	out, err := m.run(ctx, "host-list", "params=uuid,name-label,hostname,address,enabled")
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}
	blocks := parseXEBlocks(out)
	hosts := make([]HostSummary, 0, len(blocks))
	for _, b := range blocks {
		hosts = append(hosts, HostSummary{
			UUID:      b["uuid"],
			NameLabel: b["name-label"],
			Hostname:  b["hostname"],
			Address:   b["address"],
			Enabled:   b["enabled"] == "true",
		})
	}
	return hosts, nil
}

// HostInfo returns the detailed record for this host.
func (m *Module) HostInfo(ctx context.Context) (*HostInfo, error) {
	uuid, err := m.localHostUUID()
	if err != nil {
		return nil, fmt.Errorf("determining local host uuid: %w", err)
	}
	out, err := m.run(ctx, "host-param-list", "uuid="+uuid)
	if err != nil {
		return nil, fmt.Errorf("getting host params: %w", err)
	}
	blocks := parseXEBlocks(out)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no host record returned for uuid %s", uuid)
	}
	b := blocks[0]
	return &HostInfo{
		UUID:            b["uuid"],
		NameLabel:       b["name-label"],
		Hostname:        b["hostname"],
		Address:         b["address"],
		Enabled:         b["enabled"] == "true",
		SoftwareVersion: b["software-version"],
	}, nil
}

// VMList returns every guest VM on the pool, excluding control domains and
// templates (not customer-facing VMs).
func (m *Module) VMList(ctx context.Context) ([]VMInfo, error) {
	out, err := m.run(ctx, "vm-list",
		"params=uuid,name-label,power-state,resident-on,VCPUs-number,memory-actual,is-control-domain,is-a-template")
	if err != nil {
		return nil, fmt.Errorf("listing VMs: %w", err)
	}
	blocks := parseXEBlocks(out)
	vms := make([]VMInfo, 0, len(blocks))
	for _, b := range blocks {
		if b["is-control-domain"] == "true" || b["is-a-template"] == "true" {
			continue
		}
		vcpus, _ := strconv.Atoi(b["VCPUs-number"])
		mem, _ := strconv.ParseUint(b["memory-actual"], 10, 64)
		vms = append(vms, VMInfo{
			UUID:        b["uuid"],
			NameLabel:   b["name-label"],
			PowerState:  b["power-state"],
			ResidentOn:  b["resident-on"],
			VCPUs:       vcpus,
			MemoryBytes: mem,
		})
	}
	return vms, nil
}

// VMGet returns the detailed record for one VM.
func (m *Module) VMGet(ctx context.Context, uuid, name string) (*VMDetail, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "vm-param-list", "uuid="+resolved)
	if err != nil {
		return nil, fmt.Errorf("getting VM params: %w", err)
	}
	blocks := parseXEBlocks(out)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no VM record returned for uuid %s", resolved)
	}
	b := blocks[0]
	vcpus, _ := strconv.Atoi(b["VCPUs-number"])
	mem, _ := strconv.ParseUint(b["memory-actual"], 10, 64)
	return &VMDetail{
		UUID:        b["uuid"],
		NameLabel:   b["name-label"],
		Description: b["name-description"],
		PowerState:  b["power-state"],
		ResidentOn:  b["resident-on"],
		VCPUs:       vcpus,
		MemoryBytes: mem,
		OSVersion:   b["os-version"],
		IsATemplate: b["is-a-template"] == "true",
	}, nil
}

// SnapshotList returns every snapshot of the given VM.
func (m *Module) SnapshotList(ctx context.Context, uuid, name string) ([]SnapshotInfo, error) {
	vmUUID, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "snapshot-list", "snapshot-of="+vmUUID,
		"params=uuid,name-label,snapshot-of,snapshot-time")
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	blocks := parseXEBlocks(out)
	snaps := make([]SnapshotInfo, 0, len(blocks))
	for _, b := range blocks {
		snaps = append(snaps, SnapshotInfo{
			UUID:         b["uuid"],
			NameLabel:    b["name-label"],
			SnapshotOf:   b["snapshot-of"],
			SnapshotTime: b["snapshot-time"],
		})
	}
	return snaps, nil
}

// SRList returns every storage repository visible to the pool.
func (m *Module) SRList(ctx context.Context) ([]SRInfo, error) {
	out, err := m.run(ctx, "sr-list",
		"params=uuid,name-label,type,physical-size,physical-utilisation")
	if err != nil {
		return nil, fmt.Errorf("listing SRs: %w", err)
	}
	blocks := parseXEBlocks(out)
	srs := make([]SRInfo, 0, len(blocks))
	for _, b := range blocks {
		srs = append(srs, srInfoFromBlock(b))
	}
	return srs, nil
}

// SRGet returns the detailed record for one SR.
func (m *Module) SRGet(ctx context.Context, uuid, name string) (*SRInfo, error) {
	resolved, err := m.resolveSR(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "sr-param-list", "uuid="+resolved)
	if err != nil {
		return nil, fmt.Errorf("getting SR params: %w", err)
	}
	blocks := parseXEBlocks(out)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("no SR record returned for uuid %s", resolved)
	}
	info := srInfoFromBlock(blocks[0])
	return &info, nil
}

func srInfoFromBlock(b map[string]string) SRInfo {
	size, _ := strconv.ParseUint(b["physical-size"], 10, 64)
	util, _ := strconv.ParseUint(b["physical-utilisation"], 10, 64)
	return SRInfo{
		UUID:              b["uuid"],
		NameLabel:         b["name-label"],
		Type:              b["type"],
		PhysicalSizeBytes: size,
		PhysicalUtilBytes: util,
	}
}

// NetworkList returns every xapi network (OVS bridge) in the pool.
func (m *Module) NetworkList(ctx context.Context) ([]NetworkInfo, error) {
	out, err := m.run(ctx, "network-list", "params=uuid,name-label,bridge")
	if err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}
	blocks := parseXEBlocks(out)
	nets := make([]NetworkInfo, 0, len(blocks))
	for _, b := range blocks {
		nets = append(nets, NetworkInfo{UUID: b["uuid"], NameLabel: b["name-label"], Bridge: b["bridge"]})
	}
	return nets, nil
}

// NetworkBridge resolves a xapi network uuid to its underlying OVS bridge
// name. Used by internal/modules/openvswitch to support the hybrid
// xapi-network-backed VXLAN pattern.
func (m *Module) NetworkBridge(ctx context.Context, networkUUID string) (string, error) {
	out, err := m.run(ctx, "network-param-get", "uuid="+networkUUID, "param-name=bridge")
	if err != nil {
		return "", fmt.Errorf("resolving bridge for network %s: %w", networkUUID, err)
	}
	return strings.TrimSpace(out), nil
}

// VIFList returns every virtual network interface attached to a VM.
func (m *Module) VIFList(ctx context.Context, vmUUID, vmName string) ([]VIFInfo, error) {
	resolved, err := m.resolveVM(ctx, vmUUID, vmName)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "vif-list", "vm-uuid="+resolved,
		"params=uuid,device,MAC,network-uuid,locking-mode,ipv4-allowed,ipv6-allowed")
	if err != nil {
		return nil, fmt.Errorf("listing VIFs: %w", err)
	}
	blocks := parseXEBlocks(out)
	vifs := make([]VIFInfo, 0, len(blocks))
	for _, b := range blocks {
		vifs = append(vifs, VIFInfo{
			UUID:        b["uuid"],
			Device:      b["device"],
			MAC:         b["MAC"],
			NetworkUUID: b["network-uuid"],
			LockingMode: b["locking-mode"],
			IPv4Allowed: splitSetField(b["ipv4-allowed"]),
			IPv6Allowed: splitSetField(b["ipv6-allowed"]),
		})
	}
	return vifs, nil
}

// VMDiskList returns the VM's virtual block devices ordered by position,
// each joined with its VDI's name, size and SR. Position "0" is the first disk.
func (m *Module) VMDiskList(ctx context.Context, vmUUID, vmName string) ([]VMDiskInfo, error) {
	resolved, err := m.resolveVM(ctx, vmUUID, vmName)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, "vbd-list", "vm-uuid="+resolved,
		"params=uuid,userdevice,type,mode,bootable,currently-attached,vdi-uuid")
	if err != nil {
		return nil, fmt.Errorf("listing VBDs: %w", err)
	}
	disks := make([]VMDiskInfo, 0)
	for _, b := range parseXEBlocks(out) {
		d := vbdToDisk(b)
		if d.VDIUUID != "" {
			vout, err := m.run(ctx, "vdi-list", "uuid="+d.VDIUUID, "params=name-label,virtual-size,sr-uuid")
			if err != nil {
				return nil, fmt.Errorf("reading VDI %s: %w", d.VDIUUID, err)
			}
			if vb := parseXEBlocks(vout); len(vb) > 0 {
				d.NameLabel = vb[0]["name-label"]
				d.VirtualSizeBytes, _ = strconv.ParseUint(vb[0]["virtual-size"], 10, 64)
				d.SRUUID = vb[0]["sr-uuid"]
			}
		}
		disks = append(disks, d)
	}
	sort.SliceStable(disks, func(i, j int) bool {
		a, aerr := strconv.Atoi(disks[i].Position)
		b, berr := strconv.Atoi(disks[j].Position)
		if aerr == nil && berr == nil {
			return a < b
		}
		return disks[i].Position < disks[j].Position
	})
	return disks, nil
}

// vbdToDisk converts one parsed vbd-list record; the VDI fields are filled in
// by the caller. An empty CD drive reports vdi-uuid "<not in database>".
func vbdToDisk(b map[string]string) VMDiskInfo {
	vdi := b["vdi-uuid"]
	if vdi == "<not in database>" {
		vdi = ""
	}
	return VMDiskInfo{
		Position:          b["userdevice"],
		VBDUUID:           b["uuid"],
		Type:              b["type"],
		Mode:              b["mode"],
		Bootable:          b["bootable"] == "true",
		CurrentlyAttached: b["currently-attached"] == "true",
		VDIUUID:           vdi,
	}
}

// VDIList returns virtual disk images, optionally filtered to one SR.
func (m *Module) VDIList(ctx context.Context, srUUID string) ([]VDIInfo, error) {
	args := []string{"vdi-list"}
	if srUUID != "" {
		args = append(args, "sr-uuid="+srUUID)
	}
	args = append(args, "params=uuid,name-label,virtual-size,physical-utilisation,sr-uuid,type")
	out, err := m.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing VDIs: %w", err)
	}
	blocks := parseXEBlocks(out)
	vdis := make([]VDIInfo, 0, len(blocks))
	for _, b := range blocks {
		vsize, _ := strconv.ParseUint(b["virtual-size"], 10, 64)
		putil, _ := strconv.ParseUint(b["physical-utilisation"], 10, 64)
		vdis = append(vdis, VDIInfo{
			UUID:              b["uuid"],
			NameLabel:         b["name-label"],
			VirtualSizeBytes:  vsize,
			PhysicalUtilBytes: putil,
			SRUUID:            b["sr-uuid"],
			Type:              b["type"],
		})
	}
	return vdis, nil
}

// --- state-changing ---------------------------------------------------------

// vmAction resolves the VM and runs the given single-uuid xe vm-* action.
// force adds force=true (hard shutdown/reboot) for the ops that support it.
func (m *Module) vmAction(ctx context.Context, op, uuid, name string, force bool) (*ActionResult, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	args := []string{op, "uuid=" + resolved}
	if force && (op == "vm-shutdown" || op == "vm-reboot") {
		args = append(args, "force=true")
	}
	stdout, err := m.runTimeout(ctx, timeoutMedium, args...)
	if err != nil {
		r := m.failure(op, resolved, err)
		var xe *XEError
		if errors.As(err, &xe) {
			vmName, state := m.vmNameAndState(ctx, resolved)
			if vmName == "" {
				vmName = resolved
			}
			r.Message = friendlyVMPowerError(op, vmName, state, force, xe)
		}
		if op == "vm-start" {
			r.Diagnostics = m.diagnoseVMStart(ctx, resolved)
		}
		return r, nil
	}
	return &ActionResult{Operation: op, Target: resolved, Success: true,
		Message: fmt.Sprintf("%s completed successfully.", op), Output: strings.TrimSpace(stdout)}, nil
}

// vmNameAndState returns the VM's name-label and power-state (best effort).
func (m *Module) vmNameAndState(ctx context.Context, vm string) (string, string) {
	out, err := m.run(ctx, "vm-list", "uuid="+vm, "params=name-label,power-state")
	if err != nil {
		return "", ""
	}
	if b := parseXEBlocks(out); len(b) > 0 {
		return b[0]["name-label"], b[0]["power-state"]
	}
	return "", ""
}

// VMStart starts a stopped VM.
func (m *Module) VMStart(ctx context.Context, uuid, name string) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-start", uuid, name, false)
}

// VMShutdown shuts a running VM down: a clean ACPI shutdown, or when force is
// true a hard power-off that does not need the guest's cooperation.
func (m *Module) VMShutdown(ctx context.Context, uuid, name string, force bool) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-shutdown", uuid, name, force)
}

// VMReboot reboots a running VM: clean by default, or hard when force is true.
func (m *Module) VMReboot(ctx context.Context, uuid, name string, force bool) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-reboot", uuid, name, force)
}

// VMSuspend suspends a running VM to disk.
func (m *Module) VMSuspend(ctx context.Context, uuid, name string) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-suspend", uuid, name, false)
}

// VMResume resumes a suspended VM.
func (m *Module) VMResume(ctx context.Context, uuid, name string) (*ActionResult, error) {
	return m.vmAction(ctx, "vm-resume", uuid, name, false)
}

// SnapshotCreate takes a new snapshot of a VM, returning the new snapshot's uuid in Output.
func (m *Module) SnapshotCreate(ctx context.Context, uuid, name, label string) (*ActionResult, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	if label == "" {
		return nil, fmt.Errorf("label is required")
	}
	stdout, err := m.runTimeout(ctx, timeoutMedium, "vm-snapshot", "uuid="+resolved, "new-name-label="+label)
	if err != nil {
		return m.failure("vm-snapshot", resolved, err), nil
	}
	return &ActionResult{Operation: "vm-snapshot", Target: resolved, Success: true,
		Message: "snapshot created successfully.", Output: strings.TrimSpace(stdout)}, nil
}

// SnapshotDelete destroys a snapshot. snapshotUUID is the snapshot's own
// uuid (snapshots are VM-class objects), not the parent VM's uuid.
func (m *Module) SnapshotDelete(ctx context.Context, snapshotUUID string) (*ActionResult, error) {
	if snapshotUUID == "" {
		return nil, fmt.Errorf("uuid is required")
	}
	// force=true avoids an interactive confirmation prompt that would
	// otherwise hang the non-interactive agent process.
	if _, err := m.run(ctx, "snapshot-uninstall", "uuid="+snapshotUUID, "force=true"); err != nil {
		return m.failure("snapshot-uninstall", snapshotUUID, err), nil
	}
	return &ActionResult{Operation: "snapshot-uninstall", Target: snapshotUUID, Success: true,
		Message: "snapshot deleted successfully."}, nil
}

// SRCreate introduces a new storage repository. hostUUID defaults to the
// local host when empty. deviceConfig entries become repeated
// "device-config:<k>=<v>" arguments.
func (m *Module) SRCreate(ctx context.Context, name, srType string, shared bool, contentType, hostUUID string, deviceConfig map[string]string) (*ActionResult, error) {
	if name == "" || srType == "" {
		return nil, fmt.Errorf("name and type are required")
	}
	if hostUUID == "" {
		var err error
		hostUUID, err = m.localHostUUID()
		if err != nil {
			return nil, fmt.Errorf("determining local host uuid: %w", err)
		}
	}
	args := []string{
		"sr-create",
		"name-label=" + name,
		"type=" + srType,
		"shared=" + strconv.FormatBool(shared),
		"host-uuid=" + hostUUID,
	}
	if contentType != "" {
		args = append(args, "content-type="+contentType)
	}
	cfgKeys := make([]string, 0, len(deviceConfig))
	for k := range deviceConfig {
		if !mapKeyRe.MatchString(k) {
			return nil, fmt.Errorf("device_config key %q must match [A-Za-z0-9_.-/]+", k)
		}
		cfgKeys = append(cfgKeys, k)
	}
	sort.Strings(cfgKeys) // reproducible argument order
	for _, k := range cfgKeys {
		args = append(args, "device-config:"+k+"="+deviceConfig[k])
	}
	stdout, err := m.runTimeout(ctx, timeoutMedium, args...)
	if err != nil {
		return m.failure("sr-create", name, err), nil
	}
	return &ActionResult{Operation: "sr-create", Target: name, Success: true,
		Message: "SR created successfully.", Output: strings.TrimSpace(stdout)}, nil
}

// SRRemove safely detaches an SR: unplugs every PBD across the pool, then
// forgets the SR record. Data is preserved — this never destroys VDIs.
func (m *Module) SRRemove(ctx context.Context, uuid, name string) (*ActionResult, error) {
	resolved, err := m.resolveSR(ctx, uuid, name)
	if err != nil {
		return nil, err
	}

	pbdOut, err := m.run(ctx, "pbd-list", "sr-uuid="+resolved, "--minimal")
	if err != nil {
		return nil, fmt.Errorf("listing PBDs for SR %s: %w", resolved, err)
	}
	for _, pbd := range splitMinimalList(pbdOut) {
		if _, err := m.run(ctx, "pbd-unplug", "uuid="+pbd); err != nil {
			m.logger.Warn("pbd-unplug failed, continuing SR removal",
				zap.String("pbd_uuid", pbd), zap.String("sr_uuid", resolved), zap.Error(err))
		}
	}

	if _, err := m.run(ctx, "sr-forget", "uuid="+resolved); err != nil {
		return m.failure("sr-forget", resolved, err), nil
	}
	return &ActionResult{Operation: "sr-forget", Target: resolved, Success: true,
		Message: "SR detached successfully (data preserved)."}, nil
}

// SRSetDefault makes an SR the pool's default ("primary") storage: it sets the
// pool's default-SR, suspend-image-SR and crash-dump-SR, which is what
// XenCenter's "Set as Default SR" does. The SR must already be attached.
func (m *Module) SRSetDefault(ctx context.Context, uuid, name string) (*ActionResult, error) {
	resolved, err := m.resolveSR(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	pool, err := m.PoolInfo(ctx)
	if err != nil {
		return nil, err
	}
	for _, param := range []string{"default-SR", "suspend-image-SR", "crash-dump-SR"} {
		if _, err := m.run(ctx, "pool-param-set", "uuid="+pool.UUID, param+"="+resolved); err != nil {
			r := m.failure("sr-set-default", resolved, err)
			r.Message = fmt.Sprintf("setting pool %s: %s", param, err)
			return r, nil
		}
	}
	return &ActionResult{Operation: "sr-set-default", Target: resolved, Success: true,
		Message: "SR set as pool default-SR, suspend-image-SR and crash-dump-SR."}, nil
}

// VIFSetACL sets xapi's native VIF anti-spoofing/allowlist ACL. The VIF may
// be identified directly (vifUUID) or via vmUUID/vmName plus device index.
func (m *Module) VIFSetACL(ctx context.Context, vifUUID, vmUUID, vmName, device, lockingMode string, ipv4Allowed, ipv6Allowed []string) (*ActionResult, error) {
	resolved, err := m.resolveVIF(ctx, vifUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	if lockingMode == "" {
		return nil, fmt.Errorf("locking_mode is required")
	}
	args := []string{"vif-param-set", "uuid=" + resolved, "locking-mode=" + lockingMode}
	if len(ipv4Allowed) > 0 {
		args = append(args, "ipv4-allowed="+strings.Join(ipv4Allowed, ","))
	}
	if len(ipv6Allowed) > 0 {
		args = append(args, "ipv6-allowed="+strings.Join(ipv6Allowed, ","))
	}
	if _, err := m.run(ctx, args...); err != nil {
		return m.failure("vif-param-set", resolved, err), nil
	}
	return &ActionResult{Operation: "vif-param-set", Target: resolved, Success: true,
		Message: "VIF ACL updated successfully."}, nil
}

// VIFRemove deletes a VIF (virtual network card). The VIF is given directly
// (vifUUID) or resolved from a VM plus device index. The removed VIF's MAC and
// network are returned in Output so the change can be recorded or undone.
// xapi requires the VM to be halted or the VIF unplugged.
func (m *Module) VIFRemove(ctx context.Context, vifUUID, vmUUID, vmName, device string) (*ActionResult, error) {
	resolved, err := m.resolveVIF(ctx, vifUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	var what string
	if out, lerr := m.run(ctx, "vif-list", "uuid="+resolved, "params=device,MAC,network-uuid"); lerr == nil {
		if b := parseXEBlocks(out); len(b) > 0 {
			what = fmt.Sprintf("device=%s mac=%s network=%s", b[0]["device"], b[0]["MAC"], b[0]["network-uuid"])
		}
	}
	if _, err := m.run(ctx, "vif-destroy", "uuid="+resolved); err != nil {
		return m.failure("vif-destroy", resolved, err), nil
	}
	return &ActionResult{Operation: "vif-destroy", Target: resolved, Success: true,
		Message: "VIF removed.", Output: what}, nil
}

// resolveNetwork returns networkUUID unchanged if given; otherwise resolves a
// network name-label to exactly one uuid.
func (m *Module) resolveNetwork(ctx context.Context, networkUUID, networkName string) (string, error) {
	if networkUUID != "" {
		return networkUUID, nil
	}
	if networkName == "" {
		return "", fmt.Errorf("network_uuid or network_name is required")
	}
	out, err := m.run(ctx, "network-list", "name-label="+networkName, "--minimal")
	if err != nil {
		return "", fmt.Errorf("resolving network name-label %q: %w", networkName, err)
	}
	return pickUUID(out, fmt.Sprintf("network name-label %q", networkName))
}

// VIFSetNetwork moves a VIF to another network. xapi cannot re-point an
// existing VIF, so the VIF is recreated on the target network with the same
// VM, device index, MAC, MTU and locking mode. If the recreate fails, the
// original VIF is restored on its old network and the failure is reported.
// The VM must be halted (or the VIF unplugged). The new VIF's uuid is returned
// in Output.
func (m *Module) VIFSetNetwork(ctx context.Context, vifUUID, vmUUID, vmName, device, networkUUID, networkName string) (*ActionResult, error) {
	resolved, err := m.resolveVIF(ctx, vifUUID, vmUUID, vmName, device)
	if err != nil {
		return nil, err
	}
	target, err := m.resolveNetwork(ctx, networkUUID, networkName)
	if err != nil {
		return nil, err
	}
	if _, err := m.run(ctx, "network-list", "uuid="+target, "--minimal"); err != nil {
		return nil, fmt.Errorf("target network %s not found: %w", target, err)
	}

	out, err := m.run(ctx, "vif-list", "uuid="+resolved, "params=vm-uuid,device,MAC,MTU,network-uuid,locking-mode")
	if err != nil {
		return nil, fmt.Errorf("reading VIF %s: %w", resolved, err)
	}
	blocks := parseXEBlocks(out)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("VIF %s not found", resolved)
	}
	v := blocks[0]
	if v["network-uuid"] == target {
		return &ActionResult{Operation: "vif-set-network", Target: resolved, Success: true,
			Message: "VIF is already on that network; nothing changed.", Output: resolved}, nil
	}
	createArgs := func(network string) []string {
		a := []string{"vif-create", "vm-uuid=" + v["vm-uuid"], "network-uuid=" + network, "device=" + v["device"]}
		if v["MAC"] != "" {
			a = append(a, "mac="+v["MAC"])
		}
		if v["MTU"] != "" {
			a = append(a, "mtu="+v["MTU"])
		}
		return a
	}

	if _, err := m.run(ctx, "vif-destroy", "uuid="+resolved); err != nil {
		return m.failure("vif-destroy", resolved, err), nil
	}
	newOut, err := m.run(ctx, createArgs(target)...)
	if err != nil {
		r := m.failure("vif-create", resolved, err)
		if _, rerr := m.run(ctx, createArgs(v["network-uuid"])...); rerr != nil {
			r.Message += fmt.Sprintf(" — ROLLBACK ALSO FAILED, VIF device %s (mac %s) is now missing: %v", v["device"], v["MAC"], rerr)
		} else {
			r.Message += " — original VIF restored on its old network."
		}
		return r, nil
	}
	newUUID := strings.TrimSpace(newOut)
	if mode := v["locking-mode"]; mode != "" && mode != "network_default" {
		if _, err := m.run(ctx, "vif-param-set", "uuid="+newUUID, "locking-mode="+mode); err != nil {
			r := m.failure("vif-param-set", newUUID, err)
			r.Message = "VIF moved but restoring locking-mode failed: " + r.Message
			r.Output = newUUID
			return r, nil
		}
	}
	return &ActionResult{Operation: "vif-set-network", Target: resolved, Success: true,
		Message: fmt.Sprintf("VIF device %s (mac %s) moved from network %s to %s.", v["device"], v["MAC"], v["network-uuid"], target),
		Output:  newUUID}, nil
}

// PromoteTemplate turns a template into an ordinary VM in place (the template
// stops being a template). Importing an exported snapshot produces a template;
// this is the step that makes the restored backup startable. The record, disks
// and network cards are kept as imported; nothing is copied. newName, if set,
// renames it. Returns the VM's uuid in Output. xe vm-list hides templates, so
// callers should keep the uuid vm.import returned.
func (m *Module) PromoteTemplate(ctx context.Context, uuid, name, newName string) (*ActionResult, error) {
	resolved, err := m.resolveTemplate(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	if _, err := m.run(ctx, "vm-param-set", "uuid="+resolved, "is-a-template=false"); err != nil {
		return m.failure("promote-template", resolved, err), nil
	}
	if newName != "" {
		if _, err := m.run(ctx, "vm-param-set", "uuid="+resolved, "name-label="+newName); err != nil {
			r := m.failure("promote-template", resolved, err)
			r.Message = "promoted to a VM but renaming failed: " + r.Message
			r.Output = resolved
			return r, nil
		}
	}
	return &ActionResult{Operation: "promote-template", Target: resolved, Success: true,
		Message: "Template promoted to a VM; it can now be started.", Output: resolved}, nil
}

// resolveTemplate returns uuid unchanged after checking it is a template, or
// resolves a template name-label to exactly one uuid.
func (m *Module) resolveTemplate(ctx context.Context, uuid, name string) (string, error) {
	if uuid == "" {
		if name == "" {
			return "", fmt.Errorf("uuid or name is required")
		}
		out, err := m.run(ctx, "template-list", "name-label="+name, "params=uuid", "--minimal")
		if err != nil {
			return "", fmt.Errorf("resolving template name-label %q: %w", name, err)
		}
		return pickUUID(out, fmt.Sprintf("template name-label %q", name))
	}
	// template-list (not vm-list, which hides templates) lists only templates.
	out, err := m.run(ctx, "template-list", "uuid="+uuid, "params=uuid", "--minimal")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s is not a template (it is already a VM, or does not exist)", uuid)
	}
	return uuid, nil
}

// VMImport imports a VM (or, for a snapshot export, a template) from a local
// file, typically staged via internal/modules/nfsmount. The file and target SR
// are checked first so a bad request fails at once rather than after a long
// timeout. The new object's uuid is returned in Data.uuid (and Output). When
// newName is set it is applied as the name-label right after the import.
func (m *Module) VMImport(ctx context.Context, filename, srUUID string, preserve bool, newName string) (*ActionResult, error) {
	if filename == "" {
		return nil, fmt.Errorf("filename is required")
	}
	if !filepath.IsAbs(filename) {
		return nil, fmt.Errorf("filename %q must be an absolute path", filename)
	}
	st, serr := os.Stat(filename)
	switch {
	case os.IsNotExist(serr):
		return nil, fmt.Errorf("image file %s does not exist (is the repository mounted?)", filename)
	case serr != nil:
		return nil, fmt.Errorf("cannot read image file %s: %w", filename, serr)
	case !st.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", filename)
	case st.Size() == 0:
		return nil, fmt.Errorf("image file %s is empty", filename)
	}
	if srUUID != "" {
		if out, err := m.run(ctx, "sr-list", "uuid="+srUUID, "--minimal"); err != nil || strings.TrimSpace(out) == "" {
			return nil, fmt.Errorf("storage repository %s does not exist on this host", srUUID)
		}
	}
	args := []string{"vm-import", "filename=" + filename}
	if srUUID != "" {
		args = append(args, "sr-uuid="+srUUID)
	}
	if preserve {
		args = append(args, "preserve=true")
	}
	if jobs.InJob(ctx) {
		jobs.Report(ctx, 0, st.Size(), "importing "+filename+" (xe reports no import progress)")
	}
	stdout, err := m.runTimeout(ctx, timeoutLong, args...)
	if err != nil {
		return m.failure("vm-import", filename, err), nil
	}
	id := strings.TrimSpace(stdout)
	res := &ActionResult{Operation: "vm-import", Target: filename, Success: true,
		Message: "VM imported successfully.", Output: id, Data: map[string]any{"uuid": id}}
	if newName != "" && id != "" {
		if _, err := m.run(ctx, "vm-param-set", "uuid="+id, "name-label="+newName); err != nil {
			res.Message = fmt.Sprintf("VM imported as %s, but renaming it to %q failed: %v", id, newName, err)
			res.Data = map[string]any{"uuid": id, "renamed": false}
		} else {
			res.Message = fmt.Sprintf("VM imported and named %q.", newName)
			res.Data = map[string]any{"uuid": id, "name_label": newName, "renamed": true}
		}
	}
	return res, nil
}

// VMExport exports a whole VM (metadata + every attached VDI) to a local
// .xva file.
func (m *Module) VMExport(ctx context.Context, uuid, name, filename string, compress bool) (*ActionResult, error) {
	resolved, err := m.resolveVM(ctx, uuid, name)
	if err != nil {
		return nil, err
	}
	if filename == "" {
		return nil, fmt.Errorf("filename is required")
	}
	if err := checkExportTarget(filename); err != nil {
		return nil, err
	}
	args := []string{"vm-export", "vm=" + resolved, "filename=" + partialPath(filename), "compress=" + strconv.FormatBool(compress)}
	stop := trackFile(ctx, partialPath(filename), m.estimateDiskBytes(ctx, resolved))
	defer stop()
	if _, err := m.runTimeout(ctx, timeoutLong, args...); err != nil {
		r := m.failure("vm-export", resolved, err)
		if out, lerr := m.run(ctx, "snapshot-list", "uuid="+resolved, "--minimal"); lerr == nil && strings.TrimSpace(out) != "" {
			r.Message = fmt.Sprintf("%s is a snapshot, not a VM; use xenserver.vm.snapshot_export to export it. (xe: %s)", resolved, err)
		}
		var xe *XEError
		if errors.As(err, &xe) && r.Message == err.Error() {
			vmName, state := m.vmNameAndState(ctx, resolved)
			if vmName == "" {
				vmName = resolved
			}
			r.Message = friendlyVMPowerError("vm-export", vmName, state, false, xe)
		}
		r.Message += removePartial(partialPath(filename))
		return r, nil
	}
	stop()
	if ferr := finalizeExport(filename); ferr != nil {
		return &ActionResult{Operation: "vm-export", Target: resolved, Success: false,
			Message: fmt.Sprintf("export finished but could not be moved to %s: %v", filename, ferr)}, nil
	}
	return &ActionResult{Operation: "vm-export", Target: resolved, Success: true,
		Message: "VM exported successfully." + fileSummary(filename), Output: filename}, nil
}

// checkExportTarget validates a backup destination before any work starts:
// an absolute path, an existing directory, and no file already there.
func checkExportTarget(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename is required")
	}
	if !filepath.IsAbs(filename) {
		return fmt.Errorf("filename must be an absolute path")
	}
	if _, err := os.Stat(filepath.Dir(filename)); err != nil {
		return fmt.Errorf("target directory %s is not usable: %w", filepath.Dir(filename), err)
	}
	if _, err := os.Stat(filename); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite a backup", filename)
	}
	if _, err := os.Stat(partialPath(filename)); err == nil {
		return fmt.Errorf("%s exists: an earlier export to this name was interrupted; delete that leftover file first", partialPath(filename))
	}
	return nil
}

// Exports are written to "<name>.part" and renamed to <name> only when
// complete, so a crash or kill -9 mid-export can never leave a file that looks
// like a finished backup.
const partialSuffix = ".part"

func partialPath(final string) string { return final + partialSuffix }

// finalizeExport promotes a finished partial file to its final name.
func finalizeExport(final string) error { return os.Rename(partialPath(final), final) }

// removePartial deletes a half-written export after a failure so it cannot be
// mistaken for a good backup. Callers only use it after confirming the file
// did not exist before the export started. Returns a message suffix.
func removePartial(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if rerr := os.Remove(path); rerr != nil {
		return fmt.Sprintf(" WARNING: partial file %s (%d bytes) could not be removed: %v", path, st.Size(), rerr)
	}
	return fmt.Sprintf(" Partial file %s (%d bytes) was removed.", path, st.Size())
}

// estimateDiskBytes estimates how many bytes an export of vm (a VM or snapshot
// uuid) will write: the allocated size of its disks. Best effort; 0 if unknown.
func (m *Module) estimateDiskBytes(ctx context.Context, vm string) int64 {
	out, err := m.run(ctx, "vbd-list", "vm-uuid="+vm, "type=Disk", "params=vdi-uuid")
	if err != nil {
		return 0
	}
	var total int64
	for _, b := range parseXEBlocks(out) {
		vdi := b["vdi-uuid"]
		if vdi == "" || vdi == "<not in database>" {
			continue
		}
		total += m.vdiChainBytes(ctx, vdi)
	}
	return total
}

// vdiChainBytes sums the allocated size of a VDI and its VHD parents. A
// snapshot's own layer is tiny; an export writes the whole chain.
func (m *Module) vdiChainBytes(ctx context.Context, vdi string) int64 {
	var total int64
	seen := map[string]bool{}
	for i := 0; i < 32 && vdi != "" && !seen[vdi]; i++ {
		seen[vdi] = true
		vout, err := m.run(ctx, "vdi-list", "uuid="+vdi, "params=physical-utilisation")
		if err != nil {
			break
		}
		if vb := parseXEBlocks(vout); len(vb) > 0 {
			n, _ := strconv.ParseInt(vb[0]["physical-utilisation"], 10, 64)
			total += n
		}
		parent, perr := m.run(ctx, "vdi-param-get", "uuid="+vdi, "param-name=sm-config", "param-key=vhd-parent")
		if perr != nil {
			break
		}
		vdi = strings.TrimSpace(parent)
	}
	return total
}

// trackFile reports the size of path to the running background job every two
// seconds, against an estimated total, until the returned stop func is called.
// It does nothing outside a job.
func trackFile(ctx context.Context, path string, total int64) (stop func()) {
	if !jobs.InJob(ctx) {
		return func() {}
	}
	jobs.Report(ctx, 0, total, "starting export to "+path)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if st, err := os.Stat(path); err == nil {
					jobs.Report(ctx, st.Size(), total, "exporting to "+path+" (total is an estimate)")
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { // safe to call more than once
			close(done)
			if st, err := os.Stat(path); err == nil {
				jobs.Report(ctx, st.Size(), total, "exported to "+path)
			}
		})
	}
}

// fileSummary reports the size of an exported file (best effort).
func fileSummary(path string) string {
	if st, err := os.Stat(path); err == nil {
		return fmt.Sprintf(" File: %s (%d bytes).", path, st.Size())
	}
	return ""
}

// SnapshotExport exports a snapshot as a portable .xva image file
// (xe snapshot-export-to-template). The snapshot is given by uuid or by
// name-label (exactly one match). The target directory must already exist and
// the file must not exist, so a backup is never silently overwritten. The
// export streams the snapshot's metadata and all its disks; its duration and
// the resulting size are reported.
func (m *Module) SnapshotExport(ctx context.Context, uuid, name, filename string) (*ActionResult, error) {
	if err := checkExportTarget(filename); err != nil {
		return nil, err
	}
	snap := uuid
	if snap == "" {
		if name == "" {
			return nil, fmt.Errorf("snapshot uuid or name is required")
		}
		out, err := m.run(ctx, "snapshot-list", "name-label="+name, "--minimal")
		if err != nil {
			return nil, fmt.Errorf("resolving snapshot name-label %q: %w", name, err)
		}
		if snap, err = pickUUID(out, fmt.Sprintf("snapshot name-label %q", name)); err != nil {
			return nil, err
		}
	}
	if out, err := m.run(ctx, "snapshot-list", "uuid="+snap, "--minimal"); err != nil || strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("%s is not a snapshot (use xenserver.vm.export for a halted VM)", snap)
	}

	stop := trackFile(ctx, partialPath(filename), m.estimateDiskBytes(ctx, snap))
	defer stop()
	start := time.Now()
	if _, err := m.runTimeout(ctx, timeoutLong, "snapshot-export-to-template", "snapshot-uuid="+snap, "filename="+partialPath(filename)); err != nil {
		r := m.failure("snapshot-export", snap, err)
		r.Message += removePartial(partialPath(filename))
		return r, nil
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	stop()
	if ferr := finalizeExport(filename); ferr != nil {
		return &ActionResult{Operation: "snapshot-export", Target: snap, Success: false,
			Message: fmt.Sprintf("export finished but could not be moved to %s: %v (the data is in %s)", filename, ferr, partialPath(filename))}, nil
	}
	st, serr := os.Stat(filename)
	if serr != nil {
		return &ActionResult{Operation: "snapshot-export", Target: snap, Success: false,
			Message: fmt.Sprintf("xe reported success but %s was not created: %v", filename, serr)}, nil
	}
	return &ActionResult{Operation: "snapshot-export", Target: snap, Success: true,
		Message: fmt.Sprintf("Snapshot exported to %s (%d bytes) in %s.", filename, st.Size(), elapsed),
		Output:  filename}, nil
}

// VDICopy copies a VDI's data to another (possibly different) SR, returning
// the new VDI's uuid in Output.
func (m *Module) VDICopy(ctx context.Context, vdiUUID, srUUID string) (*ActionResult, error) {
	if vdiUUID == "" || srUUID == "" {
		return nil, fmt.Errorf("vdi_uuid and sr_uuid are required")
	}
	stdout, err := m.runTimeout(ctx, timeoutLong, "vdi-copy", "uuid="+vdiUUID, "sr-uuid="+srUUID)
	if err != nil {
		return m.failure("vdi-copy", vdiUUID, err), nil
	}
	return &ActionResult{Operation: "vdi-copy", Target: vdiUUID, Success: true,
		Message: "VDI copied successfully.", Output: strings.TrimSpace(stdout)}, nil
}

// VDIResize grows a VDI to sizeBytes. The VDI is given directly (vdiUUID) or
// resolved from a VM (vmUUID/vmName) plus the disk's VBD position (device,
// "0" for the first disk). xapi refuses to shrink a VDI. The resulting
// virtual size is read back and returned in Output.
func (m *Module) VDIResize(ctx context.Context, vdiUUID, vmUUID, vmName, device string, sizeBytes int64) (*ActionResult, error) {
	if sizeBytes <= 0 {
		return nil, fmt.Errorf("size_bytes must be > 0")
	}
	if vdiUUID == "" {
		vm, err := m.resolveVM(ctx, vmUUID, vmName)
		if err != nil {
			return nil, fmt.Errorf("vdi_uuid, or vm uuid/name plus device, is required: %w", err)
		}
		if device == "" {
			return nil, fmt.Errorf("vdi_uuid, or vm uuid/name plus device, is required")
		}
		out, err := m.run(ctx, "vbd-list", "vm-uuid="+vm, "userdevice="+device, "type=Disk", "params=vdi-uuid", "--minimal")
		if err != nil {
			return nil, fmt.Errorf("finding disk %s of VM %s: %w", device, vm, err)
		}
		vdiUUID, err = pickUUID(out, fmt.Sprintf("disk %s of VM %s", device, vm))
		if err != nil {
			return nil, err
		}
	}
	before, _ := m.run(ctx, "vdi-param-get", "uuid="+vdiUUID, "param-name=virtual-size")
	if _, err := m.runTimeout(ctx, timeoutMedium, "vdi-resize", "uuid="+vdiUUID, "disk-size="+strconv.FormatInt(sizeBytes, 10)); err != nil {
		r := m.failure("vdi-resize", vdiUUID, err)
		r.Message = fmt.Sprintf("%s (size before: %s bytes)", err, strings.TrimSpace(before))
		var xe *XEError
		if errors.As(err, &xe) && strings.Contains(strings.ToLower(xe.Stderr+xe.Stdout), "in use by some other operation") {
			r.Message = fmt.Sprintf("Cannot resize disk %s: it is plugged into a running VM and this storage does not support online resize (size is still %s bytes). Unplug it first (xenserver.vbd.unplug, after the guest unmounts it) or stop the VM, resize, then plug it again.", vdiUUID, strings.TrimSpace(before))
		}
		return r, nil
	}
	after, _ := m.run(ctx, "vdi-param-get", "uuid="+vdiUUID, "param-name=virtual-size")
	return &ActionResult{Operation: "vdi-resize", Target: vdiUUID, Success: true,
		Message: fmt.Sprintf("VDI resized from %s to %s bytes.", strings.TrimSpace(before), strings.TrimSpace(after)),
		Output:  strings.TrimSpace(after)}, nil
}

// VDIClone creates a fast copy-on-write clone of a VDI within the same SR,
// returning the new VDI's uuid in Output.
func (m *Module) VDIClone(ctx context.Context, vdiUUID string) (*ActionResult, error) {
	if vdiUUID == "" {
		return nil, fmt.Errorf("vdi_uuid is required")
	}
	stdout, err := m.runTimeout(ctx, timeoutMedium, "vdi-clone", "uuid="+vdiUUID)
	if err != nil {
		return m.failure("vdi-clone", vdiUUID, err), nil
	}
	return &ActionResult{Operation: "vdi-clone", Target: vdiUUID, Success: true,
		Message: "VDI cloned successfully.", Output: strings.TrimSpace(stdout)}, nil
}
