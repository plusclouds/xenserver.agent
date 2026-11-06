# plsctl CLI Reference

> **Legacy document.** This describes the REST API and `plsctl` client of an early (v0.1) version of the agent. The current agent has no `/api/v1` REST API: it is driven by command envelopes over NATS or `POST /v1/command` over HTTPS. See the [README](../README.md), [https-api.md](https-api.md) and [operations.md](operations.md). Kept for reference only.

`plsctl` is the command-line client for the PlusClouds Ubuntu VM agent.
It communicates with a running `plusclouds-agent` instance over its HTTP API.

---

## Installation

### From the Debian package

```bash
apt install ./plusclouds-agent_0.1.0_amd64.deb
```

`plsctl` is installed to `/usr/local/bin/plsctl`.

### Building from source

```bash
git clone https://github.com/plusclouds/ubuntu-agent
cd ubuntu-agent
make build-ctl
sudo cp bin/plsctl /usr/local/bin/plsctl
```

---

## Configuration

`plsctl` can be configured via flags or environment variables.

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `--url` | `PLSCTL_URL` | `http://localhost:8080` | Agent API base URL |
| `--api-key` | `PLSCTL_API_KEY` | _(empty)_ | API key for authentication |
| `-o`, `--output` | — | `table` | Output format: `table` or `json` |

**Environment variable example:**
```bash
export PLSCTL_URL=http://192.168.1.10:8080
export PLSCTL_API_KEY=secret-api-key-for-agent-api
plsctl system info
```

**Flag example (all commands):**
```bash
plsctl --url http://192.168.1.10:8080 \
       --api-key secret-api-key-for-agent-api \
       system info
```

---

## Output Formats

### Table (default)

Human-readable columnar output using `text/tabwriter`.

```bash
plsctl system info
```

```
FIELD         VALUE
-----         -----
Hostname      web-01.tenant.plusclouds.net
OS            Ubuntu 24.04 LTS
Kernel        6.8.0-35-generic
Architecture  x86_64
Uptime        1d 0h 2m
Boot Time     2026-04-04T10:00:00Z
VM ID         vm-abc123
Tenant ID     tenant-xyz
```

### JSON

Pretty-printed JSON output.

```bash
plsctl -o json system info
```

```json
{
  "hostname": "web-01.tenant.plusclouds.net",
  "os": "Ubuntu 24.04 LTS",
  "kernel_version": "6.8.0-35-generic",
  "architecture": "x86_64",
  "uptime": 86520,
  "boot_time": 1712259000,
  "vm_id": "vm-abc123",
  "tenant_id": "tenant-xyz"
}
```

---

## Commands

### `plsctl agent`

Agent status and version commands.

#### `plsctl agent status`

Check whether the agent is running and healthy.

```bash
plsctl agent status
```

```json
{
  "status": "ok",
  "version": "0.1.0"
}
```

#### `plsctl agent version`

Print the plsctl version.

```bash
plsctl agent version
```

```
plsctl version: 0.1.0
```

---

### `plsctl system`

Query VM resource information.

#### `plsctl system info`

Show VM identity and OS information.

```bash
plsctl system info
```

```
FIELD         VALUE
-----         -----
Hostname      web-01.tenant.plusclouds.net
OS            Ubuntu 24.04 LTS
Kernel        6.8.0-35-generic
Architecture  x86_64
Uptime        1d 0h 2m
Boot Time     2026-04-04T10:00:00Z
VM ID         vm-abc123
Tenant ID     tenant-xyz
```

#### `plsctl system metrics`

Show all resource metrics in a summary.

```bash
plsctl system metrics
```

```
Collected at: 2026-04-05T10:01:18Z

FIELD         VALUE
-----         -----
CPU Model     Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz
Core Count    4
Usage         12.5%
Load Avg 1m   0.42
Load Avg 5m   0.38
Load Avg 15m  0.31

FIELD      VALUE
-----      -----
Total RAM  8.0 GB
Used RAM   2.0 GB
Free RAM   6.0 GB
RAM Usage  25.0%
Swap Total 2.0 GB
Swap Used  0 B
```

#### `plsctl system cpu`

Show CPU statistics only.

```bash
plsctl system cpu
```

```
FIELD         VALUE
-----         -----
CPU Model     Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz
Core Count    4
Usage         12.5%
Load Avg 1m   0.42
Load Avg 5m   0.38
Load Avg 15m  0.31
```

#### `plsctl system memory`

Show memory (RAM and swap) statistics.

```bash
plsctl system memory
```

```
FIELD      VALUE
-----      -----
Total RAM  8.0 GB
Used RAM   2.0 GB
Free RAM   6.0 GB
RAM Usage  25.0%
Swap Total 2.0 GB
Swap Used  0 B
```

#### `plsctl system disk`

Show disk usage for all mounted partitions.

```bash
plsctl system disk
```

```
DEVICE     MOUNTPOINT  FS    TOTAL    USED     FREE     USE%
------     ----------  --    -----    ----     ----     ----
/dev/sda1  /           ext4  100.0 GB  20.0 GB  80.0 GB  20.0%
/dev/sda2  /boot       ext4  1.0 GB    200.0 MB 824.0 MB 19.5%
```

#### `plsctl system network`

Show network interface I/O statistics.

```bash
plsctl system network
```

```
INTERFACE  IP ADDRESSES        BYTES RECV  BYTES SENT  UP
---------  ------------        ----------  ----------  --
eth0       192.168.1.10/24     2.0 MB      1.0 MB      yes
lo         127.0.0.1/8         512.0 KB    512.0 KB    yes
```

---

### `plsctl service`

Manage systemd services on the VM.

#### `plsctl service list`

List all loaded systemd services.

```bash
plsctl service list
```

```
NAME                    STATE     SUBSTATE  ENABLED  PID
----                    -----     --------  -------  ---
nginx.service           active    running   no       1234
ssh.service             active    running   no       987
cron.service            active    running   no       456
snapd.service           inactive  dead      no       0
plusclouds-agent.service active   running   no       789
```

#### `plsctl service get <name>`

Show details for a single service.

```bash
plsctl service get nginx
```

```
FIELD        VALUE
-----        -----
Name         nginx.service
Description  A high performance web server and reverse proxy server
State        active
Sub-State    running
Enabled      no
PID          1234
```

#### `plsctl service start <name>`

Start a service.

```bash
plsctl service start nginx
```

```
✓ Service nginx.service started successfully.
```

#### `plsctl service stop <name>`

Stop a service.

```bash
plsctl service stop nginx
```

```
✓ Service nginx.service stopped successfully.
```

#### `plsctl service restart <name>`

Restart a service.

```bash
plsctl service restart nginx
```

```
✓ Service nginx.service restarted successfully.
```

#### `plsctl service reload <name>`

Reload a service's configuration without restarting.

```bash
plsctl service reload nginx
```

```
✓ Service nginx.service reloaded successfully.
```

#### `plsctl service enable <name>`

Enable a service to start automatically on boot.

```bash
plsctl service enable nginx
```

```
✓ Service nginx.service enabled successfully.
```

#### `plsctl service disable <name>`

Prevent a service from starting automatically on boot.

```bash
plsctl service disable nginx
```

```
✓ Service nginx.service disabled successfully.
```

**Error example (service not found):**

```bash
plsctl service start nonexistent
```

```
ERROR: [SERVICE_NOT_FOUND] unit not found: nonexistent.service
```

---

### `plsctl metadata`

Show VM ISO metadata.

#### `plsctl metadata show`

Show all available metadata (instance, network, services manifest).
Credentials are never included.

```bash
plsctl metadata show
```

```json
{
  "instance": {
    "vm_id": "vm-abc123",
    "tenant_id": "tenant-xyz",
    "tenant_name": "Acme Corp",
    "datacenter": "ist-1",
    "region": "eu-west",
    "plan_tier": "standard-2vcpu-4gb",
    "tags": { "env": "production" }
  },
  "network": {
    "ip_address": "192.168.1.10",
    "gateway": "192.168.1.1",
    "dns": ["8.8.8.8", "8.8.4.4"],
    "hostname": "web-01",
    "domain": "tenant.plusclouds.net"
  },
  "services": {
    "services": [
      { "name": "nginx.service", "enabled": true, "config": {} }
    ]
  }
}
```

#### `plsctl metadata instance`

Show VM identity metadata in table format.

```bash
plsctl metadata instance
```

```
FIELD        VALUE
-----        -----
VM ID        vm-abc123
Tenant ID    tenant-xyz
Tenant Name  Acme Corp
Datacenter   ist-1
Region       eu-west
Plan Tier    standard-2vcpu-4gb
```

#### `plsctl metadata network`

Show network configuration metadata.

```bash
plsctl metadata network
```

```
FIELD      VALUE
-----      -----
Hostname   web-01
Domain     tenant.plusclouds.net
IP Address 192.168.1.10
Gateway    192.168.1.1
DNS        8.8.8.8, 8.8.4.4
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error (connection failure, API error, invalid arguments) |
