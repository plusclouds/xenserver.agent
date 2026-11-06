# PlusClouds VM Agent

> **The foundation that thinks.**

The PlusClouds VM Agent is a lightweight daemon that turns a Linux or Windows machine into a managed infrastructure node. It executes allow-listed operations on request, reports telemetry, and describes its own capabilities. It was built to manage **XenServer / XCP-ng hypervisors** (VMs, disks, networks, storage, backups) and also manages ordinary Linux hosts (services, system info, updates).

It speaks two transports, either or both:

- **NATS** (outbound-only WebSocket/TCP): the platform pushes commands and receives telemetry, heartbeat and events. No inbound port.
- **HTTPS** (optional): `POST /v1/command` with a bearer token and a self-signed or provided certificate. Use it to drive the agent directly, with no message bus.

Both transports use the same command envelope, the same operation allowlist and the same result format.

---

## What it does

| Capability | Description |
|---|---|
| **Hypervisor management** | XenServer/XCP-ng VMs, disks, network cards, networks, VLANs, storage repositories, ISO/CD, memory and CPU settings, power control |
| **Backups** | Snapshots, snapshot/VM/disk export, import, copy, clone, restore; background jobs with progress and cancel ([docs/backups.md](docs/backups.md)) |
| **Remote command execution** | Service management, system updates and allow-listed commands |
| **Telemetry** | CPU, memory, disk I/O and network every 30 s over NATS; IPMI sensors and per-VM RRD performance on demand or pushed |
| **Capability discovery** | The agent publishes its full operation schema; `docs/operations.md` is generated from it |
| **Safe by default** | Per-operation allowlist, `confirm` required for destructive operations, secrets masked, dry-run mode |
| **Cross-platform** | Linux (systemd/D-Bus, XenServer Dom0) and Windows (service-management stub) |

---

## Quick start (HTTPS)

1. Copy the binary and the sample config to the host (the hypervisor's Dom0 for XenServer features):

   ```bash
   scp bin/plusclouds.linux root@<host>:/usr/local/bin/plusclouds-agent
   ssh root@<host> 'chmod +x /usr/local/bin/plusclouds-agent && mkdir -p /etc/plusclouds /var/log/plusclouds'
   scp configs/agent.https.yaml root@<host>:/etc/plusclouds/agent.yaml
   ```

2. Edit `/etc/plusclouds/agent.yaml`: set `http.enabled: true`, `http.listen`, `http.hosts` and a long random `http.api_key` (`openssl rand -base64 48`). The agent refuses to start with a key shorter than 32 characters or the `CHANGE_ME` placeholder. Enable only the operations you need under `allowed_operations`.

3. Install the service and start it:

   ```bash
   scp systemd/plusclouds-agent.service root@<host>:/etc/systemd/system/
   ssh root@<host> 'systemctl daemon-reload && systemctl enable --now plusclouds-agent'
   ```

4. Call it. On first start the agent logs the certificate SHA-256 fingerprint; pin it on the client.

   ```bash
   curl -sk https://<host>:8443/healthz

   curl -sk https://<host>:8443/v1/command \
     -H "Authorization: Bearer $AGENT_API_KEY" \
     -d '{"v":1,"id":"1","type":"command","payload":{"operation":"xenserver.vm.list","params":{}}}'
   ```

See [docs/https-api.md](docs/https-api.md) for the full HTTPS guide (envelope, results, errors, async jobs, examples).

---

## Architecture

```
            ┌────────────── platform / your tooling ──────────────┐
            │                                                     │
   NATS  agent.compute.{uuid}.cmd ↓        ↑  .evt (results, telemetry, heartbeat, events)
   HTTPS POST /v1/command         ↓        ↑  HTTP response (result envelope)
            │                                                     │
            └──────────────────────┬──────────────────────────────┘
                                   ▼
                     ┌──────────── dispatcher ────────────┐
                     │ allowlist · confirm · param checks │
                     └─┬────────┬─────────┬────────┬──────┘
                       ▼        ▼         ▼        ▼
                  xenserver  openvswitch nfsmount services/system
                  (xe, xapi)  (ovs-*)    (mount)   (systemd, /proc)
```

Over NATS the agent subscribes to its own command subject and publishes to its own event subject:

```
agent.compute.{uuid}.cmd   ←  platform sends commands
agent.compute.{uuid}.evt   →  agent sends telemetry, heartbeat, capabilities, results
```

NATS authentication uses the agent's API key (written by the platform during provisioning); an auth callout issues a scoped JWT. HTTPS authentication is a static bearer token configured in `http.api_key`.

### Message envelope

Every message in either direction, on either transport, shares one JSON envelope:

```json
{
  "v":          1,
  "id":         "550e8400-e29b-41d4-a716-446655440000",
  "type":       "command",
  "agent_type": "compute",
  "agent_uuid": "550e8400-e29b-41d4-a716-446655440001",
  "timestamp":  1748000000,
  "payload":    { "operation": "xenserver.vm.list", "params": {} }
}
```

See [docs/protocol.md](docs/protocol.md) for every payload type, the result format and versioning. `v` is the protocol version (currently `1`; an omitted `v` is treated as 1, anything else is rejected). `GET /healthz` and the `X-Agent-Version` / `X-Protocol-Version` response headers report the agent build and protocol version.

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

The generated reference, [docs/operations.md](docs/operations.md), lists every operation with its parameters and description, straight from the agent's own catalog. The groups below summarise it.

| Group | Operations (prefix `xenserver.` unless noted) |
|---|---|
| **Agent / system** | `agent.allowed_operations`, `agent.job.status/list/cancel`, `system.info/metrics/cpu/memory/disk/network/update`, `telemetry.set_interval`, `vm.reboot`, `vm.shutdown` (the agent's own host), `exec` |
| **Services** (systemd) | `services.list/get/start/stop/restart/reload/enable/disable` |
| **Inventory** | `pool.info`, `host.list/info/param_list`, `vm.list/get/disk_list/console_list/snapshot_list`, `sr.list/get`, `vdi.list`, `network.list`, `vif.list`, `pif.list`, `pbd.list`, `vlan.list`, `task.list` |
| **VM lifecycle** | `vm.start/shutdown/reboot/force_shutdown/force_reboot/suspend/resume/pause/unpause`, `vm.create/clone/destroy`, `vm.param_set/param_get/param_list`, `vm.memory_limits_set` |
| **Disks** | `vdi.create/destroy/resize/clone/copy/export/param_list`, `vbd.create/plug/unplug/destroy/param_list`, `vm.cd_list/cd_add/cd_insert/cd_eject/cd_remove` |
| **Network cards and networks** | `vif.create/plug/unplug/remove/set_network/set_acl/param_set/param_list`, `network.create/destroy/param_list`, `vlan.create/destroy/param_list`, `pif.unplug/param_list/param_get` |
| **Storage** | `sr.create/remove/scan/set_default/param_list/param_get`, `pbd.param_list`, `nfsmount.mount/unmount/list/list_files` |
| **Backup and restore** | `vm.snapshot_create/snapshot_delete/snapshot_promote/snapshot_export`, `vm.export/import`, `vm.promote_template`, `vdi.export/copy/clone` |
| **Performance** | `ipmi.sensors`, `metrics.rrd` |
| **Open vSwitch** | `openvswitch.bridge.list`, `port.list`, `flow.list`, `vxlan.create/remove`, `flow.add/remove` |

Notes that apply across the board:

- **Everything is opt-in.** An operation that is not in `allowed_operations` returns `rejected`. Read-only operations are enabled in the sample configs; state-changing ones ship commented out.
- **Destructive operations require `confirm: true`** (for example `vm.destroy`, `vdi.destroy`, `network.destroy`, `pif.unplug`).
- **VM and SR selection** accepts either `uuid` or `name` (a name-label). `uuid` wins when both are given; a `name` must match exactly one object.
- **Secrets are masked** in logs and in parameter listings (SR device-config, `xenstore-data`, anything named password, secret or token).
- **Errors are readable.** Failures return a plain-English `message` plus structured `detail` (command, exit code, stderr). See [docs/https-api.md](docs/https-api.md#errors).

### XenServer/XCP-ng hypervisor management

The `xenserver.*`, `openvswitch.*` and `nfsmount.*` operations manage the **hypervisor itself** (Dom0), not the machine the agent runs on. This is distinct from `vm.reboot`/`vm.shutdown`, which power-cycle the agent's own host. They require the agent to run as root on a XenServer/XCP-ng Dom0 with `xe`, `ovs-vsctl` and `ovs-ofctl` available. Tested against XenServer 8.2.

- **VXLAN** (`openvswitch.vxlan.*`) uses direct `ovs-vsctl` commands rather than xapi's tunnel object, whose VXLAN support is version-dependent. Prefer `network_uuid` (an `xe network-create` network) over a raw bridge name: `xcp-networkd` can otherwise tear down ports it does not recognise.
- **Traffic filtering**: use the native `xenserver.vif.set_acl` where it suffices (it survives xapi reconfiguration); raw `openvswitch.flow.*` rules cover the rest but carry no such guarantee.
- **`nfsmount.mount`** accepts any absolute mount point; there is no configured base directory. Restrict who can call it. Server, export path, mount point and options are validated.
- **`xenserver.vdi.export`** has no `xe` equivalent; it authenticates over the local xapi Unix socket (`xenserver.xapi_socket_path`).
- **Hot-plug** (`vbd.plug`, `vif.plug` and their `unplug`) needs the guest's Xen PV drivers/tools running.
- **Disk resize** grows a disk, never shrinks it. xapi refuses to resize a disk that is plugged into a running VM on NFS storage; unplug it or stop the VM first. Growing the partition and filesystem inside the guest is up to the guest.

#### IPMI, performance and events

The agent replaces the old per-host polling scripts:

- **`xenserver.events_enabled`**: long-polls xapi's event bus and streams every VM/SR/host change over NATS as `xapi_event`.
- **`xenserver.ipmi_enabled`**: periodically publishes the IPMI sensor table (temperatures, fans, voltages, power supplies, thresholds) as an `ipmi` message. On demand: `xenserver.ipmi.sensors`.
- **`xenserver.rrd_enabled`**: merges xapi's per-VM RRD utilisation (CPU, memory, disk, network as observed from Dom0) into telemetry under `vms`. On demand: `xenserver.metrics.rrd`.

All three default to disabled.

---

## Background (async) operations

Long operations (`xenserver.vm.snapshot_export`, `vm.export`, `vm.import`, `vdi.copy`, `vdi.clone`, `vdi.export`, `vm.clone`, `vm.create`, `sr.scan`) accept `"async": true` in `params`. The agent starts the work in the background and replies at once: HTTP `202` over HTTPS and `payload.status: "accepted"` on any transport, with the job in `output`:

```json
{"operation":"xenserver.vm.snapshot_export","params":{"uuid":"<snapshot-uuid>","filename":"/mnt/backup/x.xva","async":true}}
→ 202 {"status":"accepted","output":{"job_id":"…","status":"running",…}}
```

Poll `agent.job.status` with the `job_id`. Exports report `progress.bytes_done` against an estimated `bytes_total`, so `percent` is indicative and capped at 99 until the job finishes. `agent.job.cancel` stops a job and removes a half-written export. Jobs are held in memory: they are lost on agent restart, and a job running at shutdown is cancelled. At most `agent.max_jobs` (default 2) run at once. Details and a restore walkthrough: [docs/backups.md](docs/backups.md).

---

## Optional HTTPS endpoint

Disabled by default; set `http.enabled: true`.

- `POST /v1/command`: body is the command envelope, header `Authorization: Bearer <api_key>`. Returns the result envelope (HTTP 200, with the outcome in `payload.status`; HTTP 202 for accepted async jobs). Same allowlist as NATS.
- `GET /healthz`: unauthenticated liveness.
- **TLS:** with `http.self_signed: true` an ECDSA P-256 certificate is generated at `http.cert_file`/`http.key_file` on first start, reused afterwards and renewed 30 days before expiry. Existing certificates are never overwritten. The SHA-256 fingerprint is logged at startup; pin it on the client. Add extra names with `http.hosts`. Or point `cert_file`/`key_file` at your own certificate.
- **Token:** the server refuses to start if `http.api_key` is shorter than 32 characters or still the `CHANGE_ME…` placeholder.
- **HTTPS-only mode:** set `nats.enabled: false`. The agent then makes no outbound connections and publishes nothing; the caller polls.

Full guide: [docs/https-api.md](docs/https-api.md).

---

## Installation

Short version for a hypervisor host (full guide, including identity, the optional config drive, credentials and upgrades: [docs/provisioning.md](docs/provisioning.md)):

```bash
install -m 0755 plusclouds-agent-linux-amd64 /usr/local/bin/plusclouds-agent   # from a release; verify SHA256SUMS
mkdir -p /etc/plusclouds /var/log/plusclouds /var/lib/plusclouds/tls
install -m 0600 configs/agent.https.yaml /etc/plusclouds/agent.yaml            # or configs/agent.yaml for NATS
install -m 0644 systemd/plusclouds-agent.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now plusclouds-agent
journalctl -fu plusclouds-agent
```

To see every command and result, stop the service and run `plusclouds-agent --config /etc/plusclouds/agent.yaml` in the foreground with `log.level: debug`.

---

## Configuration reference

```yaml
nats:
  enabled: true                        # false = HTTPS-only: no NATS connection, no pushes
  connection_type: websocket           # nats | websocket (ws:// needs no certificates)
  url: nats://nats.example.com:4222
  websocket_url: wss://nats.example.com:443
  agent_uuid: ""                       # agent UUID — set by provisioning
  api_key: ""                          # NATS auth token — set by provisioning
  max_reconnects: -1                   # -1 = unlimited
  reconnect_wait: 5s

http:                                  # optional HTTPS endpoint (see "Optional HTTPS endpoint")
  enabled: false
  listen: "0.0.0.0:8443"
  cert_file: /var/lib/plusclouds/tls/agent.crt
  key_file: /var/lib/plusclouds/tls/agent.key
  self_signed: true                    # generate the cert on first start if missing
  api_key: ""                          # bearer token, at least 32 characters
  hosts: []                            # extra DNS names / IPs for the self-signed cert

agent:
  heartbeat_interval: 30s
  telemetry_interval: 30s             # changeable at runtime via telemetry.set_interval
  dry_run: false                      # true = validate only, execute nothing (see Dry-run mode)
  max_jobs: 2                         # concurrent background (async) jobs
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
    - agent.job.status                  # background-job polling
    - agent.job.list
    # - agent.job.cancel
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
    # - xenserver.vif.plug
    # - xenserver.vif.unplug
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

All values can be overridden by environment variables using the prefix `PLUSCLOUDS_AGENT_` (e.g. `PLUSCLOUDS_AGENT_HTTP_API_KEY`). Keep real keys out of files you commit.

---

## Platform compatibility

| Feature | Linux | Windows |
|---|---|---|
| NATS and HTTPS transports | ✅ | ✅ |
| Telemetry (CPU, RAM, disk, network) | ✅ | ✅ (load_avg = 0) |
| Heartbeat, capabilities | ✅ | ✅ |
| Service management | ✅ systemd/D-Bus | ⚙ stub (SCM planned) |
| `system.update` | ✅ Ubuntu/Debian | ✗ |
| `vm.reboot` / `vm.shutdown` | ✅ `systemctl` | ✅ `shutdown /r` |
| XenServer / Open vSwitch / NFS operations | ✅ on Dom0 | ✗ |

The Windows binary is cross-compiled and has not been run in production.

---

## Security model

Summary; details in [docs/security.md](docs/security.md).

- **Operation allowlist.** `allowed_operations` is a hard gate; unlisted operations return `rejected`. State-changing operations ship disabled.
- **Destructive operations** need `confirm: true`. Attached disks, the management interface and similar footguns are refused by default.
- **Exec allowlist.** When `exec` is enabled, only binaries listed in `allowed_commands` run, with no shell.
- **Transport security.** NATS: outbound-only, scoped JWT per agent, revocable. HTTPS: TLS 1.2+, constant-time bearer-token check, 1 MiB request limit, token of at least 32 characters.
- **Secrets are masked** in logs, error output and parameter listings.
- **Argument injection** is closed off: values go to `xe`, `mount` and `umount` as discrete arguments after validation, never through a shell.
- **Run as root only where required** (Dom0). The agent kills any child process it started if it dies.
- **Treat the HTTPS endpoint like an admin API:** bind to a management network, firewall it, rotate the token, and enable only the operations you need.

---

## Dry-run mode

Set `agent.dry_run: true` to run the agent without touching the host. Every operation is still authenticated and checked against `allowed_operations`, required parameters, `confirm` for destructive operations and `allowed_commands` for `exec`, but nothing is executed. A valid call returns `{"dry_run": true, "executed": false, "operation": ..., "params": ...}` (secrets masked); an invalid one is rejected exactly as it would be for real. The `agent.*` meta operations (job status and so on) still run. It works the same over HTTPS and NATS, and the agent logs a prominent warning on startup. Dry-run cannot predict errors that only appear at run time, such as a wrong uuid.

---

## Building from source

Requires Go 1.25+.

```bash
make build          # development build (current OS)
make build-linux    # Linux amd64, static, stripped  → bin/plusclouds.linux
make build-windows  # Windows amd64                  → bin/plusclouds.windows
make build-all      # both
make test           # go test -race with coverage
```

Regenerate the operations reference after changing the catalog:

```bash
GEN_API_DOC=1 go test ./internal/publisher -run TestGenerateAPIDoc
```

---

## Documentation

| Document | Contents |
|---|---|
| [docs/provisioning.md](docs/provisioning.md) | Identity, config drive, installation, credentials, upgrades |
| [docs/https-api.md](docs/https-api.md) | HTTPS endpoint: auth, envelope, results, errors, async jobs, examples |
| [docs/backups.md](docs/backups.md) | Snapshots, exports, restore, background jobs, NFS targets |
| [docs/operations.md](docs/operations.md) | Every operation and parameter (generated) |
| [docs/output-shapes.md](docs/output-shapes.md) | What the read operations return (summary vs full xe parameter shapes) |
| [docs/protocol.md](docs/protocol.md) | Envelope, results, pushed messages, versioning |
| [docs/overview.md](docs/overview.md) | Transports, NATS subjects, message flow |
| [docs/security.md](docs/security.md) | Controls, hardening checklist, known limits |

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
