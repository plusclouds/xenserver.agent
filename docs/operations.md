# Agent operations reference

Generated from the agent's own capability catalog (do not edit by hand; run
`GEN_API_DOC=1 go test ./internal/publisher -run TestGenerateAPIDoc`).

## Calling convention

```
POST https://<host>:8443/v1/command
Authorization: Bearer <http.api_key>
Content-Type: application/json

{"v":1,"id":"<your id>","type":"command",
 "payload":{"operation":"xenserver.vm.start","params":{"uuid":"..."}}}
```

Reply: an envelope whose `payload` is
`{command_id, status, message?, output?}`.

- `status`: `completed` (ran), `failed` (bad parameters or error before running), `rejected` (operation not in the host's allowed_operations, or async not supported), `accepted` (background job started; HTTP 202).
- State-changing operations return `output` = `{operation, target, success, message, output?, data?, detail?, diagnostics?}`. **Check `output.success`**: `status: completed` only means the agent ran the command. `data` holds structured results (for example `{"uuid":"<new object>"}`). On failure `message` is a plain-language explanation and `detail` has `{command, exit_code, error_code, stderr, stdout}`.
- Read operations return the JSON directly in `output` (an array of objects, or one object).
- Long operations accept `"async": true`: the call returns `202` with `output = {job_id, status:"running", ...}`. Poll `agent.job.status {job_id}`; when finished, `output.result` holds the operation's normal output. `agent.job.list` and `agent.job.cancel` also exist.
- Every operation must be listed in the host's `allowed_operations`. Destructive ones (`*.destroy`, `unplug`) also need `confirm: true`.
- `uuid`/`name` selectors: most VM operations take `uuid` or `name` (name must match exactly one VM).

118 operations.

### `agent.allowed_operations`

Return and re-publish the current capabilities list (allowed operations and parameters).

_No parameters._

### `agent.job.cancel`

Cancel a running background job. A half-written export file is removed.

| param | type | required | description |
|---|---|---|---|
| `job_id` | string | **yes** | The job to cancel. |

### `agent.job.list`

List background jobs (running and recently finished), newest first. Jobs are forgotten when the agent restarts.

_No parameters._

### `agent.job.status`

Status and progress of a background job started with params.async=true: running/completed/failed/cancelled, elapsed time, progress (bytes_done, estimated bytes_total, percent) and, once finished, the operation's own result.

| param | type | required | description |
|---|---|---|---|
| `job_id` | string | **yes** | The job_id returned when the operation was started. |

### `exec`

Execute an allowed binary on the machine. The command must be listed in exec_commands.

| param | type | required | description |
|---|---|---|---|
| `command` | string | **yes** | Absolute path of the binary to run. Must be in the exec_commands allowlist. |
| `args` | array | no | List of arguments to pass to the binary. |

### `nfsmount.list`

List every currently-mounted NFS filesystem.

_No parameters._

### `nfsmount.list_files`

List regular files directly under a mounted NFS mount point (e.g. an image repository), without a generic exec. Not recursive; mount_point must be a currently-mounted NFS mount.

| param | type | required | description |
|---|---|---|---|
| `mount_point` | string | **yes** | A currently-mounted NFS mount point, as shown by nfsmount.list. |
| `suffix` | string | no | Only names ending with this (case-insensitive), e.g. .xva or .pvm. |
| `sort_by` | string | no | name (default), size or mtime. |
| `descending` | boolean | no | Reverse the sort order. |
| `limit` | integer | no | Return at most this many files. |

### `nfsmount.mount`

Mount an NFS export, typically to stage a VM import/export file. Mount points are not confined to a base directory.

| param | type | required | description |
|---|---|---|---|
| `server` | string | **yes** | NFS server address. |
| `export_path` | string | **yes** | Remote export path. |
| `mount_point` | string | **yes** | Local directory to mount at (created if missing). |
| `mount_options` | string | no | Extra mount -o options. |

### `nfsmount.unmount`

Unmount a previously mounted NFS filesystem.

| param | type | required | description |
|---|---|---|---|
| `mount_point` | string | **yes** | Local mount point to unmount. |

### `openvswitch.bridge.list`

List every OVS bridge on the host.

_No parameters._

### `openvswitch.flow.add`

Install a raw OVS flow rule on a bridge.

| param | type | required | description |
|---|---|---|---|
| `bridge` | string | **yes** | OVS bridge name. |
| `flow_spec` | string | **yes** | ovs-ofctl flow syntax, e.g. "priority=100,ip,nw_src=10.0.0.0/24,actions=drop". |

### `openvswitch.flow.list`

Dump the raw OpenFlow flow table for a bridge.

| param | type | required | description |
|---|---|---|---|
| `bridge` | string | **yes** | OVS bridge name. |

### `openvswitch.flow.remove`

Remove OVS flow rules matching a spec on a bridge.

| param | type | required | description |
|---|---|---|---|
| `bridge` | string | **yes** | OVS bridge name. |
| `match_spec` | string | **yes** | ovs-ofctl match syntax for the flows to remove. |

### `openvswitch.port.list`

List every port on a bridge.

| param | type | required | description |
|---|---|---|---|
| `bridge` | string | **yes** | OVS bridge name. |

### `openvswitch.vxlan.create`

Add a VXLAN encapsulation port to a bridge.

| param | type | required | description |
|---|---|---|---|
| `bridge` | string | no | OVS bridge name. Either bridge or network_uuid is required. |
| `network_uuid` | string | no | xapi network uuid; its bridge is resolved automatically (the durable, xapi-aware pattern). |
| `port_name` | string | **yes** | Name for the new VXLAN interface. |
| `remote_ip` | string | **yes** | Remote VTEP IP address. |
| `local_ip` | string | no | Local VTEP IP address. |
| `vni` | integer | no | VXLAN network identifier (key). |

### `openvswitch.vxlan.remove`

Remove a VXLAN port from a bridge.

| param | type | required | description |
|---|---|---|---|
| `bridge` | string | **yes** | OVS bridge name. |
| `port_name` | string | **yes** | VXLAN interface name to remove. |

### `services.disable`

Disable a service from starting automatically on boot.

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name to disable. |

### `services.enable`

Enable a service to start automatically on boot.

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name to enable. |

### `services.get`

Get detailed status of a single systemd service.

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name (e.g. nginx or nginx.service). |

### `services.list`

List all loaded systemd services on the machine.

_No parameters._

### `services.reload`

Send a reload signal to a running service without stopping it.

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name to reload. |

### `services.restart`

Restart a systemd service (stop then start).

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name to restart. |

### `services.start`

Start a stopped systemd service.

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name to start. |

### `services.stop`

Stop a running systemd service.

| param | type | required | description |
|---|---|---|---|
| `name` | string | **yes** | Service name to stop. |

### `system.cpu`

Return CPU usage, core count, load averages, and per-core usage.

_No parameters._

### `system.disk`

Return disk usage and I/O stats for all real block-device partitions.

_No parameters._

### `system.info`

Return static system information (hostname, OS, kernel, uptime).

_No parameters._

### `system.memory`

Return RAM utilisation (total, used, usage %).

_No parameters._

### `system.metrics`

Return a full resource snapshot (CPU, memory, disk, network).

_No parameters._

### `system.network`

Return network I/O counters for all physical interfaces.

_No parameters._

### `system.update`

Run apt-get update && apt-get upgrade -y. Supported on Ubuntu/Debian only.

_No parameters._

### `telemetry.set_interval`

Change the telemetry push interval at runtime. Minimum 5 seconds.

| param | type | required | description |
|---|---|---|---|
| `interval_s` | integer | **yes** | New telemetry interval in seconds. |

### `vm.reboot`

Reboot the machine immediately.

_No parameters._

### `vm.shutdown`

Shut down the machine immediately.

_No parameters._

### `xenserver.host.info`

Return the detailed record for this host.

_No parameters._

### `xenserver.host.list`

List every host in the pool.

_No parameters._

### `xenserver.host.param_list`

All parameters of a host as JSON. Defaults to this host.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | Host uuid (default: this host). |

### `xenserver.ipmi.sensors`

Read the current IPMI sensor table (ipmitool sensor): name, reading, units, status and thresholds for temperatures, fans, voltages, currents, power and discrete states. Optional units filter, e.g. ["degrees C","RPM"]. Replaces the ipmi.py sidecar script.

| param | type | required | description |
|---|---|---|---|
| `units` | array | no | Only sensors with these units: "degrees C", "RPM", "Watts", "Volts", "Amps", "percent", "discrete". |

### `xenserver.metrics.rrd`

Return the latest XAPI RRD-derived utilisation snapshot: host metrics (cpu, memory, disk, network) and, per VM, cpu0..N, memory, memory_internal_free, vbd_*_iops/latency/throughput and vif_*_rx/tx. Give uuid to get only that VM (the host block is then omitted). Pass since (Unix time) to choose the window start; only its newest sample is returned, so poll with end_time+1 to build a series. Values are 60 s averages.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | Only this VM's metrics. |
| `since` | integer | no | Unix time window start (default: the last 3 minutes). |

### `xenserver.network.create`

Create an internal network (no physical interface). Returns data.uuid.

| param | type | required | description |
|---|---|---|---|
| `new_name` | string | **yes** | Name-label. |
| `description` | string | no | Name-description. |
| `mtu` | integer | no | MTU 68-9216. |

### `xenserver.network.destroy`

Delete a network. IRREVERSIBLE; requires confirm=true; refused while cards or interfaces use it.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | Network uuid. |
| `confirm` | boolean | **yes** | Must be true. |

### `xenserver.network.list`

List every xapi network (OVS bridge) in the pool.

| param | type | required | description |
|---|---|---|---|
| `name_label` | string | no | Only this name-label. |
| `bridge` | string | no | Only this bridge. |
| `pif_uuid` | string | no | Only the network of this interface. |

### `xenserver.network.param_list`

All parameters of a network as JSON.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | Network uuid. |

### `xenserver.pbd.list`

List SR attachments: uuid, sr_uuid, host_uuid, currently_attached, device_config (secrets masked).

| param | type | required | description |
|---|---|---|---|
| `sr_uuid` | string | no | Only this SR. |
| `host_uuid` | string | no | Only this host. |

### `xenserver.pbd.param_list`

All parameters of a PBD as JSON.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | PBD uuid. |

### `xenserver.pif.list`

List network interfaces: uuid, device, vlan (-1 untagged), network_uuid, host_uuid, mac, currently_attached, ip, netmask, gateway, management, physical, mtu, bond_slave_of, bond_master_of.

| param | type | required | description |
|---|---|---|---|
| `vlan` | integer | no | Only this VLAN tag (-1 = untagged interfaces). |
| `network_uuid` | string | no | Only this network. |
| `host_uuid` | string | no | Only this host. |

### `xenserver.pif.param_get`

Read one interface parameter or map key.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | Interface uuid. |
| `param` | string | **yes** | Parameter name. |
| `param_key` | string | no | Key within a map parameter. |

### `xenserver.pif.param_list`

All parameters of an interface as JSON.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | Interface uuid. |

### `xenserver.pif.unplug`

Take an interface down. Requires confirm=true; the management interface is always refused.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | Interface uuid. |
| `confirm` | boolean | **yes** | Must be true. |

### `xenserver.pool.info`

Return this host's XenServer/XCP-ng resource pool record.

_No parameters._

### `xenserver.sr.create`

Introduce a new storage repository (xe sr-create). Works for ISO repositories too: type=iso, content_type=iso, shared=true, device_config={"type":"nfs_iso","location":"<ip>:<path>","nfsversion":"4"}. With if_not_exists=true it does nothing when an SR with that name-label already exists (data.existing=true). Returns data.uuid.

| param | type | required | description |
|---|---|---|---|
| `name` | string | no | Name-label for the new SR. name_label is accepted as an alias; one of the two is required. |
| `name_label` | string | no | Alias of name. |
| `if_not_exists` | boolean | no | Skip creation (and succeed) when an SR with this name-label already exists. |
| `type` | string | **yes** | SR type, e.g. nfs, lvmoiscsi, ext, lvm. |
| `shared` | boolean | no | Whether the SR is shared across the pool. |
| `content_type` | string | no | SR content-type, e.g. user or iso. |
| `host_uuid` | string | no | Host to attach the SR to. Defaults to this host. |
| `device_config` | object | **yes** | SR-type-specific device-config key/value pairs (e.g. server, serverpath for NFS). |

### `xenserver.sr.get`

Return the detailed record for one storage repository.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | SR uuid. Either uuid or name is required. |
| `name` | string | no | SR name-label. Must resolve to exactly one SR. |

### `xenserver.sr.list`

List every storage repository visible to the pool.

| param | type | required | description |
|---|---|---|---|
| `type` | string | no | Only this SR type (nfs, iso, lvm, ...). |
| `name_label` | string | no | Only this name-label. |
| `content_type` | string | no | Only this content type (iso, user, ...). |

### `xenserver.sr.param_get`

Read one SR parameter (e.g. type) or map key.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | SR uuid. Either uuid or name is required. |
| `name` | string | no | SR name-label. |
| `param` | string | **yes** | Parameter name. |
| `param_key` | string | no | Key within a map parameter. |

### `xenserver.sr.param_list`

All parameters of an SR as JSON.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | SR uuid. Either uuid or name is required. |
| `name` | string | no | SR name-label. |

### `xenserver.sr.remove`

Safely detach an SR (unplug all PBDs, then forget the SR record). Data is preserved.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | SR uuid. Either uuid or name is required. |
| `name` | string | no | SR name-label. Must resolve to exactly one SR. |

### `xenserver.sr.scan`

Rescan an SR so new or removed disks appear. Can be slow; use async.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | SR uuid. Either uuid or name is required. |
| `name` | string | no | SR name-label. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.sr.set_default`

Make an SR the pool's primary storage (default-SR, suspend-image-SR and crash-dump-SR).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | SR uuid. Either uuid or name is required. |
| `name` | string | no | SR name-label. Must resolve to exactly one SR. |

### `xenserver.task.list`

List xapi tasks: uuid, name_label, status, progress, created, finished, result, error_info.

_No parameters._

### `xenserver.vbd.create`

Attach a disk to a VM (creates the VBD, unplugged; plug it with xenserver.vbd.plug if the VM runs). Returns data.uuid.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `vdi_uuid` | string | no | Disk to attach (required for type Disk). |
| `device` | string | no | Position ("0", "1", ...) or autodetect (default). |
| `mode` | string | no | RW (default) or RO. |
| `type` | string | no | Disk (default) or CD. |
| `bootable` | boolean | no | Mark bootable. |

### `xenserver.vbd.destroy`

Remove a disk's attachment to a VM; the disk and its data stay on the SR. The VBD must be unplugged or the VM halted.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `vbd_uuid` | string | no | VBD uuid. If omitted, uuid/name plus device are used. |
| `device` | string | no | Disk position ("0" = first), used with uuid/name when vbd_uuid is omitted. |

### `xenserver.vbd.param_list`

All parameters of a VBD as JSON (params, maps, sets).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `vbd_uuid` | string | no | VBD uuid. If omitted, uuid/name plus device are used. |
| `device` | string | no | Disk position ("0" = first), used with uuid/name when vbd_uuid is omitted. |

### `xenserver.vbd.plug`

Hot-attach a VBD to a running VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `vbd_uuid` | string | no | VBD uuid. If omitted, uuid/name plus device are used. |
| `device` | string | no | Disk position ("0" = first), used with uuid/name when vbd_uuid is omitted. |

### `xenserver.vbd.unplug`

Hot-detach a VBD from a running VM (guest must release the disk).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `vbd_uuid` | string | no | VBD uuid. If omitted, uuid/name plus device are used. |
| `device` | string | no | Disk position ("0" = first), used with uuid/name when vbd_uuid is omitted. |

### `xenserver.vdi.clone`

Create a fast copy-on-write clone of a VDI within the same SR.

| param | type | required | description |
|---|---|---|---|
| `vdi_uuid` | string | **yes** | VDI uuid to clone. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vdi.copy`

Copy a VDI's data to another (possibly different) SR.

| param | type | required | description |
|---|---|---|---|
| `vdi_uuid` | string | **yes** | Source VDI uuid. |
| `sr_uuid` | string | **yes** | Destination SR uuid. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vdi.create`

Create a virtual disk on an SR. Returns data.uuid. Attach it to a VM with xenserver.vbd.create.

| param | type | required | description |
|---|---|---|---|
| `sr_uuid` | string | **yes** | SR to create the disk on. |
| `new_name` | string | **yes** | Name-label of the disk. |
| `size_bytes` | integer | **yes** | Virtual size in bytes. |
| `description` | string | no | Name-description. |
| `type` | string | no | Disk type, default user. |
| `sharable` | boolean | no | Allow attaching to several VMs. |
| `read_only` | boolean | no | Create read-only. |

### `xenserver.vdi.destroy`

Permanently delete a disk and its data. IRREVERSIBLE; requires confirm=true. A disk attached to a VM is refused.

| param | type | required | description |
|---|---|---|---|
| `vdi_uuid` | string | **yes** | Disk to delete. |
| `confirm` | boolean | **yes** | Must be true. |
| `allow_attached` | boolean | no | Destroy even if the disk is still attached to a VM (default: refused; detach it first with vbd.destroy). |

### `xenserver.vdi.export`

Export a single VDI's raw disk content to a local file via xapi's HTTP export interface.

| param | type | required | description |
|---|---|---|---|
| `vdi_uuid` | string | **yes** | VDI uuid to export. |
| `filename` | string | **yes** | Destination path for the exported disk data. |
| `format` | string | no | Export format: vhd (default) or raw. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vdi.list`

List virtual disk images, optionally filtered to one SR.

| param | type | required | description |
|---|---|---|---|
| `sr_uuid` | string | no | Restrict the list to this SR uuid. |

### `xenserver.vdi.param_list`

All parameters of a disk as JSON: params (scalars), maps (sm-config, other-config, ... as objects), sets (arrays).

| param | type | required | description |
|---|---|---|---|
| `vdi_uuid` | string | **yes** | Disk uuid. |

### `xenserver.vdi.resize`

Grow a VDI to a new size (xapi refuses to shrink). Identify it by vdi_uuid, or by VM uuid/name plus the disk's position (device).

| param | type | required | description |
|---|---|---|---|
| `size_bytes` | integer | **yes** | New total virtual size in bytes. |
| `vdi_uuid` | string | no | VDI uuid. If omitted, uuid/name plus device are used. |
| `uuid` | string | no | VM uuid, used with device when vdi_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vdi_uuid is omitted. |
| `device` | string | no | Disk position (VBD userdevice), e.g. "0" for the first disk. |

### `xenserver.vif.create`

Add a network card to a VM on a network (uuid or name-label) at a device position. The VM should be halted. Returns data.uuid.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `device` | string | **yes** | Card position, e.g. "0". |
| `network_uuid` | string | no | Network uuid. Either network_uuid or network_name is required. |
| `network_name` | string | no | Network name-label. |
| `mac` | string | no | MAC address aa:bb:cc:dd:ee:ff (generated if omitted). |
| `mtu` | integer | no | MTU 68-9216. |

### `xenserver.vif.list`

List every virtual network interface attached to a VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vif.param_list`

All parameters of a network card as JSON (params, maps, sets).

| param | type | required | description |
|---|---|---|---|
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | Card position ("0", ...), used with uuid/name when vif_uuid is omitted. |

### `xenserver.vif.param_set`

Change allowlisted VIF parameters: qos_algorithm_type=ratelimit with qos_algorithm_params:kbps for rate limits, other-config:<key>, locking-mode, ipv4-allowed, ipv6-allowed, and MAC. A MAC change recreates the card (VM must be halted; the VIF uuid changes, see data.uuid).

| param | type | required | description |
|---|---|---|---|
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | Card position ("0", ...), used with uuid/name when vif_uuid is omitted. |
| `set` | object | **yes** | Map of parameter to value, e.g. {"qos_algorithm_type":"ratelimit","qos_algorithm_params:kbps":"10000"}. |

### `xenserver.vif.plug`

Hot-attach a network card to a running VM (needs guest tools). Create it first with xenserver.vif.create.

| param | type | required | description |
|---|---|---|---|
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | Card position ("0", ...), used with uuid/name when vif_uuid is omitted. |

### `xenserver.vif.remove`

Delete a VM's virtual network card (VIF). The VM must be halted or the VIF unplugged. The removed VIF's device, MAC and network are returned in output.

| param | type | required | description |
|---|---|---|---|
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used to resolve it. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | VIF device index (e.g. "5"), used with uuid/name when vif_uuid is omitted. |

### `xenserver.vif.set_acl`

Set xapi's native VIF anti-spoofing/allowlist ACL (locking-mode, ipv4-allowed, ipv6-allowed).

| param | type | required | description |
|---|---|---|---|
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used to resolve it. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | VIF device index (e.g. "0"), used with uuid/name when vif_uuid is omitted. |
| `locking_mode` | string | **yes** | One of: locked, unlocked, disabled. |
| `ipv4_allowed` | array | no | Allowed IPv4 addresses when locking_mode=locked. |
| `ipv6_allowed` | array | no | Allowed IPv6 addresses when locking_mode=locked. |

### `xenserver.vif.set_network`

Move a VM's network card to another network. xapi cannot re-point a VIF, so it is recreated with the same VM, device index, MAC, MTU and locking mode (restored on its old network if the recreate fails). The VM must be halted or the VIF unplugged. The new VIF uuid is returned in output; the VIF uuid changes.

| param | type | required | description |
|---|---|---|---|
| `network_uuid` | string | no | Target network uuid. Either network_uuid or network_name is required. |
| `network_name` | string | no | Target network name-label. Must resolve to exactly one network. |
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used to resolve it. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | VIF device index (e.g. "0"), used with uuid/name when vif_uuid is omitted. |

### `xenserver.vif.unplug`

Hot-detach a network card from a running VM (needs guest tools); the card stays defined and can be plugged again or removed with xenserver.vif.remove.

| param | type | required | description |
|---|---|---|---|
| `vif_uuid` | string | no | VIF uuid. If omitted, uuid/name plus device are used. |
| `uuid` | string | no | VM uuid, used with device when vif_uuid is omitted. |
| `name` | string | no | VM name-label, used with device when vif_uuid is omitted. |
| `device` | string | no | Card position ("0", ...), used with uuid/name when vif_uuid is omitted. |

### `xenserver.vlan.create`

Create a VLAN (tag 1-4094) on a physical interface, carried by a network (create it with network.create first). May interrupt traffic on the interface. Returns data.uuid (the VLAN, for vlan.destroy) and data.pif_uuid (the new tagged interface).

| param | type | required | description |
|---|---|---|---|
| `pif_uuid` | string | **yes** | Physical interface. |
| `network_uuid` | string | **yes** | Network that will carry the VLAN. |
| `tag` | integer | **yes** | VLAN tag. |

### `xenserver.vlan.destroy`

Delete a VLAN. IRREVERSIBLE; requires confirm=true.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | VLAN uuid. |
| `confirm` | boolean | **yes** | Must be true. |

### `xenserver.vlan.list`

List VLANs: uuid, tag, tagged_pif, untagged_pif. Optional tag filter.

| param | type | required | description |
|---|---|---|---|
| `tag` | integer | no | Only VLANs with this tag. |

### `xenserver.vlan.param_list`

All parameters of a VLAN as JSON.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | VLAN uuid. |

### `xenserver.vm.cd_add`

Add a CD drive holding an ISO from an ISO SR. device defaults to autodetect.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `cd_name` | string | **yes** | ISO name in an ISO SR. |
| `device` | string | no | Device position. |

### `xenserver.vm.cd_eject`

Eject the ISO from the VM's CD drive (the drive stays).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.cd_insert`

Insert an ISO into the VM's existing empty CD drive.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `cd_name` | string | **yes** | ISO name in an ISO SR. |

### `xenserver.vm.cd_list`

List a VM's CD/DVD drives: vbd_uuid, position, empty, iso_name, iso_uuid, currently_attached, mode.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.cd_remove`

Remove the CD drive that currently holds the named ISO. An empty drive cannot be found this way: remove it with xenserver.vbd.destroy and its device position (see xenserver.vm.cd_list).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `cd_name` | string | **yes** | ISO name in the drive. |

### `xenserver.vm.clone`

Clone a VM (copy-on-write where the SR supports it). The source must be halted or a snapshot. Slow on some SRs, so use async. Returns data.uuid.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `new_name` | string | **yes** | Name-label of the clone. |
| `description` | string | no | Name-description of the clone. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vm.console_list`

List a VM's consoles: uuid, protocol (vnc, rfb, vt100) and location URL.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.create`

Create a VM. With template (uuid or name-label) it installs a new VM from that template (xe vm-install, copying its disks to sr_uuid; slow, use async). Without template it creates an empty VM record to configure with vm.param_set. Returns data.uuid.

| param | type | required | description |
|---|---|---|---|
| `new_name` | string | **yes** | Name-label of the new VM. |
| `description` | string | no | Name-description. |
| `template` | string | no | Template uuid or name-label to install from. |
| `sr_uuid` | string | no | SR for the new VM's disks when installing from a template. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vm.destroy`

Delete a halted VM's record. IRREVERSIBLE; requires confirm=true. Its disks stay on the SR and must be removed separately. Control domains are refused.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `confirm` | boolean | **yes** | Must be true. |

### `xenserver.vm.disk_list`

List a VM's disks and CD drives by position (VBD userdevice; "0" is the first disk) with each VDI's uuid, name, size and SR. Use the position with xenserver.vdi.resize.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.export`

Export a whole VM (metadata + every attached VDI) to a local .xva file.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `filename` | string | **yes** | Destination path for the .xva file. |
| `compress` | boolean | no | Compress the export. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vm.force_reboot`

Hard reboot of a running VM (xe vm-reboot force=true). Does not need the guest's cooperation; the guest gets no chance to flush data.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.force_shutdown`

Hard power-off of a running VM (xe vm-shutdown force=true). Does not need the guest's cooperation; the guest gets no chance to flush data.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.get`

Return the detailed record for one VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.import`

Import a VM from a local .xva/.ova file (typically staged via nfsmount.mount).

| param | type | required | description |
|---|---|---|---|
| `filename` | string | **yes** | Path to the VM export file. |
| `sr_uuid` | string | no | Target SR for the imported VM's disks. |
| `preserve` | boolean | no | Preserve the original MAC addresses and uuids. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vm.list`

List every guest VM on the pool (control domains and templates excluded).

| param | type | required | description |
|---|---|---|---|
| `name_label` | string | no | Only VMs with this name-label. |
| `is_control_domain` | boolean | no | true lists control domains (dom0); setting any filter disables the default exclusion. |
| `is_a_template` | boolean | no | true lists templates. |
| `fields` | array | no | xe field names to return per VM (e.g. ["uuid","name-label","power-state"]); returns one object per VM with exactly those fields. |

### `xenserver.vm.memory_limits_set`

Set the memory limits in bytes on a halted VM. Give all four (static_min <= dynamic_min <= dynamic_max <= static_max) or just memory_bytes to set all four equal. On hosts with dynamic memory control (DMC) disabled, as here, static_max, dynamic_min and dynamic_max must be equal (static_min may be lower); memory_bytes is the simple way.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `memory_bytes` | integer | no | Set all four limits to this value (the simple choice when dynamic memory control is disabled). |
| `static_min` | integer | no | Minimum static memory, bytes. |
| `static_max` | integer | no | Maximum static memory, bytes. |
| `dynamic_min` | integer | no | Minimum dynamic memory, bytes. |
| `dynamic_max` | integer | no | Maximum dynamic memory, bytes. |

### `xenserver.vm.param_get`

Read one VM parameter, or one key of a map parameter (param=xenstore-data, param_key=<key>).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `param` | string | **yes** | Parameter name, e.g. name-label, power-state, VCPUs-max, platform. |
| `param_key` | string | no | Key within a map parameter. |

### `xenserver.vm.param_list`

Return every parameter of a VM as JSON: params (scalars), maps (xenstore-data, platform, other-config, ... as objects) and sets (allowed-operations, VBDs, VIFs, ... as arrays).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.param_set`

Set allowlisted VM parameters in one call: name-label, name-description, VCPUs-max, VCPUs-at-startup, and map entries platform:<key>, xenstore-data:<key>, other-config:<key>. Values are passed to xe as single arguments (spaces and quotes are safe); keys outside the allowlist are rejected and nothing is changed. VCPUs-max is applied before VCPUs-at-startup. Returns the list of applied changes.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `set` | object | no | Map of parameter to new value, e.g. {"name-label":"web1","xenstore-data:vm-data/hostname":"web1"}. |
| `unset` | array | no | Map entries to remove, e.g. ["xenstore-data:old-key"]. |

### `xenserver.vm.pause`

Pause a running VM (frozen in memory; unlike suspend nothing is written to disk).

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.promote_template`

Turn a template into an ordinary VM in place — the step that makes a restored snapshot export startable (importing one yields a template). Keeps disks and network cards; optionally renames.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | Template uuid, e.g. the one vm.import returned. Either uuid or name is required. |
| `name` | string | no | Template name-label. Must resolve to exactly one template. |
| `new_name` | string | no | New name-label for the resulting VM. |

### `xenserver.vm.reboot`

Perform a clean reboot of a running VM. Needs Xen guest tools in the VM; otherwise use xenserver.vm.force_reboot.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.resume`

Resume a suspended VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.shutdown`

Perform a clean ACPI shutdown of a running VM. Needs Xen guest tools in the VM; otherwise use xenserver.vm.force_shutdown.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.snapshot_create`

Take a new snapshot of a VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |
| `label` | string | **yes** | Name-label for the new snapshot. |

### `xenserver.vm.snapshot_delete`

Destroy a snapshot.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | The snapshot's own uuid (not the parent VM's uuid). |

### `xenserver.vm.snapshot_export`

Export a snapshot as a portable .xva image file (metadata plus all disks) — the first step of a backup. Refuses to overwrite an existing file; the target directory must exist. Reports size and duration.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | Snapshot uuid. Either uuid or name is required. |
| `name` | string | no | Snapshot name-label. Must resolve to exactly one snapshot. |
| `filename` | string | **yes** | Absolute path of the .xva file to create, e.g. on a mounted NFS repository. |
| `async` | boolean | no | Run in the background: reply immediately with a job_id and poll agent.job.status for progress. |

### `xenserver.vm.snapshot_list`

List every snapshot of a VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.snapshot_promote`

Turn a snapshot into an ordinary VM in place (snapshot-param-set is-a-template=false). The uuid stays the same.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | **yes** | Snapshot uuid. |
| `new_name` | string | no | New name-label. |

### `xenserver.vm.start`

Start a stopped VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.suspend`

Suspend a running VM to disk.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

### `xenserver.vm.unpause`

Unpause a paused VM.

| param | type | required | description |
|---|---|---|---|
| `uuid` | string | no | VM uuid. Either uuid or name is required. |
| `name` | string | no | VM name-label. Must resolve to exactly one VM. |

