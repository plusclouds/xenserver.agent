// Package dispatcher routes inbound command envelopes to module operations
// and returns result envelopes. Operations must be listed in the agent config's
// allowed_operations; unlisted or unknown operations are rejected without execution.
package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/executor"
	"github.com/plusclouds/ubuntu-agent/internal/jobs"
	"github.com/plusclouds/ubuntu-agent/internal/modules/nfsmount"
	"github.com/plusclouds/ubuntu-agent/internal/modules/openvswitch"
	"github.com/plusclouds/ubuntu-agent/internal/modules/services"
	"github.com/plusclouds/ubuntu-agent/internal/modules/system"
	"github.com/plusclouds/ubuntu-agent/internal/modules/xenserver"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
	"github.com/plusclouds/ubuntu-agent/internal/publisher"
)

// Dispatcher routes command envelopes to the appropriate module method.
type Dispatcher struct {
	sys                *system.Module
	svc                services.Manager
	exec               *executor.Executor
	xen                *xenserver.Module
	ovs                *openvswitch.Module
	nfs                *nfsmount.Module
	pub                *publisher.Publisher
	allowedOps         map[string]bool
	allowedCommands    []string
	agentUUID          string
	logger             *zap.Logger
	jobs               *jobs.Manager
	dryRun             bool
	callbackPrefixes   []string
	callbackClient     *http.Client
	callbackRetryDelay time.Duration
}

// SetJobs enables background execution (params.async) and the agent.job.*
// operations. Without it, async requests are rejected.
func (d *Dispatcher) SetJobs(m *jobs.Manager) { d.jobs = m }

// SetDryRun makes every operation (except the agent.* meta operations) be
// authorised and checked against its schema but not executed.
func (d *Dispatcher) SetDryRun(v bool) { d.dryRun = v }

// dryRunReply validates a command the way a real run would (allowlist already
// passed; required parameters; confirm for destructive operations) and
// describes what would have happened, without touching the host.
func (d *Dispatcher) dryRunReply(env protocol.Envelope, op string, raw json.RawMessage, p params, start time.Time) protocol.Envelope {
	var given map[string]json.RawMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &given)
	}
	var missing []string
	if schema, ok := publisher.SchemaFor(op); ok {
		for _, sp := range schema.Params {
			if !sp.Required {
				continue
			}
			if _, has := given[sp.Name]; !has {
				missing = append(missing, sp.Name)
			}
		}
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf("dry run: missing required parameter(s): %s", strings.Join(missing, ", "))
		d.logResult(env.ID, op, protocol.StatusRejected, msg, time.Since(start))
		return d.reject(env, msg)
	}
	if schema, ok := publisher.SchemaFor(op); ok {
		for _, sp := range schema.Params {
			if sp.Name == "confirm" && sp.Required && !p.Confirm {
				msg := "dry run: this operation is destructive and requires confirm=true"
				d.logResult(env.ID, op, protocol.StatusRejected, msg, time.Since(start))
				return d.reject(env, msg)
			}
		}
	}
	if op == "exec" && !slices.Contains(d.allowedCommands, p.Command) {
		msg := fmt.Sprintf("dry run: command %q is not in allowed_commands", p.Command)
		d.logResult(env.ID, op, protocol.StatusFailed, msg, time.Since(start))
		return d.fail(env, msg)
	}
	d.logResult(env.ID, op, protocol.StatusCompleted, "dry run", time.Since(start))
	return d.ok(env, map[string]any{
		"dry_run":   true,
		"operation": op,
		"executed":  false,
		"message":   "Dry run: the operation is permitted and its parameters are valid; nothing was executed.",
		"params":    json.RawMessage(redactParams(raw)),
	})
}

// asyncOps are the long-running operations that may be run in the background.
var asyncOps = map[string]bool{
	"xenserver.vm.snapshot_export": true,
	"xenserver.vm.export":          true,
	"xenserver.vm.import":          true,
	"xenserver.vdi.copy":           true,
	"xenserver.vdi.clone":          true,
	"xenserver.vdi.export":         true,
	"xenserver.vm.clone":           true,
	"xenserver.vm.create":          true,
	"xenserver.sr.scan":            true,
}

// New creates a Dispatcher. allowedOps and allowedCommands come from agent config.
func New(
	sys *system.Module,
	svc services.Manager,
	exec *executor.Executor,
	xen *xenserver.Module,
	ovs *openvswitch.Module,
	nfs *nfsmount.Module,
	pub *publisher.Publisher,
	agentUUID string,
	allowedOps []string,
	allowedCommands []string,
	logger *zap.Logger,
) *Dispatcher {
	ops := make(map[string]bool, len(allowedOps))
	for _, op := range allowedOps {
		ops[op] = true
	}
	return &Dispatcher{
		sys:             sys,
		svc:             svc,
		exec:            exec,
		xen:             xen,
		ovs:             ovs,
		nfs:             nfs,
		pub:             pub,
		allowedOps:      ops,
		allowedCommands: allowedCommands,
		agentUUID:       agentUUID,
		logger:          logger,
	}
}

// params is the common command params shape. Fields are optional depending on operation.
type params struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	IntervalS int      `json:"interval_s"`

	// xenserver.* / openvswitch.* / nfsmount.* fields. UUID is overloaded
	// per-operation (VM uuid, SR uuid, snapshot uuid to delete, etc.) —
	// see the run() case for each operation's exact meaning.
	UUID          string            `json:"uuid"`
	Label         string            `json:"label"`
	Type          string            `json:"type"`
	ContentType   string            `json:"content_type"`
	Shared        bool              `json:"shared"`
	HostUUID      string            `json:"host_uuid"`
	DeviceConfig  map[string]string `json:"device_config"`
	VIFUUID       string            `json:"vif_uuid"`
	Device        string            `json:"device"`
	LockingMode   string            `json:"locking_mode"`
	IPv4Allowed   []string          `json:"ipv4_allowed"`
	IPv6Allowed   []string          `json:"ipv6_allowed"`
	Filename      string            `json:"filename"`
	SRUUID        string            `json:"sr_uuid"`
	Preserve      bool              `json:"preserve"`
	Compress      bool              `json:"compress"`
	VDIUUID       string            `json:"vdi_uuid"`
	Format        string            `json:"format"`
	Bridge        string            `json:"bridge"`
	NetworkUUID   string            `json:"network_uuid"`
	NetworkName   string            `json:"network_name"`
	PortName      string            `json:"port_name"`
	RemoteIP      string            `json:"remote_ip"`
	LocalIP       string            `json:"local_ip"`
	VNI           int               `json:"vni"`
	FlowSpec      string            `json:"flow_spec"`
	MatchSpec     string            `json:"match_spec"`
	Server        string            `json:"server"`
	ExportPath    string            `json:"export_path"`
	MountPoint    string            `json:"mount_point"`
	MountOptions  string            `json:"mount_options"`
	Async         bool              `json:"async"`
	NewName       string            `json:"new_name"`
	Set           map[string]string `json:"set"`
	Unset         []string          `json:"unset"`
	Param         string            `json:"param"`
	ParamKey      string            `json:"param_key"`
	StaticMin     int64             `json:"static_min"`
	StaticMax     int64             `json:"static_max"`
	DynamicMin    int64             `json:"dynamic_min"`
	DynamicMax    int64             `json:"dynamic_max"`
	Template      string            `json:"template"`
	Description   string            `json:"description"`
	Confirm       bool              `json:"confirm"`
	IfNotExists   bool              `json:"if_not_exists"`
	AllowAttached bool              `json:"allow_attached"`
	Units         []string          `json:"units"`
	Since         int64             `json:"since"`
	MemoryBytes   int64             `json:"memory_bytes"`
	VBDUUID       string            `json:"vbd_uuid"`
	Mode          string            `json:"mode"`
	Bootable      bool              `json:"bootable"`
	Sharable      bool              `json:"sharable"`
	ReadOnly      bool              `json:"read_only"`
	CDName        string            `json:"cd_name"`
	NameLabel     string            `json:"name_label"`
	IsControlDom  *bool             `json:"is_control_domain"`
	IsATemplate   *bool             `json:"is_a_template"`
	IncludeVMs    bool              `json:"include_vms"`
	Callback      *callbackParam    `json:"callback"`
	Files         map[string]string `json:"files"`
	VolumeLabel   string            `json:"volume_label"`
	Overwrite     bool              `json:"overwrite"`
	Fields        []string          `json:"fields"`
	PIFUUID       string            `json:"pif_uuid"`
	Tag           int               `json:"tag"`
	MAC           string            `json:"mac"`
	MTU           int               `json:"mtu"`
	VLAN          *int              `json:"vlan"`
	JobID         string            `json:"job_id"`
	Suffix        string            `json:"suffix"`
	SortBy        string            `json:"sort_by"`
	Descending    bool              `json:"descending"`
	Limit         int               `json:"limit"`
	SizeBytes     int64             `json:"size_bytes"`
}

// Dispatch handles a single command envelope and returns the result envelope.
// It always returns a valid result — errors become failed/rejected statuses.
func (d *Dispatcher) Dispatch(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	start := time.Now()

	if _, err := protocol.NormalizeVersion(env.V); err != nil {
		d.logger.Warn("command rejected: unsupported protocol version",
			zap.String("command_id", env.ID), zap.Int("v", env.V))
		return d.reject(env, err.Error())
	}

	var cmd protocol.CommandPayload
	if err := env.DecodePayload(&cmd); err != nil {
		d.logger.Error("→ command received: could not decode payload",
			zap.String("command_id", env.ID),
			zap.Error(err),
		)
		return d.reject(env, "could not decode command payload: "+err.Error())
	}

	op := cmd.Operation

	d.logger.Info("→ command received",
		zap.String("command_id", env.ID),
		zap.String("operation", op),
	)
	d.logger.Debug("→ command params",
		zap.String("command_id", env.ID),
		zap.String("operation", op),
		zap.ByteString("params", redactParams(cmd.Params)),
	)

	if !d.allowedOps[op] {
		result := d.reject(env, fmt.Sprintf("operation %q is not permitted on this agent", op))
		d.logResult(env.ID, op, protocol.StatusRejected, "operation not in allowed_operations", time.Since(start))
		return result
	}

	var p params
	if len(cmd.Params) > 0 && !emptyParams(cmd.Params) {
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			result := d.reject(env, "could not decode params: "+err.Error())
			d.logResult(env.ID, op, protocol.StatusRejected, "could not decode params: "+err.Error(), time.Since(start))
			return result
		}
	}

	if d.dryRun && !strings.HasPrefix(op, "agent.") {
		return d.dryRunReply(env, op, cmd.Params, p, start)
	}

	if p.Async {
		return d.startAsync(env, op, p, start)
	}

	output, err := d.run(ctx, op, p)
	if err != nil {
		result := d.fail(env, err.Error())
		d.logResult(env.ID, op, protocol.StatusFailed, err.Error(), time.Since(start))
		return result
	}

	d.logger.Debug("← command output",
		zap.String("command_id", env.ID),
		zap.String("operation", op),
		zap.Any("output", output),
	)
	result := d.ok(env, output)
	d.logResult(env.ID, op, protocol.StatusCompleted, "", time.Since(start))
	return result
}

// startAsync runs op in the background and replies at once with the job.
func (d *Dispatcher) startAsync(env protocol.Envelope, op string, p params, start time.Time) protocol.Envelope {
	if !asyncOps[op] {
		d.logResult(env.ID, op, protocol.StatusRejected, "async not supported", time.Since(start))
		return d.reject(env, fmt.Sprintf("operation %q does not support async", op))
	}
	if d.jobs == nil {
		d.logResult(env.ID, op, protocol.StatusRejected, "jobs disabled", time.Since(start))
		return d.reject(env, "background jobs are not enabled on this agent")
	}
	if err := d.checkCallback(p.Callback); err != nil {
		d.logResult(env.ID, op, protocol.StatusRejected, err.Error(), time.Since(start))
		return d.reject(env, err.Error())
	}
	var onFinish func(jobs.Job)
	if p.Callback != nil {
		cb := *p.Callback
		onFinish = func(j jobs.Job) { d.sendCallback(cb, j) }
	}
	job, err := d.jobs.StartWith(op, func(ctx context.Context) (any, bool, string, error) {
		out, err := d.run(ctx, op, p)
		if err != nil {
			return nil, false, "", err
		}
		failed, msg := logicalFailure(out)
		return out, failed, msg, nil
	}, onFinish)
	if err != nil {
		d.logResult(env.ID, op, protocol.StatusFailed, err.Error(), time.Since(start))
		return d.fail(env, err.Error())
	}
	d.logger.Info("background job started", zap.String("job_id", job.ID), zap.String("operation", op))
	res, rerr := protocol.ResultFor(env, d.agentUUID, protocol.StatusAccepted,
		"started in the background; poll agent.job.status with this job_id", job)
	if rerr != nil {
		d.logger.Error("failed to build result envelope", zap.Error(rerr))
	}
	d.logResult(env.ID, op, protocol.StatusAccepted, "", time.Since(start))
	return res
}

// logicalFailure reports whether an operation output says success=false
// (modules return that, with a nil error, when the underlying tool failed).
func logicalFailure(out any) (bool, string) {
	raw, err := json.Marshal(out)
	if err != nil {
		return false, ""
	}
	var probe struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Success == nil || *probe.Success {
		return false, ""
	}
	return true, probe.Message
}

// logResult emits the summary log line for a completed dispatch.
func (d *Dispatcher) logResult(commandID, op, status, msg string, elapsed time.Duration) {
	fields := []zap.Field{
		zap.String("command_id", commandID),
		zap.String("operation", op),
		zap.String("status", status),
		zap.Duration("elapsed", elapsed),
	}
	if msg != "" {
		fields = append(fields, zap.String("message", msg))
	}
	switch status {
	case protocol.StatusCompleted:
		d.logger.Info("← result: completed", fields...)
	case protocol.StatusFailed:
		d.logger.Error("← result: failed", fields...)
	case protocol.StatusRejected:
		d.logger.Warn("← result: rejected", fields...)
	}
}

// run executes the operation and returns the output or an error.
func (d *Dispatcher) run(ctx context.Context, op string, p params) (any, error) {
	switch op {
	// ---- system -------------------------------------------------------
	case "system.info":
		return d.sys.GetInfo(ctx)
	case "system.metrics":
		return d.sys.GetMetrics(ctx)
	case "system.cpu":
		return d.sys.GetCPU(ctx)
	case "system.memory":
		return d.sys.GetMemory(ctx)
	case "system.disk":
		return d.sys.GetDisk(ctx)
	case "system.network":
		return d.sys.GetNetwork(ctx)

	// ---- services -----------------------------------------------------
	case "services.list":
		return d.svc.List(ctx)
	case "services.get":
		return d.svc.Get(ctx, p.Name)
	case "services.start":
		return d.svc.Start(ctx, p.Name)
	case "services.stop":
		return d.svc.Stop(ctx, p.Name)
	case "services.restart":
		return d.svc.Restart(ctx, p.Name)
	case "services.reload":
		return d.svc.Reload(ctx, p.Name)
	case "services.enable":
		return d.svc.Enable(ctx, p.Name)
	case "services.disable":
		return d.svc.Disable(ctx, p.Name)

	// ---- vm -----------------------------------------------------------
	case "vm.reboot":
		var stdout, stderr string
		var execErr error
		if runtime.GOOS == "windows" {
			stdout, stderr, execErr = d.exec.Execute(ctx, "shutdown", "/r", "/t", "0")
		} else {
			stdout, stderr, execErr = d.exec.Execute(ctx, "systemctl", "reboot")
		}
		return map[string]string{"stdout": stdout, "stderr": stderr}, execErr
	case "vm.shutdown":
		var stdout, stderr string
		var execErr error
		if runtime.GOOS == "windows" {
			stdout, stderr, execErr = d.exec.Execute(ctx, "shutdown", "/s", "/t", "0")
		} else {
			stdout, stderr, execErr = d.exec.Execute(ctx, "systemctl", "poweroff")
		}
		return map[string]string{"stdout": stdout, "stderr": stderr}, execErr

	// ---- system.update ------------------------------------------------
	case "system.update":
		distro, err := detectDebianDistro()
		if err != nil {
			return nil, fmt.Errorf("could not detect OS: %w", err)
		}
		if distro == "" {
			return nil, fmt.Errorf("system.update is only supported on Ubuntu/Debian; detected OS is not apt-based")
		}

		// Run apt-get update first, then upgrade. Stop on first failure.
		updateOut, updateErr, err := d.exec.Execute(ctx,
			"apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef",
			"-o", "Dpkg::Options::=--force-confold", "update")
		if err != nil {
			return map[string]any{
				"step":   "apt-get update",
				"stdout": updateOut,
				"stderr": updateErr,
			}, fmt.Errorf("apt-get update failed: %w", err)
		}

		upgradeOut, upgradeErr, err := d.exec.Execute(ctx,
			"apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef",
			"-o", "Dpkg::Options::=--force-confold", "upgrade")
		return map[string]any{
			"distro":         distro,
			"update_stdout":  updateOut,
			"update_stderr":  updateErr,
			"upgrade_stdout": upgradeOut,
			"upgrade_stderr": upgradeErr,
		}, err

	// ---- agent --------------------------------------------------------
	case "agent.job.status":
		if d.jobs == nil {
			return nil, fmt.Errorf("background jobs are not enabled on this agent")
		}
		if p.JobID == "" {
			return nil, fmt.Errorf("job_id is required")
		}
		j, ok := d.jobs.Get(p.JobID)
		if !ok {
			return nil, fmt.Errorf("no job with id %s (jobs are forgotten when the agent restarts)", p.JobID)
		}
		return j, nil
	case "agent.job.list":
		if d.jobs == nil {
			return nil, fmt.Errorf("background jobs are not enabled on this agent")
		}
		return d.jobs.List(), nil
	case "agent.job.cancel":
		if d.jobs == nil {
			return nil, fmt.Errorf("background jobs are not enabled on this agent")
		}
		if p.JobID == "" {
			return nil, fmt.Errorf("job_id is required")
		}
		if !d.jobs.Cancel(p.JobID) {
			return nil, fmt.Errorf("job %s is unknown or already finished", p.JobID)
		}
		return map[string]string{"job_id": p.JobID, "status": "cancelling"}, nil
	case "agent.allowed_operations":
		payload, err := d.pub.SendCapabilities(ctx)
		if err != nil {
			return nil, fmt.Errorf("re-publishing capabilities: %w", err)
		}
		return payload, nil

	// ---- telemetry ----------------------------------------------------
	case "telemetry.set_interval":
		if p.IntervalS <= 0 {
			return nil, fmt.Errorf("interval_s must be a positive integer (seconds)")
		}
		requested := time.Duration(p.IntervalS) * time.Second
		applied := d.pub.SetTelemetryInterval(requested)
		return map[string]any{
			"requested_interval_s": p.IntervalS,
			"applied_interval_s":   int(applied.Seconds()),
		}, nil

	// ---- exec ---------------------------------------------------------
	case "exec":
		if !slices.Contains(d.allowedCommands, p.Command) {
			return nil, fmt.Errorf("command %q is not in allowed_commands", p.Command)
		}
		stdout, stderr, err := d.exec.Execute(ctx, p.Command, p.Args...)
		return map[string]string{"stdout": stdout, "stderr": stderr}, err

	// ---- xenserver: read-only ------------------------------------------
	case "xenserver.pool.info":
		return d.xen.PoolInfo(ctx)
	case "xenserver.host.list":
		return d.xen.HostList(ctx)
	case "xenserver.host.info":
		return d.xen.HostInfo(ctx)
	case "xenserver.vm.list":
		if f := (xenserver.VMFilter{NameLabel: p.NameLabel, IsControlDomain: p.IsControlDom, IsATemplate: p.IsATemplate, Fields: p.Fields}); !f.Empty() {
			return d.xen.VMListFiltered(ctx, f)
		}
		return d.xen.VMList(ctx)
	case "xenserver.vm.get":
		return d.xen.VMGet(ctx, p.UUID, p.Name)
	case "xenserver.vm.snapshot_list":
		return d.xen.SnapshotList(ctx, p.UUID, p.Name)
	case "xenserver.sr.list":
		if p.Type != "" || p.NameLabel != "" || p.ContentType != "" {
			return d.xen.SRListFiltered(ctx, p.Type, p.NameLabel, p.ContentType)
		}
		return d.xen.SRList(ctx)
	case "xenserver.sr.get":
		return d.xen.SRGet(ctx, p.UUID, p.Name)
	case "xenserver.network.list":
		if p.NameLabel != "" || p.Bridge != "" || p.PIFUUID != "" {
			return d.xen.NetworkListFiltered(ctx, p.NameLabel, p.Bridge, p.PIFUUID)
		}
		return d.xen.NetworkList(ctx)
	case "xenserver.vif.list":
		return d.xen.VIFList(ctx, p.UUID, p.Name)
	case "xenserver.vm.disk_list":
		return d.xen.VMDiskList(ctx, p.UUID, p.Name)
	case "xenserver.vdi.list":
		return d.xen.VDIList(ctx, p.SRUUID)
	case "xenserver.ipmi.sensors":
		sensors, err := d.xen.IPMISensors(ctx)
		if err != nil {
			return nil, err
		}
		return xenserver.FilterIPMI(sensors, p.Units), nil
	case "xenserver.metrics.rrd":
		snap, err := d.xen.RRDUpdates(ctx, p.Since)
		if err != nil {
			return nil, err
		}
		return xenserver.FilterRRD(snap, p.UUID), nil

	// ---- xenserver: state-changing --------------------------------------
	case "xenserver.vm.start":
		return d.xen.VMStart(ctx, p.UUID, p.Name)
	case "xenserver.vm.shutdown":
		return d.xen.VMShutdown(ctx, p.UUID, p.Name, false)
	case "xenserver.vm.reboot":
		return d.xen.VMReboot(ctx, p.UUID, p.Name, false)
	case "xenserver.vm.force_shutdown":
		return d.xen.VMShutdown(ctx, p.UUID, p.Name, true)
	case "xenserver.vm.force_reboot":
		return d.xen.VMReboot(ctx, p.UUID, p.Name, true)
	case "xenserver.vm.suspend":
		return d.xen.VMSuspend(ctx, p.UUID, p.Name)
	case "xenserver.vm.resume":
		return d.xen.VMResume(ctx, p.UUID, p.Name)
	case "xenserver.vm.snapshot_create":
		return d.xen.SnapshotCreate(ctx, p.UUID, p.Name, p.Label)
	case "xenserver.vm.param_set":
		return d.xen.VMParamSet(ctx, p.UUID, p.Name, p.Set, p.Unset)
	case "xenserver.vm.param_get":
		return d.xen.VMParamGet(ctx, p.UUID, p.Name, p.Param, p.ParamKey)
	case "xenserver.vm.param_list":
		return d.xen.VMParamList(ctx, p.UUID, p.Name)
	case "xenserver.vm.memory_limits_set":
		if p.MemoryBytes > 0 {
			p.StaticMin, p.StaticMax, p.DynamicMin, p.DynamicMax = p.MemoryBytes, p.MemoryBytes, p.MemoryBytes, p.MemoryBytes
		}
		return d.xen.VMMemoryLimitsSet(ctx, p.UUID, p.Name, p.StaticMin, p.StaticMax, p.DynamicMin, p.DynamicMax)
	case "xenserver.vm.console_list":
		return d.xen.VMConsoleList(ctx, p.UUID, p.Name)
	case "xenserver.vm.create":
		return d.xen.VMCreate(ctx, p.NewName, p.Description, p.Template, p.SRUUID)
	case "xenserver.vm.clone":
		return d.xen.VMClone(ctx, p.UUID, p.Name, p.NewName, p.Description)
	case "xenserver.vm.destroy":
		return d.xen.VMDestroy(ctx, p.UUID, p.Name, p.Confirm)
	case "xenserver.vm.pause":
		return d.xen.VMPause(ctx, p.UUID, p.Name)
	case "xenserver.vm.unpause":
		return d.xen.VMUnpause(ctx, p.UUID, p.Name)
	case "xenserver.vdi.create":
		return d.xen.VDICreate(ctx, p.SRUUID, p.NewName, p.Description, p.SizeBytes, p.Type, p.Sharable, p.ReadOnly)
	case "xenserver.vdi.destroy":
		return d.xen.VDIDestroy(ctx, p.VDIUUID, p.Confirm, p.AllowAttached)
	case "xenserver.vdi.param_list":
		return d.xen.VDIParamList(ctx, p.VDIUUID)
	case "xenserver.vbd.create":
		return d.xen.VBDCreate(ctx, p.UUID, p.Name, p.VDIUUID, p.Device, p.Mode, p.Type, p.Bootable)
	case "xenserver.vbd.plug":
		return d.xen.VBDPlug(ctx, p.VBDUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vbd.unplug":
		return d.xen.VBDUnplug(ctx, p.VBDUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vbd.destroy":
		return d.xen.VBDDestroy(ctx, p.VBDUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vbd.param_list":
		return d.xen.VBDParamList(ctx, p.VBDUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vm.cd_list":
		return d.xen.VMCDList(ctx, p.UUID, p.Name)
	case "xenserver.vm.cd_add":
		return d.xen.VMCDAdd(ctx, p.UUID, p.Name, p.CDName, p.Device)
	case "xenserver.vm.cd_insert":
		return d.xen.VMCDInsert(ctx, p.UUID, p.Name, p.CDName)
	case "xenserver.vm.cd_eject":
		return d.xen.VMCDEject(ctx, p.UUID, p.Name)
	case "xenserver.vm.cd_remove":
		return d.xen.VMCDRemove(ctx, p.UUID, p.Name, p.CDName)
	case "xenserver.vif.create":
		return d.xen.VIFCreate(ctx, p.UUID, p.Name, p.Device, p.NetworkUUID, p.NetworkName, p.MAC, p.MTU)
	case "xenserver.vif.param_list":
		vif, err := d.xen.ResolveVIF(ctx, p.VIFUUID, p.UUID, p.Name, p.Device)
		if err != nil {
			return nil, err
		}
		return d.xen.ObjectParamList(ctx, "vif", vif)
	case "xenserver.vif.param_set":
		return d.xen.VIFParamSet(ctx, p.VIFUUID, p.UUID, p.Name, p.Device, p.Set)
	case "xenserver.network.create":
		return d.xen.NetworkCreate(ctx, p.NewName, p.Description, p.MTU)
	case "xenserver.network.destroy":
		return d.xen.NetworkDestroy(ctx, p.UUID, p.Confirm)
	case "xenserver.network.param_list":
		return d.xen.ObjectParamList(ctx, "network", p.UUID)
	case "xenserver.vlan.list":
		return d.xen.VLANList(ctx, p.Tag)
	case "xenserver.vlan.create":
		return d.xen.VLANCreate(ctx, p.PIFUUID, p.NetworkUUID, p.Tag)
	case "xenserver.vlan.destroy":
		return d.xen.VLANDestroy(ctx, p.UUID, p.Confirm)
	case "xenserver.vlan.param_list":
		return d.xen.ObjectParamList(ctx, "vlan", p.UUID)
	case "xenserver.pif.list":
		return d.xen.PIFList(ctx, p.VLAN, p.NetworkUUID, p.HostUUID)
	case "xenserver.pif.param_list":
		return d.xen.ObjectParamList(ctx, "pif", p.UUID)
	case "xenserver.pif.param_get":
		return d.xen.ObjectParamGet(ctx, "pif", p.UUID, p.Param, p.ParamKey)
	case "xenserver.pif.unplug":
		return d.xen.PIFUnplug(ctx, p.UUID, p.Confirm)
	case "xenserver.pbd.list":
		return d.xen.PBDList(ctx, p.SRUUID, p.HostUUID)
	case "xenserver.pbd.param_list":
		return d.xen.ObjectParamList(ctx, "pbd", p.UUID)
	case "xenserver.task.list":
		return d.xen.TaskList(ctx)
	case "xenserver.sr.scan":
		return d.xen.SRScan(ctx, p.UUID, p.Name)
	case "xenserver.sr.param_list":
		sr, err := d.xen.ResolveSR(ctx, p.UUID, p.Name)
		if err != nil {
			return nil, err
		}
		return d.xen.ObjectParamList(ctx, "sr", sr)
	case "xenserver.sr.param_get":
		sr, err := d.xen.ResolveSR(ctx, p.UUID, p.Name)
		if err != nil {
			return nil, err
		}
		return d.xen.ObjectParamGet(ctx, "sr", sr, p.Param, p.ParamKey)
	case "xenserver.iso.create":
		return d.xen.ISOCreate(ctx, p.SRUUID, p.Name, p.Files, p.VolumeLabel, p.Overwrite)
	case "xenserver.iso.remove":
		return d.xen.ISORemove(ctx, p.SRUUID, p.Name, p.Confirm)
	case "xenserver.inventory.snapshot":
		return d.xen.InventorySnapshot(ctx, p.IncludeVMs)
	case "xenserver.host.param_list":
		return d.xen.HostParamList(ctx, p.UUID)
	case "xenserver.vm.snapshot_promote":
		return d.xen.SnapshotPromote(ctx, p.UUID, p.NewName)
	case "xenserver.vm.promote_template":
		return d.xen.PromoteTemplate(ctx, p.UUID, p.Name, p.NewName)
	case "xenserver.vm.snapshot_export":
		return d.xen.SnapshotExport(ctx, p.UUID, p.Name, p.Filename)
	case "xenserver.vm.snapshot_delete":
		// p.UUID here is the snapshot's own uuid, not the parent VM's.
		return d.xen.SnapshotDelete(ctx, p.UUID)
	case "xenserver.sr.create":
		name := p.Name
		if name == "" {
			name = p.NameLabel
		}
		if p.IfNotExists {
			return d.xen.SRCreateIfMissing(ctx, name, p.Type, p.Shared, p.ContentType, p.HostUUID, p.DeviceConfig)
		}
		return d.xen.SRCreate(ctx, name, p.Type, p.Shared, p.ContentType, p.HostUUID, p.DeviceConfig)
	case "xenserver.sr.remove":
		return d.xen.SRRemove(ctx, p.UUID, p.Name)
	case "xenserver.sr.set_default":
		return d.xen.SRSetDefault(ctx, p.UUID, p.Name)
	case "xenserver.vif.set_network":
		return d.xen.VIFSetNetwork(ctx, p.VIFUUID, p.UUID, p.Name, p.Device, p.NetworkUUID, p.NetworkName)
	case "xenserver.vif.plug":
		return d.xen.VIFPlug(ctx, p.VIFUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vif.unplug":
		return d.xen.VIFUnplug(ctx, p.VIFUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vif.remove":
		return d.xen.VIFRemove(ctx, p.VIFUUID, p.UUID, p.Name, p.Device)
	case "xenserver.vif.set_acl":
		return d.xen.VIFSetACL(ctx, p.VIFUUID, p.UUID, p.Name, p.Device, p.LockingMode, p.IPv4Allowed, p.IPv6Allowed)
	case "xenserver.vm.import":
		return d.xen.VMImport(ctx, p.Filename, p.SRUUID, p.Preserve, p.NewName)
	case "xenserver.vm.export":
		return d.xen.VMExport(ctx, p.UUID, p.Name, p.Filename, p.Compress)
	case "xenserver.vdi.copy":
		return d.xen.VDICopy(ctx, p.VDIUUID, p.SRUUID)
	case "xenserver.vdi.resize":
		return d.xen.VDIResize(ctx, p.VDIUUID, p.UUID, p.Name, p.Device, p.SizeBytes)
	case "xenserver.vdi.clone":
		return d.xen.VDIClone(ctx, p.VDIUUID)
	case "xenserver.vdi.export":
		return d.xen.VDIExport(ctx, p.VDIUUID, p.Filename, p.Format)

	// ---- openvswitch: read-only ------------------------------------------
	case "openvswitch.bridge.list":
		return d.ovs.BridgeList(ctx)
	case "openvswitch.port.list":
		return d.ovs.PortList(ctx, p.Bridge)
	case "openvswitch.flow.list":
		return d.ovs.FlowList(ctx, p.Bridge)

	// ---- openvswitch: state-changing -------------------------------------
	case "openvswitch.vxlan.create":
		return d.ovs.VXLANCreate(ctx, p.Bridge, p.NetworkUUID, p.PortName, p.RemoteIP, p.LocalIP, p.VNI)
	case "openvswitch.vxlan.remove":
		return d.ovs.VXLANRemove(ctx, p.Bridge, p.PortName)
	case "openvswitch.flow.add":
		return d.ovs.FlowAdd(ctx, p.Bridge, p.FlowSpec)
	case "openvswitch.flow.remove":
		return d.ovs.FlowRemove(ctx, p.Bridge, p.MatchSpec)

	// ---- nfsmount ---------------------------------------------------------
	case "nfsmount.list":
		return d.nfs.List(ctx)
	case "nfsmount.list_files":
		return d.nfs.ListFiles(ctx, p.MountPoint, p.Suffix, p.SortBy, p.Descending, p.Limit)
	case "nfsmount.mount":
		return d.nfs.Mount(ctx, p.Server, p.ExportPath, p.MountPoint, p.MountOptions)
	case "nfsmount.unmount":
		return d.nfs.Unmount(ctx, p.MountPoint)

	default:
		return nil, fmt.Errorf("unknown operation %q", op)
	}
}

// --- result helpers -----------------------------------------------------------

func (d *Dispatcher) ok(src protocol.Envelope, output any) protocol.Envelope {
	env, err := protocol.ResultFor(src, d.agentUUID, protocol.StatusCompleted, "", output)
	if err != nil {
		d.logger.Error("failed to build result envelope", zap.Error(err))
	}
	return env
}

func (d *Dispatcher) fail(src protocol.Envelope, msg string) protocol.Envelope {
	env, err := protocol.ResultFor(src, d.agentUUID, protocol.StatusFailed, msg, nil)
	if err != nil {
		d.logger.Error("failed to build result envelope", zap.Error(err))
	}
	return env
}

func (d *Dispatcher) reject(src protocol.Envelope, msg string) protocol.Envelope {
	env, err := protocol.ResultFor(src, d.agentUUID, protocol.StatusRejected, msg, nil)
	if err != nil {
		d.logger.Error("failed to build result envelope", zap.Error(err))
	}
	return env
}

// detectDebianDistro reads /etc/os-release and returns the distro name if the
// system is Ubuntu or Debian (or a Debian derivative). Returns "" for other
// distros and an error only if the file cannot be read.
func detectDebianDistro() (string, error) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", fmt.Errorf("reading /etc/os-release: %w", err)
	}

	var id, idLike string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			id = strings.ToLower(strings.Trim(strings.TrimPrefix(line, "ID="), `"`))
		}
		if strings.HasPrefix(line, "ID_LIKE=") {
			idLike = strings.ToLower(strings.Trim(strings.TrimPrefix(line, "ID_LIKE="), `"`))
		}
	}

	if id == "ubuntu" || id == "debian" ||
		strings.Contains(idLike, "ubuntu") || strings.Contains(idLike, "debian") {
		if id != "" {
			return id, nil
		}
		return idLike, nil
	}
	return "", nil
}

// emptyParams reports whether raw JSON represents an absence of params.
// PHP serializes empty arrays as [] and some callers send null or {}.
// All three are treated as "no params provided".
func emptyParams(raw []byte) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "null", "[]", "{}", "":
		return true
	}
	return false
}
