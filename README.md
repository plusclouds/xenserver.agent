# PlusClouds VM Agent

> **The foundation that thinks.**

The PlusClouds VM Agent is a lightweight, production-grade daemon that turns any Linux or Windows machine into an intelligent infrastructure node. It connects to the PlusClouds platform over NATS, streams real-time telemetry, executes remote commands, and announces its own capabilities — so your platform always knows exactly what each machine can do.

No REST API. No open ports. No certificates to manage. Just a single binary, a config file, and a secure WebSocket connection to the platform.

---

## What it does

| Capability | Description |
|---|---|
| **Real-time telemetry** | CPU, memory, disk I/O, and network metrics pushed every 30 seconds |
| **Remote command execution** | Service management, system updates, and custom operations triggered from the platform |
| **Capability discovery** | On boot the agent publishes its full operation schema — the platform always knows what it can do |
| **Heartbeat** | Keeps the `is_alive` status current on the platform every 30 seconds |
| **Cross-platform** | Single codebase, two binaries: Linux (systemd/D-Bus) and Windows (SCM stub) |
| **Zero open ports** | Outbound-only NATS WebSocket connection — no inbound firewall rules needed |

---

## Architecture

The agent communicates exclusively over NATS. It subscribes to its own command subject and publishes events to its own event subject.

```
agent.vm.{uuid}.cmd   ←  platform sends commands
agent.vm.{uuid}.evt   →  agent sends telemetry, heartbeat, capabilities, results
vm.{uuid}.telemetry   →  client-facing telemetry stream (VM_TELEMETRY JetStream, 15-min retention)
```

Authentication uses the `agent_api_key` from `agent.yaml` (written by the platform during provisioning). The NATS auth callout validates every connection against the platform database and issues a scoped JWT — no static passwords, no shared secrets.

### Message envelope

Every NATS message in either direction shares a common JSON envelope:

```json
{
  "v":          1,
  "id":         "550e8400-e29b-41d4-a716-446655440000",
  "type":       "command|telemetry|heartbeat|capabilities|result|...",
  "agent_type": "vm",
  "agent_uuid": "550e8400-e29b-41d4-a716-446655440001",
  "timestamp":  1748000000,
  "payload":    {}
}
```

See [docs/protocol.md](docs/protocol.md) for the full payload schema for every event type.

---

## Telemetry payload

The agent publishes a structured telemetry snapshot every 30 seconds:

```json
{
  "cpu": {
    "usage_pct":  12.5,
    "core_count": 4,
    "load_avg":   [0.18, 0.23, 0.09],
    "cores": [
      { "id": 0, "usage_pct": 15.2 },
      { "id": 1, "usage_pct": 9.8 }
    ]
  },
  "memory": {
    "total_bytes": 4051935232,
    "used_bytes":  564654080,
    "usage_pct":   13.9
  },
  "disks": [
    {
      "device": "/dev/xvda2", "mountpoint": "/",
      "total_bytes": 42156257280, "used_bytes": 7149359104, "usage_pct": 17.7,
      "io": {
        "read_bytes_per_s": 1048576, "write_bytes_per_s": 524288,
        "read_iops": 120, "write_iops": 45, "util_pct": 23.4
      }
    }
  ],
  "network": [
    { "interface": "eth0", "bytes_sent": 95310, "bytes_recv": 1652348, "is_up": true }
  ]
}
```

Disk I/O rates are calculated as a delta between consecutive snapshots — the first telemetry event after boot omits the `io` field. Pseudo-filesystems (`tmpfs`, `devtmpfs`, etc.) and virtual network interfaces (`lo`, `docker*`, `veth*`) are automatically excluded.

---

## Supported operations

The agent announces its available operations on boot via a `capabilities` event. The platform can also request a fresh capabilities list at any time by sending `agent.allowed_operations`.

| Operation | Description | Parameters |
|---|---|---|
| `agent.allowed_operations` | Re-publish the capabilities list | — |
| `services.list` | List all loaded systemd services | — |
| `services.get` | Get status of a single service | `name` (string) |
| `services.start` | Start a service | `name` (string) |
| `services.stop` | Stop a service | `name` (string) |
| `services.restart` | Restart a service | `name` (string) |
| `services.reload` | Reload a service | `name` (string) |
| `services.enable` | Enable a service on boot | `name` (string) |
| `services.disable` | Disable a service on boot | `name` (string) |
| `system.info` | Hostname, OS, kernel, uptime | — |
| `system.metrics` | Full resource snapshot | — |
| `system.cpu` | CPU usage + per-core breakdown | — |
| `system.memory` | RAM utilisation | — |
| `system.disk` | Disk usage + I/O rates | — |
| `system.network` | Network interface counters | — |
| `system.update` | `apt-get update && upgrade -y` | — (Ubuntu/Debian only) |
| `telemetry.set_interval` | Change telemetry push interval | `interval_s` (integer, min 5) |
| `vm.reboot` | Reboot the machine | — |
| `vm.shutdown` | Shut down the machine | — |
| `exec` | Run an allowed binary | `command` (string), `args` (array) |
| `xenserver.pool.info` | Resource pool record | — |
| `xenserver.host.list` | List hosts in the pool | — |
| `xenserver.host.info` | Detailed record for this host | — |
| `xenserver.vm.list` | List guest VMs on the pool | — |
| `xenserver.vm.get` | Detailed record for one VM | `uuid` or `name` |
| `xenserver.vm.snapshot_list` | List a VM's snapshots | `uuid` or `name` |
| `xenserver.sr.list` | List storage repositories | — |
| `xenserver.sr.get` | Detailed SR record | `uuid` or `name` |
| `xenserver.network.list` | List xapi networks (OVS bridges) | — |
| `xenserver.vif.list` | List a VM's virtual network interfaces | `uuid` or `name` |
| `xenserver.vm.disk_list` | List a VM's disks/CD drives by position (`0` = first disk) with VDI uuid, name, size, SR | `uuid` or `name` |
| `xenserver.vdi.list` | List virtual disk images | `sr_uuid` (optional filter) |
| `xenserver.ipmi.sensors` | Read the current IPMI sensor table | — |
| `xenserver.metrics.rrd` | Latest XAPI RRD utilisation snapshot (host + every VM) | — |
| `xenserver.vm.start` | Start a stopped VM | `uuid` or `name` |
| `xenserver.vm.shutdown` | Clean ACPI shutdown of a VM | `uuid` or `name` |
| `xenserver.vm.reboot` | Clean reboot of a VM | `uuid` or `name` |
| `xenserver.vm.force_shutdown` | Hard power-off (no guest cooperation; no data flush) | `uuid` or `name` |
| `xenserver.vm.force_reboot` | Hard reboot (no guest cooperation; no data flush) | `uuid` or `name` |
| `xenserver.vm.suspend` | Suspend a VM to disk | `uuid` or `name` |
| `xenserver.vm.resume` | Resume a suspended VM | `uuid` or `name` |
| `xenserver.vm.snapshot_create` | Take a VM snapshot | `uuid`/`name`, `label` |
| `xenserver.vm.param_set` | Set allowlisted VM params: `name-label`, `name-description`, `VCPUs-max`, `VCPUs-at-startup`, `platform:<k>`, `xenstore-data:<k>`, `other-config:<k>` (keys outside the list rejected; returns applied list) | `uuid`/`name`, `set` (object), `unset` (array of map entries) |
| `xenserver.vm.param_get` | Read one VM parameter or one map key | `uuid`/`name`, `param`, `param_key` |
| `xenserver.vm.param_list` | All VM parameters: scalars, maps as objects, sets as arrays | `uuid`/`name` |
| `xenserver.vm.memory_limits_set` | Set the four memory limits in bytes (halted VM) | `uuid`/`name`, `static_min`, `static_max`, `dynamic_min`, `dynamic_max` |
| `xenserver.vm.console_list` | List consoles (uuid, protocol, location) | `uuid`/`name` |
| `xenserver.vm.create` | Create a VM, optionally from a template (async-capable); returns `data.uuid` | `new_name`, `description`, `template`, `sr_uuid` |
| `xenserver.vm.clone` | Clone a VM (async-capable); returns `data.uuid` | `uuid`/`name`, `new_name`, `description` |
| `xenserver.vm.destroy` | Delete a halted VM record (irreversible; disks stay) | `uuid`/`name`, `confirm: true` |
| `xenserver.vm.pause` / `xenserver.vm.unpause` | Pause / unpause a VM | `uuid`/`name` |
| `xenserver.vdi.create` | Create a disk on an SR; returns `data.uuid` | `sr_uuid`, `new_name`, `size_bytes`, `description`, `type`, `sharable`, `read_only` |
| `xenserver.vdi.destroy` | Permanently delete a disk (irreversible; refused while attached) | `vdi_uuid`, `confirm: true` |
| `xenserver.vdi.param_list` | All disk parameters (scalars, maps, sets) | `vdi_uuid` |
| `xenserver.vbd.create` | Attach a disk to a VM (unplugged); returns `data.uuid` | `uuid`/`name`, `vdi_uuid`, `device`, `mode`, `type`, `bootable` |
| `xenserver.vbd.plug` / `unplug` | Hot-attach / hot-detach a VBD on a running VM | `vbd_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vbd.destroy` | Remove a disk's attachment (disk stays) | `vbd_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vbd.param_list` | All VBD parameters | `vbd_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vm.cd_list` | List a VM's CD drives and ISOs | `uuid`/`name` |
| `xenserver.vm.cd_add` / `cd_insert` / `cd_eject` / `cd_remove` | CD drive operations by ISO name | `uuid`/`name`, `cd_name`, `device` |
| `xenserver.vif.create` | Add a network card to a VM; returns `data.uuid` | `uuid`/`name`, `device`, `network_uuid`/`network_name`, `mac`, `mtu` |
| `xenserver.vif.param_list` | All card parameters | `vif_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vif.param_set` | Rate limit (`qos_algorithm_type`, `qos_algorithm_params:kbps`), `other-config:*`, `locking-mode`, `ipv4-allowed`, `ipv6-allowed`, `MAC` (recreates the card) | selector, `set` |
| `xenserver.network.create` / `destroy` / `param_list` | Create (returns `data.uuid`), delete (`confirm: true`) or inspect a network | `new_name`, `description`, `mtu` / `uuid`, `confirm` |
| `xenserver.vlan.list` / `create` / `destroy` / `param_list` | List (optional `tag`), create (`pif_uuid`, `network_uuid`, `tag`), delete (`confirm: true`), inspect VLANs | see schema |
| `xenserver.pif.list` / `param_list` / `param_get` / `unplug` | List interfaces (filters `vlan`, `network_uuid`, `host_uuid`), inspect, read one parameter, take down (`confirm: true`; management interface refused) | see schema |
| `xenserver.pbd.list` / `param_list` | SR attachments (secrets masked) | `sr_uuid`, `host_uuid` / `uuid` |
| `xenserver.task.list` | List xapi tasks | — |
| `xenserver.sr.scan` | Rescan an SR (async-capable) | `uuid`/`name` |
| `xenserver.sr.param_list` / `param_get` | SR parameters | `uuid`/`name`, `param`, `param_key` |
| `xenserver.host.param_list` | Host parameters (default: this host) | `uuid` |
| `xenserver.vm.snapshot_promote` | Turn a snapshot into a VM in place | `uuid`, `new_name` |
| Filters | `vm.list`: `name_label`, `is_control_domain`, `is_a_template`, `fields`; `sr.list`: `type`, `name_label`, `content_type`; `network.list`: `name_label`, `bridge`, `pif_uuid` | |
| `xenserver.vm.promote_template` | Turn a template into a VM in place (restoring a snapshot export imports a template) | `uuid` or `name`, `new_name` |
| `xenserver.vm.snapshot_export` | Export a snapshot as a .xva image (refuses to overwrite; reports size/duration) | `uuid` or `name`, `filename` (absolute path) |
| `xenserver.vm.snapshot_delete` | Destroy a snapshot | `uuid` (the snapshot's own uuid) |
| `xenserver.sr.create` | Introduce a new SR | `name`, `type`, `shared`, `content_type`, `host_uuid`, `device_config` |
| `xenserver.sr.remove` | Safely detach an SR (data preserved) | `uuid` or `name` |
| `xenserver.sr.set_default` | Make an SR the pool's primary storage (default/suspend/crash-dump SR) | `uuid` or `name` |
| `xenserver.vif.set_network` | Move a VM's network card to another network (recreates the VIF keeping device/MAC/MTU/locking; rolls back on failure; VM halted) | `network_uuid` or `network_name`, `vif_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vif.remove` | Delete a VM's network card (VM halted or VIF unplugged); returns the removed device/MAC/network | `vif_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vif.set_acl` | Set native VIF anti-spoofing/allowlist ACL | `vif_uuid` or (`uuid`/`name` + `device`), `locking_mode`, `ipv4_allowed`, `ipv6_allowed` |
| `xenserver.vm.import` | Import a VM from a local file | `filename`, `sr_uuid`, `preserve` |
| `xenserver.vm.export` | Export a whole VM to a local `.xva` | `uuid`/`name`, `filename`, `compress` |
| `xenserver.vdi.copy` | Copy a VDI's data to another SR | `vdi_uuid`, `sr_uuid` |
| `xenserver.vdi.resize` | Grow a VDI (never shrinks); by `vdi_uuid` or VM `uuid`/`name` + disk position `device` | `size_bytes`, `vdi_uuid` or (`uuid`/`name` + `device`) |
| `xenserver.vdi.clone` | Fast copy-on-write VDI clone (same SR) | `vdi_uuid` |
| `xenserver.vdi.export` | Export a single VDI's raw disk content | `vdi_uuid`, `filename`, `format` |
| `openvswitch.bridge.list` | List OVS bridges | — |
| `openvswitch.port.list` | List ports on a bridge | `bridge` |
| `openvswitch.flow.list` | Dump a bridge's raw flow table | `bridge` |
| `openvswitch.vxlan.create` | Add a VXLAN encapsulation port | `bridge` or `network_uuid`, `port_name`, `remote_ip`, `local_ip`, `vni` |
| `openvswitch.vxlan.remove` | Remove a VXLAN port | `bridge`, `port_name` |
| `openvswitch.flow.add` | Install a raw OVS flow rule | `bridge`, `flow_spec` |
| `openvswitch.flow.remove` | Remove matching OVS flow rules | `bridge`, `match_spec` |
| `agent.job.status` | Status/progress of a background job (`running`/`completed`/`failed`/`cancelled`, elapsed, bytes done/estimated total, final result) | `job_id` |
| `agent.job.list` | List background jobs, newest first | — |
| `agent.job.cancel` | Cancel a running job (half-written export removed) | `job_id` |
| `nfsmount.list` | List currently-mounted NFS filesystems | — |
| `nfsmount.list_files` | List files directly under a mounted NFS mount point (image repository browser; not recursive) | `mount_point`, `suffix`, `sort_by` (name/size/mtime), `descending`, `limit` |
| `nfsmount.mount` | Mount an NFS export | `server`, `export_path`, `mount_point`, `mount_options` |
| `nfsmount.unmount` | Unmount an NFS filesystem | `mount_point` |

All operations are opt-in. Remove any entry from `allowed_operations` in `agent.yaml` and the platform receives a `rejected` result instead of executing it.

### XenServer/XCP-ng hypervisor management

The `xenserver.*`, `openvswitch.*`, and `nfsmount.*` operations manage the **hypervisor itself** (Dom0), not the machine the agent runs on — distinct from `vm.reboot`/`vm.shutdown`, which power-cycle the agent's own host. They require the agent to run as root on a XenServer/XCP-ng Dom0 with the `xe`, `ovs-vsctl`, and `ovs-ofctl` binaries available.

A few operational notes:

- **VM/SR identification** accepts either `uuid` or `name` (a name-label). `uuid` wins when both are given; `name` must resolve to exactly one match or the operation is rejected as ambiguous/not-found.
- **VXLAN** (`openvswitch.vxlan.*`) is implemented via direct `ovs-vsctl` commands rather than xapi's `tunnel` object, since the tunnel API's VXLAN support is version-dependent. Prefer passing `network_uuid` (a `xe network-create`-backed network) over a raw `bridge` name where possible — `xcp-networkd` only preserves OVS state it recognizes as belonging to a xapi network, and can otherwise tear down unrecognized ports/bridges on its own reconciliation events (xapi restart, host reboot, etc.).
- **Traffic filtering** is available two ways: the native, xapi-managed `xenserver.vif.set_acl` (anti-spoofing / IP allowlisting via `locking-mode`) and raw `openvswitch.flow.*` rules for anything the native ACL can't express. Prefer the native ACL where it suffices — it survives xapi's own reconfiguration; raw flow rules do not have that guarantee.
- **`nfsmount.mount`** does not confine mount points to a configured base directory — the platform may mount anywhere it asks. This is a deliberate choice; restrict what the platform is permitted to send if that's a concern in your deployment.
- **`xenserver.vdi.export`** has no `xe` subprocess equivalent — raw single-disk export only exists through xapi's local HTTP interface, so this operation authenticates over the local xapi Unix socket (`xenserver.xapi_socket_path`) instead of shelling out.

#### Replacing the RRD/IPMI/events sidecar scripts

Historically, XenServer hosts had three Python scripts (`rrd.py`, `ipmi.py`, `events.py`) pushed to Dom0 and cron'd to poll XAPI's performance/event/IPMI interfaces and `curl` the results to the platform. The agent now does all three natively — no sidecar scripts, no cron, no plaintext-adjacent `curl` shelling:

- **`xenserver.events_enabled`** — long-polls XAPI's own object-change event bus (`event.from`) and streams every event (VM/SR/host/... state change) to the platform as a `xapi_event` message, in real time rather than on a cron cadence.
- **`xenserver.ipmi_enabled`** — periodically runs `ipmitool sensor` and publishes the full sensor table (temperatures, fans, voltages, power supplies, with thresholds) as an `ipmi` message. Also available on-demand via the `xenserver.ipmi.sensors` operation.
- **`xenserver.rrd_enabled`** — merges XAPI's own RRD-derived per-VM utilisation (actual observed CPU/memory/disk/network usage inside each guest, which Dom0 has no other way to see) into the regular telemetry push as a `vms` key, matching the schema `docs/protocol.md` already anticipated for compute agents. Also available on-demand via `xenserver.metrics.rrd`.

All three default to `false`/disabled — enable them per-host once you're ready to retire the corresponding Python script and cron entry.

---

## Installation

### 1. Deploy the binary

```bash
# Linux
scp bin/plusclouds.linux root@<server-ip>:/usr/local/bin/plusclouds-agent
chmod +x /usr/local/bin/plusclouds-agent

# Windows
# Copy bin/plusclouds.windows to the target machine and run it as a service
```

### 2. Create the config directory

```bash
mkdir -p /etc/plusclouds /var/log/plusclouds
chmod 0750 /var/log/plusclouds
```

### 3. Deploy the config

```bash
scp configs/agent.yaml root@<server-ip>:/etc/plusclouds/agent.yaml
```

Edit `/etc/plusclouds/agent.yaml` and set the identity fields for this machine (written automatically during VM provisioning):

```yaml
nats:
  connection_type: websocket           # "nats" or "websocket"
  websocket_url: wss://nats.plusclouds.com:443
  agent_uuid: "<vm-uuid>"             # from iaas_virtual_machines
  api_key:     "<agent-api-key>"      # from iaas_virtual_machines.events_token
```

### 4. Install and start the systemd service

```bash
scp systemd/plusclouds-agent.service root@<server-ip>:/etc/systemd/system/
systemctl daemon-reload
systemctl enable --now plusclouds-agent
```

### 5. Verify

```bash
journalctl -fu plusclouds-agent
# or
tail -f /var/log/plusclouds/agent.log | jq .
```

You should see:
```
agent identity resolved   {"agent_uuid": "..."}
connected to NATS         {"url": "wss://nats.plusclouds.com:443"}
capabilities published    {"operation_count": 17}
heartbeat published
telemetry published
```

---

## Configuration reference

```yaml
nats:
  connection_type: websocket           # nats | websocket (ws:// needs no certificates)
  url: nats://nats.plusclouds.com:4222
  websocket_url: wss://nats.plusclouds.com:443
  agent_uuid: ""                       # VM UUID — set by provisioning
  api_key: ""                          # NATS auth token — set by provisioning
  max_reconnects: -1                   # -1 = unlimited
  reconnect_wait: 5s

agent:
  heartbeat_interval: 30s
  telemetry_interval: 30s             # changeable at runtime via telemetry.set_interval
  dry_run: false                      # true = log commands instead of running them (see below)
  allowed_operations:
    - agent.allowed_operations
    - services.list
    - services.get
    - services.start
    - services.stop
    - services.restart
    - services.reload
    - services.enable
    - services.disable
    - system.info
    - system.metrics
    - system.cpu
    - system.memory
    - system.disk
    - system.network
    - system.update
    - telemetry.set_interval
    # - vm.reboot
    # - vm.shutdown
    # - exec
    - xenserver.pool.info               # read-only xenserver.*/openvswitch.*/nfsmount.*
    - xenserver.host.list               # ops are enabled by default; state-changing
    - xenserver.host.info               # ones ship commented out below — see the
    - xenserver.vm.list                 # "Supported operations" table for the full list.
    - xenserver.vm.get
    - xenserver.vm.snapshot_list
    - xenserver.sr.list
    - xenserver.sr.get
    - xenserver.network.list
    - xenserver.vif.list
    - xenserver.vm.disk_list
    - xenserver.vm.param_get
    - xenserver.vm.param_list
    - xenserver.vm.console_list
    - xenserver.vm.cd_list
    - xenserver.vdi.param_list
    - xenserver.vbd.param_list
    - xenserver.vif.param_list
    - xenserver.network.param_list
    - xenserver.vlan.list
    - xenserver.vlan.param_list
    - xenserver.pif.list
    - xenserver.pif.param_list
    - xenserver.pif.param_get
    - xenserver.pbd.list
    - xenserver.pbd.param_list
    - xenserver.task.list
    - xenserver.sr.param_list
    - xenserver.sr.param_get
    - xenserver.host.param_list
    - xenserver.vdi.list
    - xenserver.ipmi.sensors
    - xenserver.metrics.rrd
    - openvswitch.bridge.list
    - openvswitch.port.list
    - openvswitch.flow.list
    - nfsmount.list
    - nfsmount.list_files
    # - xenserver.vm.start
    # - xenserver.vm.shutdown
    # - xenserver.vm.reboot
    # - xenserver.vm.force_shutdown
    # - xenserver.vm.force_reboot
    # - xenserver.vm.suspend
    # - xenserver.vm.resume
    # - xenserver.vm.snapshot_create
    # - xenserver.vm.param_set
    # - xenserver.vm.memory_limits_set
    # - xenserver.vm.create
    # - xenserver.vm.clone
    # - xenserver.vm.destroy
    # - xenserver.vm.pause
    # - xenserver.vm.unpause
    # - xenserver.vdi.create
    # - xenserver.vdi.destroy
    # - xenserver.vbd.create
    # - xenserver.vbd.plug
    # - xenserver.vbd.unplug
    # - xenserver.vbd.destroy
    # - xenserver.vm.cd_add
    # - xenserver.vm.cd_insert
    # - xenserver.vm.cd_eject
    # - xenserver.vm.cd_remove
    # - xenserver.vif.create
    # - xenserver.vif.param_set
    # - xenserver.network.create
    # - xenserver.network.destroy
    # - xenserver.vlan.create
    # - xenserver.vlan.destroy
    # - xenserver.pif.unplug
    # - xenserver.sr.scan
    # - xenserver.vm.snapshot_promote
    # - xenserver.vm.promote_template
    # - xenserver.vm.snapshot_export
    # - xenserver.vm.snapshot_delete
    # - xenserver.sr.create
    # - xenserver.sr.remove
    # - xenserver.sr.set_default
    # - xenserver.vif.set_network
    # - xenserver.vif.remove
    # - xenserver.vif.set_acl
    # - xenserver.vm.import
    # - xenserver.vm.export
    # - xenserver.vdi.copy
    # - xenserver.vdi.resize
    # - xenserver.vdi.clone
    # - xenserver.vdi.export
    # - openvswitch.vxlan.create
    # - openvswitch.vxlan.remove
    # - openvswitch.flow.add
    # - openvswitch.flow.remove
    # - nfsmount.mount
    # - nfsmount.unmount
  allowed_commands:                    # only used when exec is enabled above
    - /usr/bin/journalctl
    - /usr/bin/df
    - /usr/bin/free

xenserver:
  xe_binary: /usr/bin/xe
  inventory_path: /etc/xensource-inventory
  xapi_socket_path: /var/lib/xcp/xapi
  ipmi_binary: /usr/bin/ipmitool
  events_enabled: false               # long-poll XAPI's event bus, streaming changes
  events_poll_interval: 10s
  ipmi_enabled: false                 # periodic IPMI sensor push
  ipmi_interval: 60s
  rrd_enabled: false                  # merge per-VM RRD utilisation into telemetry

openvswitch:
  vsctl_binary: /usr/bin/ovs-vsctl
  ofctl_binary: /usr/bin/ovs-ofctl

nfsmount:
  mount_binary: /usr/bin/mount
  umount_binary: /usr/bin/umount

iso:
  mount_path: /media/plusclouds-config # optional, checked silently at boot

log:
  level: info                          # debug | info | warn | error
  format: json                         # json | console
  file: /var/log/plusclouds/agent.log  # leave empty to disable file logging

autoheal:
  enabled: true
  restart_delay: 10s
```

All values can be overridden by environment variables using the prefix `PLUSCLOUDS_AGENT_` (e.g. `PLUSCLOUDS_AGENT_NATS_API_KEY`).

---

## Building from source

Requires Go 1.22+.

```bash
# Development build (current OS)
make build

# Production build — Linux amd64, static binary, stripped
make build-linux

# Production build — Windows amd64
make build-windows

# Both platforms at once
make build-all

# Run tests
make test
```

Outputs:
```
bin/plusclouds.linux    — ELF 64-bit, statically linked, ~12 MB
bin/plusclouds.windows  — PE32+, ~12 MB
```

---

## Platform compatibility

| Feature | Linux | Windows |
|---|---|---|
| NATS connection | ✅ | ✅ |
| Telemetry (CPU, RAM, disk, network) | ✅ | ✅ (load_avg = 0) |
| Heartbeat | ✅ | ✅ |
| Capabilities event | ✅ | ✅ |
| Service management | ✅ systemd/D-Bus | ⚙ stub (SCM planned) |
| `system.update` | ✅ Ubuntu/Debian | ✗ |
| `vm.reboot` / `vm.shutdown` | ✅ `systemctl` | ✅ `shutdown /r` |

---

## Security model

- **Outbound-only** — the agent never listens on any port
- **Scoped JWT** — the NATS auth callout issues a JWT granting publish/subscribe only to this agent's own subjects
- **Operation allowlist** — `allowed_operations` in `agent.yaml` is a hard gate; unknown or unlisted operations return `rejected`
- **Exec allowlist** — when `exec` is enabled, only binaries explicitly listed in `allowed_commands` can be invoked
- **Token revocation** — remove `events_token` from the platform database and the agent is rejected on its next connection attempt

---

## Dry-run mode

Set `agent.dry_run: true` in `agent.yaml` to run the agent without touching the host. Every operation is still authenticated and checked against `allowed_operations`, required parameters, `confirm` for destructive operations and `allowed_commands` for `exec`, but nothing is executed: a valid call returns `{"dry_run": true, "executed": false, "operation": ..., "params": ...}` (secrets masked), and an invalid one is rejected exactly as it would be for real. The `agent.*` meta operations (job status etc.) still run. Works the same over HTTPS and NATS. The agent logs a prominent warning on startup when dry-run is active.

This does **not** cover `services.*` — those go through systemd D-Bus method calls, not a subprocess, so there's no command line to log or skip.

---

## Support

Having trouble? We're here to help.

📧 **[support@plusclouds.com](mailto:support@plusclouds.com)**

For bug reports and feature requests, open an issue on GitHub.

---

## Our Libraries

This agent is part of the **PlusClouds open-source ecosystem** — precision infrastructure and intelligence tools built for SaaS companies and tech-forward businesses.

Browse all available libraries and building blocks:
[https://plusclouds.com/us/solutions/libraries](https://plusclouds.com/us/solutions/libraries)

---

## Join the Community

Great infrastructure is built together. The PlusClouds developer community is where engineers share ideas, ask questions, and help shape the direction of the platform. Whether you're integrating a single agent or building an entire infrastructure layer on our stack — you're welcome here.

[https://plusclouds.com/us/community](https://plusclouds.com/us/community)

## Background (async) operations

Long operations — `xenserver.vm.snapshot_export`, `vm.export`, `vm.import`, `vdi.copy`, `vdi.clone`, `vdi.export` — accept `"async": true` in `params`. The agent starts the work in the background and replies at once: HTTP `202` over HTTPS, and `payload.status: "accepted"` on any transport, with the job in `output`:

```json
{"operation":"xenserver.vm.snapshot_export","params":{"uuid":"<snapshot>","filename":"/mnt/backup/x.xva","async":true}}
→ 202 {"status":"accepted","output":{"job_id":"…","status":"running",…}}
```

Poll `agent.job.status` with the `job_id`. Exports report `progress.bytes_done` (the file size on disk) against an estimated `bytes_total` (allocated disk size), so `percent` is indicative and capped at 99 until the job truly finishes; imports report elapsed time only. When finished, `result` holds the operation's own output (the same JSON the blocking call returns) and `status` is `completed`, or `failed` with `error`. `agent.job.cancel` stops a job and removes a half-written export.

Jobs live in memory only: they are lost on agent restart, and a job running at shutdown is cancelled. At most `agent.max_jobs` (default 2) run at once; further requests fail until one finishes. Add `agent.job.status` and `agent.job.list` (enabled by default) and, if wanted, `agent.job.cancel` to `allowed_operations`.

## Optional HTTPS endpoint

Disabled by default. Set `http.enabled: true` in `agent.yaml` to expose the dispatcher over HTTPS next to NATS.

- `POST /v1/command` — body is the command envelope, header `Authorization: Bearer <api_key>`. Returns the result envelope (HTTP 200; outcome in `payload.status`). Same `allowed_operations` allowlist as NATS. The server refuses to start if `http.api_key` is shorter than 32 characters or still the `CHANGE_ME…` placeholder.
- `GET /healthz` — unauthenticated liveness.
- TLS: with `http.self_signed: true` an ECDSA P-256 cert is generated at `http.cert_file`/`http.key_file` on first start, reused afterwards and renewed 30 days before expiry. Existing certs are never overwritten. The SHA-256 fingerprint is logged at startup; pin it on the client (`curl -k` or `--pinnedpubkey`). Add extra SANs via `http.hosts`.
