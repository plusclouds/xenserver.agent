// Package publisher runs the heartbeat and telemetry loops that push events
// from the agent to the platform via the NATS evt subject.
package publisher

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/config"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	natsclient "github.com/plusclouds/ubuntu-agent/internal/nats"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

const minTelemetryInterval = 1 * time.Second

// Publisher publishes periodic events (heartbeat, telemetry) to the platform.
type Publisher struct {
	nats                *natsclient.Client
	sys                 *system.Module
	xen                 *xenserver.Module
	agentUUID           string
	cfg                 config.AgentConfig
	xenCfg              config.XenServerConfig
	logger              *zap.Logger
	hostname            string
	telemetryIntervalCh chan time.Duration
	// lastRRDEnd tracks the end of the last successfully-fetched RRD window
	// so the next telemetry tick queries only new data, avoiding re-sending
	// the same samples — mirrors rrd.py's own continuation logic. Only ever
	// touched from the single telemetryLoop goroutine.
	lastRRDEnd int64
}

// New creates a Publisher. xen may be nil on agents not managing a
// XenServer/XCP-ng host — the events/IPMI/RRD loops are then no-ops.
func New(
	nats *natsclient.Client,
	sys *system.Module,
	xen *xenserver.Module,
	agentUUID string,
	cfg config.AgentConfig,
	xenCfg config.XenServerConfig,
	logger *zap.Logger,
) *Publisher {
	hostname, _ := os.Hostname()
	return &Publisher{
		nats:                nats,
		sys:                 sys,
		xen:                 xen,
		agentUUID:           agentUUID,
		cfg:                 cfg,
		xenCfg:              xenCfg,
		logger:              logger,
		hostname:            hostname,
		telemetryIntervalCh: make(chan time.Duration, 1),
	}
}

// Start launches the heartbeat and telemetry goroutines (plus, when
// configured, the XAPI events and IPMI loops) and announces capabilities.
// They run until ctx is cancelled.
func (p *Publisher) Start(ctx context.Context) {
	if p.nats == nil {
		p.logger.Info("NATS disabled — periodic publishing (heartbeat, telemetry, events) is off")
		return
	}
	p.sendCapabilities(ctx) //nolint:errcheck — result unused on startup
	go p.heartbeatLoop(ctx)
	go p.telemetryLoop(ctx)
	if p.xen != nil && p.xenCfg.EventsEnabled {
		go p.xapiEventsLoop(ctx)
	}
	if p.xen != nil && p.xenCfg.IPMIEnabled {
		go p.ipmiLoop(ctx)
	}
}

// SetTelemetryInterval updates the telemetry push interval at runtime.
// The minimum allowed value is 5s; smaller values are clamped.
func (p *Publisher) SetTelemetryInterval(d time.Duration) time.Duration {
	if d < minTelemetryInterval {
		d = minTelemetryInterval
	}
	// Non-blocking send: if a pending update is already queued, drop it and
	// replace with the newest value.
	select {
	case p.telemetryIntervalCh <- d:
	default:
		<-p.telemetryIntervalCh
		p.telemetryIntervalCh <- d
	}
	return d
}

func (p *Publisher) heartbeatLoop(ctx context.Context) {
	interval := p.cfg.HeartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	p.sendHeartbeat(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendHeartbeat(ctx)
		}
	}
}

func (p *Publisher) telemetryLoop(ctx context.Context) {
	interval := p.cfg.TelemetryInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	p.sendTelemetry(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-p.telemetryIntervalCh:
			ticker.Reset(newInterval)
			p.logger.Info("telemetry interval updated", zap.Duration("interval", newInterval))
			// Send an immediate snapshot so the platform doesn't wait for the new tick.
			p.sendTelemetry(ctx)
		case <-ticker.C:
			p.sendTelemetry(ctx)
		}
	}
}

func (p *Publisher) sendHeartbeat(ctx context.Context) {
	info, err := p.sys.GetInfo(ctx)
	if err != nil {
		p.logger.Warn("heartbeat: could not get system info", zap.Error(err))
		return
	}

	env, err := protocol.New(p.agentUUID, protocol.TypeHeartbeat, protocol.HeartbeatPayload{
		Version: "0.2.0",
		UptimeS: info.Uptime,
	})
	if err != nil {
		p.logger.Warn("heartbeat: could not build envelope", zap.Error(err))
		return
	}

	if err := p.nats.Publish(env); err != nil {
		p.logger.Warn("heartbeat: publish failed", zap.Error(err))
		return
	}
	p.logger.Debug("heartbeat published")
}

// operationCatalog is the full schema for every supported operation.
// Only operations present in AllowedOperations are published.
var operationCatalog = map[string]protocol.OperationSchema{
	"agent.allowed_operations": {
		Operation:   "agent.allowed_operations",
		Description: "Return and re-publish the current capabilities list (allowed operations and parameters).",
	},
	"services.list": {
		Operation:   "services.list",
		Description: "List all loaded systemd services on the machine.",
	},
	"services.get": {
		Operation:   "services.get",
		Description: "Get detailed status of a single systemd service.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name (e.g. nginx or nginx.service)."},
		},
	},
	"services.start": {
		Operation:   "services.start",
		Description: "Start a stopped systemd service.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name to start."},
		},
	},
	"services.stop": {
		Operation:   "services.stop",
		Description: "Stop a running systemd service.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name to stop."},
		},
	},
	"services.restart": {
		Operation:   "services.restart",
		Description: "Restart a systemd service (stop then start).",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name to restart."},
		},
	},
	"services.reload": {
		Operation:   "services.reload",
		Description: "Send a reload signal to a running service without stopping it.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name to reload."},
		},
	},
	"services.enable": {
		Operation:   "services.enable",
		Description: "Enable a service to start automatically on boot.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name to enable."},
		},
	},
	"services.disable": {
		Operation:   "services.disable",
		Description: "Disable a service from starting automatically on boot.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: true, Description: "Service name to disable."},
		},
	},
	"system.info": {
		Operation:   "system.info",
		Description: "Return static system information (hostname, OS, kernel, uptime).",
	},
	"system.metrics": {
		Operation:   "system.metrics",
		Description: "Return a full resource snapshot (CPU, memory, disk, network).",
	},
	"system.cpu": {
		Operation:   "system.cpu",
		Description: "Return CPU usage, core count, load averages, and per-core usage.",
	},
	"system.memory": {
		Operation:   "system.memory",
		Description: "Return RAM utilisation (total, used, usage %).",
	},
	"system.disk": {
		Operation:   "system.disk",
		Description: "Return disk usage and I/O stats for all real block-device partitions.",
	},
	"system.network": {
		Operation:   "system.network",
		Description: "Return network I/O counters for all physical interfaces.",
	},
	"system.update": {
		Operation:   "system.update",
		Description: "Run apt-get update && apt-get upgrade -y. Supported on Ubuntu/Debian only.",
	},
	"telemetry.set_interval": {
		Operation:   "telemetry.set_interval",
		Description: "Change the telemetry push interval at runtime. Minimum 5 seconds.",
		Params: []protocol.OperationParam{
			{Name: "interval_s", Type: "integer", Required: true,
				Description: "New telemetry interval in seconds.", MinValue: intPtr(1)},
		},
	},
	"vm.reboot": {
		Operation:   "vm.reboot",
		Description: "Reboot the machine immediately.",
	},
	"vm.shutdown": {
		Operation:   "vm.shutdown",
		Description: "Shut down the machine immediately.",
	},
	"exec": {
		Operation:   "exec",
		Description: "Execute an allowed binary on the machine. The command must be listed in exec_commands.",
		Params: []protocol.OperationParam{
			{Name: "command", Type: "string", Required: true, Description: "Absolute path of the binary to run. Must be in the exec_commands allowlist."},
			{Name: "args", Type: "array", Required: false, Description: "List of arguments to pass to the binary."},
		},
	},

	// ---- xenserver: read-only -------------------------------------------
	"xenserver.pool.info": {
		Operation:   "xenserver.pool.info",
		Description: "Return this host's XenServer/XCP-ng resource pool record.",
	},
	"xenserver.host.list": {
		Operation:   "xenserver.host.list",
		Description: "List every host in the pool.",
	},
	"xenserver.host.info": {
		Operation:   "xenserver.host.info",
		Description: "Return the detailed record for this host.",
	},
	"xenserver.vm.list": {
		Operation:   "xenserver.vm.list",
		Description: "List every guest VM on the pool (control domains and templates excluded).",
	},
	"xenserver.vm.get": {
		Operation:   "xenserver.vm.get",
		Description: "Return the detailed record for one VM.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.snapshot_list": {
		Operation:   "xenserver.vm.snapshot_list",
		Description: "List every snapshot of a VM.",
		Params:      vmIdentifierParams,
	},
	"xenserver.sr.list": {
		Operation:   "xenserver.sr.list",
		Description: "List every storage repository visible to the pool.",
	},
	"xenserver.sr.get": {
		Operation:   "xenserver.sr.get",
		Description: "Return the detailed record for one storage repository.",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: false, Description: "SR uuid. Either uuid or name is required."},
			{Name: "name", Type: "string", Required: false, Description: "SR name-label. Must resolve to exactly one SR."},
		},
	},
	"xenserver.network.list": {
		Operation:   "xenserver.network.list",
		Description: "List every xapi network (OVS bridge) in the pool.",
	},
	"xenserver.vif.list": {
		Operation:   "xenserver.vif.list",
		Description: "List every virtual network interface attached to a VM.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.disk_list": {
		Operation:   "xenserver.vm.disk_list",
		Description: "List a VM's disks and CD drives by position (VBD userdevice; \"0\" is the first disk) with each VDI's uuid, name, size and SR. Use the position with xenserver.vdi.resize.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vdi.list": {
		Operation:   "xenserver.vdi.list",
		Description: "List virtual disk images, optionally filtered to one SR.",
		Params: []protocol.OperationParam{
			{Name: "sr_uuid", Type: "string", Required: false, Description: "Restrict the list to this SR uuid."},
		},
	},
	"xenserver.ipmi.sensors": {
		Operation:   "xenserver.ipmi.sensors",
		Description: "Read the current IPMI sensor table (ipmitool sensor): name, reading, units, status and thresholds for temperatures, fans, voltages, currents, power and discrete states. Optional units filter, e.g. [\"degrees C\",\"RPM\"]. Replaces the ipmi.py sidecar script.",
		Params: []protocol.OperationParam{
			{Name: "units", Type: "array", Required: false, Description: "Only sensors with these units: \"degrees C\", \"RPM\", \"Watts\", \"Volts\", \"Amps\", \"percent\", \"discrete\"."},
		},
	},
	"xenserver.metrics.rrd": {
		Operation:   "xenserver.metrics.rrd",
		Description: "Return the latest XAPI RRD-derived utilisation snapshot: host metrics (cpu, memory, disk, network) and, per VM, cpu0..N, memory, memory_internal_free, vbd_*_iops/latency/throughput and vif_*_rx/tx. Give uuid to get only that VM (the host block is then omitted). Pass since (Unix time) to choose the window start; only its newest sample is returned, so poll with end_time+1 to build a series. Values are 60 s averages.",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: false, Description: "Only this VM's metrics."},
			{Name: "since", Type: "integer", Required: false, Description: "Unix time window start (default: the last 3 minutes)."},
		},
	},

	// ---- xenserver: state-changing ---------------------------------------
	"xenserver.vm.start": {
		Operation:   "xenserver.vm.start",
		Description: "Start a stopped VM.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.shutdown": {
		Operation:   "xenserver.vm.shutdown",
		Description: "Perform a clean ACPI shutdown of a running VM. Needs Xen guest tools in the VM; otherwise use xenserver.vm.force_shutdown.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.reboot": {
		Operation:   "xenserver.vm.reboot",
		Description: "Perform a clean reboot of a running VM. Needs Xen guest tools in the VM; otherwise use xenserver.vm.force_reboot.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.force_shutdown": {
		Operation:   "xenserver.vm.force_shutdown",
		Description: "Hard power-off of a running VM (xe vm-shutdown force=true). Does not need the guest's cooperation; the guest gets no chance to flush data.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.force_reboot": {
		Operation:   "xenserver.vm.force_reboot",
		Description: "Hard reboot of a running VM (xe vm-reboot force=true). Does not need the guest's cooperation; the guest gets no chance to flush data.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.suspend": {
		Operation:   "xenserver.vm.suspend",
		Description: "Suspend a running VM to disk.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.resume": {
		Operation:   "xenserver.vm.resume",
		Description: "Resume a suspended VM.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.snapshot_create": {
		Operation:   "xenserver.vm.snapshot_create",
		Description: "Take a new snapshot of a VM.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "label", Type: "string", Required: true, Description: "Name-label for the new snapshot."}),
	},
	"xenserver.vm.param_set": {
		Operation:   "xenserver.vm.param_set",
		Description: "Set allowlisted VM parameters in one call: name-label, name-description, VCPUs-max, VCPUs-at-startup, and map entries platform:<key>, xenstore-data:<key>, other-config:<key>. Values are passed to xe as single arguments (spaces and quotes are safe); keys outside the allowlist are rejected and nothing is changed. VCPUs-max is applied before VCPUs-at-startup. Returns the list of applied changes.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "set", Type: "object", Required: false, Description: "Map of parameter to new value, e.g. {\"name-label\":\"web1\",\"xenstore-data:vm-data/hostname\":\"web1\"}."},
			protocol.OperationParam{Name: "unset", Type: "array", Required: false, Description: "Map entries to remove, e.g. [\"xenstore-data:old-key\"]."}),
	},
	"xenserver.vm.param_get": {
		Operation:   "xenserver.vm.param_get",
		Description: "Read one VM parameter, or one key of a map parameter (param=xenstore-data, param_key=<key>).",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "param", Type: "string", Required: true, Description: "Parameter name, e.g. name-label, power-state, VCPUs-max, platform."},
			protocol.OperationParam{Name: "param_key", Type: "string", Required: false, Description: "Key within a map parameter."}),
	},
	"xenserver.vm.param_list": {
		Operation:   "xenserver.vm.param_list",
		Description: "Return every parameter of a VM as JSON: params (scalars), maps (xenstore-data, platform, other-config, ... as objects) and sets (allowed-operations, VBDs, VIFs, ... as arrays).",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.memory_limits_set": {
		Operation:   "xenserver.vm.memory_limits_set",
		Description: "Set the memory limits in bytes on a halted VM. Give all four (static_min <= dynamic_min <= dynamic_max <= static_max) or just memory_bytes to set all four equal. On hosts with dynamic memory control (DMC) disabled, as here, static_max, dynamic_min and dynamic_max must be equal (static_min may be lower); memory_bytes is the simple way.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "memory_bytes", Type: "integer", Required: false, Description: "Set all four limits to this value (the simple choice when dynamic memory control is disabled)."},
			protocol.OperationParam{Name: "static_min", Type: "integer", Required: false, Description: "Minimum static memory, bytes."},
			protocol.OperationParam{Name: "static_max", Type: "integer", Required: false, Description: "Maximum static memory, bytes."},
			protocol.OperationParam{Name: "dynamic_min", Type: "integer", Required: false, Description: "Minimum dynamic memory, bytes."},
			protocol.OperationParam{Name: "dynamic_max", Type: "integer", Required: false, Description: "Maximum dynamic memory, bytes."}),
	},
	"xenserver.vm.console_list": {
		Operation:   "xenserver.vm.console_list",
		Description: "List a VM's consoles: uuid, protocol (vnc, rfb, vt100) and location URL.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.create": {
		Operation:   "xenserver.vm.create",
		Description: "Create a VM. With template (uuid or name-label) it installs a new VM from that template (xe vm-install, copying its disks to sr_uuid; slow, use async). Without template it creates an empty VM record to configure with vm.param_set. Returns data.uuid.",
		Params: []protocol.OperationParam{
			{Name: "new_name", Type: "string", Required: true, Description: "Name-label of the new VM."},
			{Name: "description", Type: "string", Required: false, Description: "Name-description."},
			{Name: "template", Type: "string", Required: false, Description: "Template uuid or name-label to install from."},
			{Name: "sr_uuid", Type: "string", Required: false, Description: "SR for the new VM's disks when installing from a template."},
		},
	},
	"xenserver.vm.clone": {
		Operation:   "xenserver.vm.clone",
		Description: "Clone a VM (copy-on-write where the SR supports it). The source must be halted or a snapshot. Slow on some SRs, so use async. Returns data.uuid.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "new_name", Type: "string", Required: true, Description: "Name-label of the clone."},
			protocol.OperationParam{Name: "description", Type: "string", Required: false, Description: "Name-description of the clone."}),
	},
	"xenserver.vm.destroy": {
		Operation:   "xenserver.vm.destroy",
		Description: "Delete a halted VM's record. IRREVERSIBLE; requires confirm=true. Its disks stay on the SR and must be removed separately. Control domains are refused.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "confirm", Type: "boolean", Required: true, Description: "Must be true."}),
	},
	"xenserver.vm.pause": {
		Operation:   "xenserver.vm.pause",
		Description: "Pause a running VM (frozen in memory; unlike suspend nothing is written to disk).",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.unpause": {
		Operation:   "xenserver.vm.unpause",
		Description: "Unpause a paused VM.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vdi.create": {
		Operation:   "xenserver.vdi.create",
		Description: "Create a virtual disk on an SR. Returns data.uuid. Attach it to a VM with xenserver.vbd.create.",
		Params: []protocol.OperationParam{
			{Name: "sr_uuid", Type: "string", Required: true, Description: "SR to create the disk on."},
			{Name: "new_name", Type: "string", Required: true, Description: "Name-label of the disk."},
			{Name: "size_bytes", Type: "integer", Required: true, Description: "Virtual size in bytes."},
			{Name: "description", Type: "string", Required: false, Description: "Name-description."},
			{Name: "type", Type: "string", Required: false, Description: "Disk type, default user."},
			{Name: "sharable", Type: "boolean", Required: false, Description: "Allow attaching to several VMs."},
			{Name: "read_only", Type: "boolean", Required: false, Description: "Create read-only."},
		},
	},
	"xenserver.vdi.destroy": {
		Operation:   "xenserver.vdi.destroy",
		Description: "Permanently delete a disk and its data. IRREVERSIBLE; requires confirm=true. A disk attached to a VM is refused.",
		Params: []protocol.OperationParam{
			{Name: "vdi_uuid", Type: "string", Required: true, Description: "Disk to delete."},
			{Name: "confirm", Type: "boolean", Required: true, Description: "Must be true."},
			{Name: "allow_attached", Type: "boolean", Required: false, Description: "Destroy even if the disk is still attached to a VM (default: refused; detach it first with vbd.destroy)."},
		},
	},
	"xenserver.vdi.param_list": {
		Operation:   "xenserver.vdi.param_list",
		Description: "All parameters of a disk as JSON: params (scalars), maps (sm-config, other-config, ... as objects), sets (arrays).",
		Params:      []protocol.OperationParam{{Name: "vdi_uuid", Type: "string", Required: true, Description: "Disk uuid."}},
	},
	"xenserver.vbd.create": {
		Operation:   "xenserver.vbd.create",
		Description: "Attach a disk to a VM (creates the VBD, unplugged; plug it with xenserver.vbd.plug if the VM runs). Returns data.uuid.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "vdi_uuid", Type: "string", Required: false, Description: "Disk to attach (required for type Disk)."},
			protocol.OperationParam{Name: "device", Type: "string", Required: false, Description: "Position (\"0\", \"1\", ...) or autodetect (default)."},
			protocol.OperationParam{Name: "mode", Type: "string", Required: false, Description: "RW (default) or RO."},
			protocol.OperationParam{Name: "type", Type: "string", Required: false, Description: "Disk (default) or CD."},
			protocol.OperationParam{Name: "bootable", Type: "boolean", Required: false, Description: "Mark bootable."}),
	},
	"xenserver.vbd.plug": {
		Operation:   "xenserver.vbd.plug",
		Description: "Hot-attach a VBD to a running VM.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "vbd_uuid", Type: "string", Required: false, Description: "VBD uuid. If omitted, uuid/name plus device are used."},
			protocol.OperationParam{Name: "device", Type: "string", Required: false, Description: "Disk position (\"0\" = first), used with uuid/name when vbd_uuid is omitted."}),
	},
	"xenserver.vbd.unplug": {
		Operation:   "xenserver.vbd.unplug",
		Description: "Hot-detach a VBD from a running VM (guest must release the disk).",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "vbd_uuid", Type: "string", Required: false, Description: "VBD uuid. If omitted, uuid/name plus device are used."},
			protocol.OperationParam{Name: "device", Type: "string", Required: false, Description: "Disk position (\"0\" = first), used with uuid/name when vbd_uuid is omitted."}),
	},
	"xenserver.vbd.destroy": {
		Operation:   "xenserver.vbd.destroy",
		Description: "Remove a disk's attachment to a VM; the disk and its data stay on the SR. The VBD must be unplugged or the VM halted.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "vbd_uuid", Type: "string", Required: false, Description: "VBD uuid. If omitted, uuid/name plus device are used."},
			protocol.OperationParam{Name: "device", Type: "string", Required: false, Description: "Disk position (\"0\" = first), used with uuid/name when vbd_uuid is omitted."}),
	},
	"xenserver.vbd.param_list": {
		Operation:   "xenserver.vbd.param_list",
		Description: "All parameters of a VBD as JSON (params, maps, sets).",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "vbd_uuid", Type: "string", Required: false, Description: "VBD uuid. If omitted, uuid/name plus device are used."},
			protocol.OperationParam{Name: "device", Type: "string", Required: false, Description: "Disk position (\"0\" = first), used with uuid/name when vbd_uuid is omitted."}),
	},
	"xenserver.vm.cd_list": {
		Operation:   "xenserver.vm.cd_list",
		Description: "List a VM's CD/DVD drives: vbd_uuid, position, empty, iso_name, iso_uuid, currently_attached, mode.",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.cd_add": {
		Operation:   "xenserver.vm.cd_add",
		Description: "Add a CD drive holding an ISO from an ISO SR. device defaults to autodetect.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "cd_name", Type: "string", Required: true, Description: "ISO name in an ISO SR."},
			protocol.OperationParam{Name: "device", Type: "string", Required: false, Description: "Device position."}),
	},
	"xenserver.vm.cd_insert": {
		Operation:   "xenserver.vm.cd_insert",
		Description: "Insert an ISO into the VM's existing empty CD drive.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "cd_name", Type: "string", Required: true, Description: "ISO name in an ISO SR."}),
	},
	"xenserver.vm.cd_eject": {
		Operation:   "xenserver.vm.cd_eject",
		Description: "Eject the ISO from the VM's CD drive (the drive stays).",
		Params:      vmIdentifierParams,
	},
	"xenserver.vm.cd_remove": {
		Operation:   "xenserver.vm.cd_remove",
		Description: "Remove the CD drive that currently holds the named ISO. An empty drive cannot be found this way: remove it with xenserver.vbd.destroy and its device position (see xenserver.vm.cd_list).",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "cd_name", Type: "string", Required: true, Description: "ISO name in the drive."}),
	},
	"xenserver.vm.promote_template": {
		Operation:   "xenserver.vm.promote_template",
		Description: "Turn a template into an ordinary VM in place — the step that makes a restored snapshot export startable (importing one yields a template). Keeps disks and network cards; optionally renames.",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: false, Description: "Template uuid, e.g. the one vm.import returned. Either uuid or name is required."},
			{Name: "name", Type: "string", Required: false, Description: "Template name-label. Must resolve to exactly one template."},
			{Name: "new_name", Type: "string", Required: false, Description: "New name-label for the resulting VM."},
		},
	},
	"xenserver.vm.snapshot_export": {
		Operation:   "xenserver.vm.snapshot_export",
		Description: "Export a snapshot as a portable .xva image file (metadata plus all disks) — the first step of a backup. Refuses to overwrite an existing file; the target directory must exist. Reports size and duration.",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: false, Description: "Snapshot uuid. Either uuid or name is required."},
			{Name: "name", Type: "string", Required: false, Description: "Snapshot name-label. Must resolve to exactly one snapshot."},
			{Name: "filename", Type: "string", Required: true, Description: "Absolute path of the .xva file to create, e.g. on a mounted NFS repository."},
		},
	},
	"xenserver.vm.snapshot_delete": {
		Operation:   "xenserver.vm.snapshot_delete",
		Description: "Destroy a snapshot.",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: true, Description: "The snapshot's own uuid (not the parent VM's uuid)."},
		},
	},
	"xenserver.sr.create": {
		Operation:   "xenserver.sr.create",
		Description: "Introduce a new storage repository (xe sr-create). Works for ISO repositories too: type=iso, content_type=iso, shared=true, device_config={\"type\":\"nfs_iso\",\"location\":\"<ip>:<path>\",\"nfsversion\":\"4\"}. With if_not_exists=true it does nothing when an SR with that name-label already exists (data.existing=true). Returns data.uuid.",
		Params: []protocol.OperationParam{
			{Name: "name", Type: "string", Required: false, Description: "Name-label for the new SR. name_label is accepted as an alias; one of the two is required."},
			{Name: "name_label", Type: "string", Required: false, Description: "Alias of name."},
			{Name: "if_not_exists", Type: "boolean", Required: false, Description: "Skip creation (and succeed) when an SR with this name-label already exists."},
			{Name: "type", Type: "string", Required: true, Description: "SR type, e.g. nfs, lvmoiscsi, ext, lvm."},
			{Name: "shared", Type: "boolean", Required: false, Description: "Whether the SR is shared across the pool."},
			{Name: "content_type", Type: "string", Required: false, Description: "SR content-type, e.g. user or iso."},
			{Name: "host_uuid", Type: "string", Required: false, Description: "Host to attach the SR to. Defaults to this host."},
			{Name: "device_config", Type: "object", Required: true, Description: "SR-type-specific device-config key/value pairs (e.g. server, serverpath for NFS)."},
		},
	},
	"xenserver.sr.remove": {
		Operation:   "xenserver.sr.remove",
		Description: "Safely detach an SR (unplug all PBDs, then forget the SR record). Data is preserved.",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: false, Description: "SR uuid. Either uuid or name is required."},
			{Name: "name", Type: "string", Required: false, Description: "SR name-label. Must resolve to exactly one SR."},
		},
	},
	"xenserver.sr.set_default": {
		Operation:   "xenserver.sr.set_default",
		Description: "Make an SR the pool's primary storage (default-SR, suspend-image-SR and crash-dump-SR).",
		Params: []protocol.OperationParam{
			{Name: "uuid", Type: "string", Required: false, Description: "SR uuid. Either uuid or name is required."},
			{Name: "name", Type: "string", Required: false, Description: "SR name-label. Must resolve to exactly one SR."},
		},
	},
	"xenserver.vif.set_network": {
		Operation:   "xenserver.vif.set_network",
		Description: "Move a VM's network card to another network. xapi cannot re-point a VIF, so it is recreated with the same VM, device index, MAC, MTU and locking mode (restored on its old network if the recreate fails). The VM must be halted or the VIF unplugged. The new VIF uuid is returned in output; the VIF uuid changes.",
		Params: []protocol.OperationParam{
			{Name: "network_uuid", Type: "string", Required: false, Description: "Target network uuid. Either network_uuid or network_name is required."},
			{Name: "network_name", Type: "string", Required: false, Description: "Target network name-label. Must resolve to exactly one network."},
			{Name: "vif_uuid", Type: "string", Required: false, Description: "VIF uuid. If omitted, uuid/name plus device are used to resolve it."},
			{Name: "uuid", Type: "string", Required: false, Description: "VM uuid, used with device when vif_uuid is omitted."},
			{Name: "name", Type: "string", Required: false, Description: "VM name-label, used with device when vif_uuid is omitted."},
			{Name: "device", Type: "string", Required: false, Description: "VIF device index (e.g. \"0\"), used with uuid/name when vif_uuid is omitted."},
		},
	},
	"xenserver.vif.remove": {
		Operation:   "xenserver.vif.remove",
		Description: "Delete a VM's virtual network card (VIF). The VM must be halted or the VIF unplugged. The removed VIF's device, MAC and network are returned in output.",
		Params: []protocol.OperationParam{
			{Name: "vif_uuid", Type: "string", Required: false, Description: "VIF uuid. If omitted, uuid/name plus device are used to resolve it."},
			{Name: "uuid", Type: "string", Required: false, Description: "VM uuid, used with device when vif_uuid is omitted."},
			{Name: "name", Type: "string", Required: false, Description: "VM name-label, used with device when vif_uuid is omitted."},
			{Name: "device", Type: "string", Required: false, Description: "VIF device index (e.g. \"5\"), used with uuid/name when vif_uuid is omitted."},
		},
	},
	"xenserver.vif.set_acl": {
		Operation:   "xenserver.vif.set_acl",
		Description: "Set xapi's native VIF anti-spoofing/allowlist ACL (locking-mode, ipv4-allowed, ipv6-allowed).",
		Params: []protocol.OperationParam{
			{Name: "vif_uuid", Type: "string", Required: false, Description: "VIF uuid. If omitted, uuid/name plus device are used to resolve it."},
			{Name: "uuid", Type: "string", Required: false, Description: "VM uuid, used with device when vif_uuid is omitted."},
			{Name: "name", Type: "string", Required: false, Description: "VM name-label, used with device when vif_uuid is omitted."},
			{Name: "device", Type: "string", Required: false, Description: "VIF device index (e.g. \"0\"), used with uuid/name when vif_uuid is omitted."},
			{Name: "locking_mode", Type: "string", Required: true, Description: "One of: locked, unlocked, disabled."},
			{Name: "ipv4_allowed", Type: "array", Required: false, Description: "Allowed IPv4 addresses when locking_mode=locked."},
			{Name: "ipv6_allowed", Type: "array", Required: false, Description: "Allowed IPv6 addresses when locking_mode=locked."},
		},
	},
	"xenserver.vm.import": {
		Operation:   "xenserver.vm.import",
		Description: "Import a VM from a local .xva/.ova file (typically staged via nfsmount.mount). The file and SR are checked first, so a bad path or SR fails at once. Returns data.uuid of the imported VM (or template, for a snapshot export), also in the async job result. Supports async.",
		Params: []protocol.OperationParam{
			{Name: "filename", Type: "string", Required: true, Description: "Absolute path to the VM export file; it must exist."},
			{Name: "sr_uuid", Type: "string", Required: false, Description: "Target SR for the imported VM's disks; must exist on this host."},
			{Name: "new_name", Type: "string", Required: false, Description: "Name-label to set right after the import (data.renamed reports whether it applied)."},
			{Name: "preserve", Type: "boolean", Required: false, Description: "Preserve the original MAC addresses and uuids."},
		},
	},
	"xenserver.vm.export": {
		Operation:   "xenserver.vm.export",
		Description: "Export a whole VM (metadata + every attached VDI) to a local .xva file.",
		Params: append(append([]protocol.OperationParam{}, vmIdentifierParams...),
			protocol.OperationParam{Name: "filename", Type: "string", Required: true, Description: "Destination path for the .xva file."},
			protocol.OperationParam{Name: "compress", Type: "boolean", Required: false, Description: "Compress the export."},
		),
	},
	"xenserver.vdi.copy": {
		Operation:   "xenserver.vdi.copy",
		Description: "Copy a VDI's data to another (possibly different) SR.",
		Params: []protocol.OperationParam{
			{Name: "vdi_uuid", Type: "string", Required: true, Description: "Source VDI uuid."},
			{Name: "sr_uuid", Type: "string", Required: true, Description: "Destination SR uuid."},
		},
	},
	"xenserver.vdi.resize": {
		Operation:   "xenserver.vdi.resize",
		Description: "Grow a VDI to a new size (xapi refuses to shrink). Identify it by vdi_uuid, or by VM uuid/name plus the disk's position (device).",
		Params: []protocol.OperationParam{
			{Name: "size_bytes", Type: "integer", Required: true, Description: "New total virtual size in bytes."},
			{Name: "vdi_uuid", Type: "string", Required: false, Description: "VDI uuid. If omitted, uuid/name plus device are used."},
			{Name: "uuid", Type: "string", Required: false, Description: "VM uuid, used with device when vdi_uuid is omitted."},
			{Name: "name", Type: "string", Required: false, Description: "VM name-label, used with device when vdi_uuid is omitted."},
			{Name: "device", Type: "string", Required: false, Description: "Disk position (VBD userdevice), e.g. \"0\" for the first disk."},
		},
	},
	"xenserver.vdi.clone": {
		Operation:   "xenserver.vdi.clone",
		Description: "Create a fast copy-on-write clone of a VDI within the same SR.",
		Params: []protocol.OperationParam{
			{Name: "vdi_uuid", Type: "string", Required: true, Description: "VDI uuid to clone."},
		},
	},
	"xenserver.vdi.export": {
		Operation:   "xenserver.vdi.export",
		Description: "Export a single VDI's raw disk content to a local file via xapi's HTTP export interface.",
		Params: []protocol.OperationParam{
			{Name: "vdi_uuid", Type: "string", Required: true, Description: "VDI uuid to export."},
			{Name: "filename", Type: "string", Required: true, Description: "Destination path for the exported disk data."},
			{Name: "format", Type: "string", Required: false, Description: "Export format: vhd (default) or raw."},
		},
	},

	// ---- openvswitch: read-only ------------------------------------------
	"openvswitch.bridge.list": {
		Operation:   "openvswitch.bridge.list",
		Description: "List every OVS bridge on the host.",
	},
	"openvswitch.port.list": {
		Operation:   "openvswitch.port.list",
		Description: "List every port on a bridge.",
		Params: []protocol.OperationParam{
			{Name: "bridge", Type: "string", Required: true, Description: "OVS bridge name."},
		},
	},
	"openvswitch.flow.list": {
		Operation:   "openvswitch.flow.list",
		Description: "Dump the raw OpenFlow flow table for a bridge.",
		Params: []protocol.OperationParam{
			{Name: "bridge", Type: "string", Required: true, Description: "OVS bridge name."},
		},
	},

	// ---- openvswitch: state-changing --------------------------------------
	"openvswitch.vxlan.create": {
		Operation:   "openvswitch.vxlan.create",
		Description: "Add a VXLAN encapsulation port to a bridge.",
		Params: []protocol.OperationParam{
			{Name: "bridge", Type: "string", Required: false, Description: "OVS bridge name. Either bridge or network_uuid is required."},
			{Name: "network_uuid", Type: "string", Required: false, Description: "xapi network uuid; its bridge is resolved automatically (the durable, xapi-aware pattern)."},
			{Name: "port_name", Type: "string", Required: true, Description: "Name for the new VXLAN interface."},
			{Name: "remote_ip", Type: "string", Required: true, Description: "Remote VTEP IP address."},
			{Name: "local_ip", Type: "string", Required: false, Description: "Local VTEP IP address."},
			{Name: "vni", Type: "integer", Required: false, Description: "VXLAN network identifier (key)."},
		},
	},
	"openvswitch.vxlan.remove": {
		Operation:   "openvswitch.vxlan.remove",
		Description: "Remove a VXLAN port from a bridge.",
		Params: []protocol.OperationParam{
			{Name: "bridge", Type: "string", Required: true, Description: "OVS bridge name."},
			{Name: "port_name", Type: "string", Required: true, Description: "VXLAN interface name to remove."},
		},
	},
	"openvswitch.flow.add": {
		Operation:   "openvswitch.flow.add",
		Description: "Install a raw OVS flow rule on a bridge.",
		Params: []protocol.OperationParam{
			{Name: "bridge", Type: "string", Required: true, Description: "OVS bridge name."},
			{Name: "flow_spec", Type: "string", Required: true, Description: "ovs-ofctl flow syntax, e.g. \"priority=100,ip,nw_src=10.0.0.0/24,actions=drop\"."},
		},
	},
	"openvswitch.flow.remove": {
		Operation:   "openvswitch.flow.remove",
		Description: "Remove OVS flow rules matching a spec on a bridge.",
		Params: []protocol.OperationParam{
			{Name: "bridge", Type: "string", Required: true, Description: "OVS bridge name."},
			{Name: "match_spec", Type: "string", Required: true, Description: "ovs-ofctl match syntax for the flows to remove."},
		},
	},

	// ---- nfsmount ----------------------------------------------------------
	"nfsmount.list": {
		Operation:   "nfsmount.list",
		Description: "List every currently-mounted NFS filesystem.",
	},
	"nfsmount.list_files": {
		Operation:   "nfsmount.list_files",
		Description: "List regular files directly under a mounted NFS mount point (e.g. an image repository), without a generic exec. Not recursive; mount_point must be a currently-mounted NFS mount.",
		Params: []protocol.OperationParam{
			{Name: "mount_point", Type: "string", Required: true, Description: "A currently-mounted NFS mount point, as shown by nfsmount.list."},
			{Name: "suffix", Type: "string", Required: false, Description: "Only names ending with this (case-insensitive), e.g. .xva or .pvm."},
			{Name: "sort_by", Type: "string", Required: false, Description: "name (default), size or mtime."},
			{Name: "descending", Type: "boolean", Required: false, Description: "Reverse the sort order."},
			{Name: "limit", Type: "integer", Required: false, Description: "Return at most this many files."},
		},
	},
	"nfsmount.mount": {
		Operation:   "nfsmount.mount",
		Description: "Mount an NFS export, typically to stage a VM import/export file. Mount points are not confined to a base directory.",
		Params: []protocol.OperationParam{
			{Name: "server", Type: "string", Required: true, Description: "NFS server address."},
			{Name: "export_path", Type: "string", Required: true, Description: "Remote export path."},
			{Name: "mount_point", Type: "string", Required: true, Description: "Local directory to mount at (created if missing)."},
			{Name: "mount_options", Type: "string", Required: false, Description: "Extra mount -o options."},
		},
	},
	"nfsmount.unmount": {
		Operation:   "nfsmount.unmount",
		Description: "Unmount a previously mounted NFS filesystem.",
		Params: []protocol.OperationParam{
			{Name: "mount_point", Type: "string", Required: true, Description: "Local mount point to unmount."},
		},
	},
}

// vmIdentifierParams is the shared "uuid or name" descriptor for operations
// that target a single VM. uuid wins when both are given; name must resolve
// to exactly one VM.
func init() {
	operationCatalog["agent.job.status"] = protocol.OperationSchema{
		Operation:   "agent.job.status",
		Description: "Status and progress of a background job started with params.async=true: running/completed/failed/cancelled, elapsed time, progress (bytes_done, estimated bytes_total, percent) and, once finished, the operation's own result.",
		Params:      []protocol.OperationParam{{Name: "job_id", Type: "string", Required: true, Description: "The job_id returned when the operation was started."}},
	}
	operationCatalog["agent.job.list"] = protocol.OperationSchema{
		Operation:   "agent.job.list",
		Description: "List background jobs (running and recently finished), newest first. Jobs are forgotten when the agent restarts.",
	}
	operationCatalog["agent.job.cancel"] = protocol.OperationSchema{
		Operation:   "agent.job.cancel",
		Description: "Cancel a running background job. A half-written export file is removed.",
		Params:      []protocol.OperationParam{{Name: "job_id", Type: "string", Required: true, Description: "The job to cancel."}},
	}
	// Long operations can run in the background: the call returns 202 /
	// status "accepted" with a job_id at once instead of blocking.
	for _, op := range []string{"xenserver.vm.snapshot_export", "xenserver.vm.export", "xenserver.vm.import",
		"xenserver.vdi.copy", "xenserver.vdi.clone", "xenserver.vdi.export",
		"xenserver.vm.clone", "xenserver.vm.create", "xenserver.sr.scan"} {
		if s, ok := operationCatalog[op]; ok {
			s.Params = append(append([]protocol.OperationParam{}, s.Params...), protocol.OperationParam{
				Name: "async", Type: "boolean", Required: false,
				Description: "Run in the background: reply immediately with a job_id and poll agent.job.status for progress.",
			}, protocol.OperationParam{
				Name: "callback", Type: "object", Required: false,
				Description: "With async: {\"url\":\"https://...\",\"token\":\"...\"}. When the job ends the agent POSTs {agent_uuid, job_id, operation, status, error, elapsed_s, result} to the URL with the token as a bearer header (retried up to 4 times). The URL must be under an agent.callback_allowed_urls prefix; otherwise the request is rejected. Polling agent.job.status always works too.",
			})
			operationCatalog[op] = s
		}
	}
}

var vmIdentifierParams = []protocol.OperationParam{
	{Name: "uuid", Type: "string", Required: false, Description: "VM uuid. Either uuid or name is required."},
	{Name: "name", Type: "string", Required: false, Description: "VM name-label. Must resolve to exactly one VM."},
}

func intPtr(v int) *int { return &v }

// SendCapabilities re-publishes the capabilities event and returns the payload.
// Called on boot and whenever the platform sends an agent.allowed_operations command.
func (p *Publisher) SendCapabilities(ctx context.Context) (*protocol.CapabilitiesPayload, error) {
	payload := p.sendCapabilities(ctx)
	return payload, nil
}

func (p *Publisher) sendCapabilities(_ context.Context) *protocol.CapabilitiesPayload {
	p.logger.Info("capabilities: building from config",
		zap.Int("allowed_operations_in_config", len(p.cfg.AllowedOperations)),
		zap.Strings("allowed_operations", p.cfg.AllowedOperations),
	)

	// If allowed_operations is empty (config not loaded or intentionally blank),
	// fall back to publishing every operation in the catalog so the platform
	// always receives a usable list.
	source := p.cfg.AllowedOperations
	if len(source) == 0 {
		p.logger.Warn("capabilities: allowed_operations is empty — publishing full catalog as fallback")
		for op := range operationCatalog {
			source = append(source, op)
		}
	}

	schemas := make([]protocol.OperationSchema, 0, len(source))
	execAllowed := false

	for _, op := range source {
		if op == "exec" {
			execAllowed = true
		}
		if schema, ok := operationCatalog[op]; ok {
			schemas = append(schemas, schema)
		}
	}

	payload := protocol.CapabilitiesPayload{
		Operations: schemas,
	}
	if execAllowed {
		payload.ExecCommands = p.cfg.AllowedCommands
	}

	env, err := protocol.New(p.agentUUID, protocol.TypeCapabilities, payload)
	if err != nil {
		p.logger.Error("capabilities: could not build envelope", zap.Error(err))
		return &payload
	}

	if p.nats == nil {
		return &payload
	}
	if err := p.nats.Publish(env); err != nil {
		p.logger.Error("capabilities: publish failed", zap.Error(err))
		return &payload
	}

	p.logger.Info("capabilities published",
		zap.Int("operation_count", len(schemas)),
		zap.Bool("exec_allowed", execAllowed),
	)
	return &payload
}

// telemetryPayload extends system.SystemMetrics with an optional "vms" key
// carrying XAPI RRD-derived per-VM utilisation — the shape docs/protocol.md
// anticipated for compute agents ("may include a vms key instead"). VMs is
// only populated when a xenserver.Module is configured and RRD is enabled.
type telemetryPayload struct {
	system.SystemMetrics
	VMs []xenserver.VMMetric `json:"vms,omitempty"`
}

func (p *Publisher) sendTelemetry(ctx context.Context) {
	metrics, err := p.sys.GetMetrics(ctx)
	if err != nil {
		p.logger.Warn("telemetry: could not get metrics", zap.Error(err))
		return
	}

	payload := telemetryPayload{SystemMetrics: *metrics}
	if p.xen != nil && p.xenCfg.RRDEnabled {
		if snapshot, err := p.xen.RRDUpdates(ctx, p.lastRRDEnd); err != nil {
			p.logger.Warn("telemetry: could not get xenserver rrd metrics", zap.Error(err))
		} else {
			payload.VMs = snapshot.VMs
			if snapshot.EndTime > 0 {
				p.lastRRDEnd = snapshot.EndTime + 1
			}
		}
	}

	env, err := protocol.New(p.agentUUID, protocol.TypeTelemetry, payload)
	if err != nil {
		p.logger.Warn("telemetry: could not build envelope", zap.Error(err))
		return
	}

	// Publish to platform evt subject.
	if err := p.nats.Publish(env); err != nil {
		p.logger.Warn("telemetry: publish to evt failed", zap.Error(err))
		return
	}

	// Also publish to the client-facing vm.{uuid}.telemetry subject (VM_TELEMETRY
	// JetStream stream, 15-minute retention). Requires the JWT to include publish
	// permission for this subject — a warning is logged if not yet granted.
	if err := p.nats.PublishToSubject(p.nats.TelemetrySubject(), env); err != nil {
		p.logger.Warn("telemetry: publish to vm telemetry subject failed",
			zap.String("subject", p.nats.TelemetrySubject()),
			zap.Error(err),
		)
	}

	p.logger.Debug("telemetry published")
}

// xapiEventsLoop long-polls XAPI's own event bus (event.from) and publishes
// each object-change event as a xapi_event message. Replaces the events.py
// sidecar script's session.xenapi.event_from loop. Runs until ctx is
// cancelled; a failed poll is logged and retried after the poll interval
// rather than aborting the loop.
func (p *Publisher) xapiEventsLoop(ctx context.Context) {
	poller, err := p.xen.NewEventPoller(ctx)
	if err != nil {
		p.logger.Warn("xapi events: could not start session, disabling event streaming", zap.Error(err))
		return
	}
	defer poller.Close(context.Background())

	interval := p.xenCfg.EventsPollInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	token := ""
	for {
		events, nextToken, err := poller.Poll(ctx, token, 30)
		if err != nil {
			p.logger.Warn("xapi events: poll failed, retrying", zap.Error(err))
		} else {
			token = nextToken
			for _, ev := range events {
				p.publishXAPIEvent(ev)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (p *Publisher) publishXAPIEvent(ev xenserver.XAPIEvent) {
	payload := protocol.XAPIEventPayload{
		Hostname:  p.hostname,
		Class:     ev.Class,
		UUID:      ev.ObjUUID,
		Operation: ev.Operation,
		Snapshot:  ev.Snapshot,
	}
	env, err := protocol.New(p.agentUUID, protocol.TypeXAPIEvent, payload)
	if err != nil {
		p.logger.Warn("xapi event: could not build envelope", zap.Error(err))
		return
	}
	if err := p.nats.Publish(env); err != nil {
		p.logger.Warn("xapi event: publish failed", zap.Error(err))
	}
}

// ipmiLoop periodically reads IPMI sensor data and publishes it as an ipmi
// event. Replaces the ipmi.py sidecar script's cron'd REST push.
func (p *Publisher) ipmiLoop(ctx context.Context) {
	interval := p.xenCfg.IPMIInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}

	p.sendIPMI(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendIPMI(ctx)
		}
	}
}

func (p *Publisher) sendIPMI(ctx context.Context) {
	sensors, err := p.xen.IPMISensors(ctx)
	if err != nil {
		p.logger.Warn("ipmi: could not read sensors", zap.Error(err))
		return
	}

	env, err := protocol.New(p.agentUUID, protocol.TypeIPMI, protocol.IPMIPayload{
		Hostname: p.hostname,
		Sensors:  sensors,
	})
	if err != nil {
		p.logger.Warn("ipmi: could not build envelope", zap.Error(err))
		return
	}
	if err := p.nats.Publish(env); err != nil {
		p.logger.Warn("ipmi: publish failed", zap.Error(err))
		return
	}
	p.logger.Debug("ipmi sensors published")
}

// SchemaFor returns the catalog schema of an operation.
func SchemaFor(op string) (protocol.OperationSchema, bool) {
	s, ok := operationCatalog[op]
	return s, ok
}
