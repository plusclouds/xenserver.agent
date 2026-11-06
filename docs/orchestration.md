# PlusClouds Agent — Orchestration Team Reference

> **Partly outdated.** The ISO config drive provisioning described here is still current, but endpoints and REST calls to `/api/v1/...` belong to the earlier REST design and no longer exist. For controlling the agent today, see the [README](../README.md), [https-api.md](https-api.md) and [operations.md](operations.md).

This document is the authoritative reference for the PlusClouds orchestration
layer team. It covers the complete contract between the orchestrator and the
agent: token lifecycle, ISO provisioning, every endpoint the agent calls, every
endpoint the orchestrator calls on the agent, error handling, security model,
and the expected VM lifecycle.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Token Lifecycle](#2-token-lifecycle)
3. [ISO Config Drive Provisioning](#3-iso-config-drive-provisioning)
4. [Endpoints the Agent Calls (Outbound)](#4-endpoints-the-agent-calls-outbound)
   - [POST /agents/register](#post-agentsregister)
   - [POST /agents/heartbeat](#post-agentsheartbeat)
5. [Endpoints the Orchestrator Calls on the Agent (Inbound)](#5-endpoints-the-orchestrator-calls-on-the-agent-inbound)
   - [Health & Liveness](#health--liveness)
   - [System Information](#system-information)
   - [Service Management](#service-management)
   - [VM Metadata](#vm-metadata)
   - [Prometheus Metrics](#prometheus-metrics-scraping)
6. [Agent Startup Sequence](#6-agent-startup-sequence)
7. [VM Lifecycle](#7-vm-lifecycle)
8. [Capabilities Reference](#8-capabilities-reference)
9. [Error Codes & Handling](#9-error-codes--handling)
10. [Security Model](#10-security-model)
11. [Prometheus Integration](#11-prometheus-integration)
12. [Roadmap](#12-roadmap)

---

## 1. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        PlusClouds Platform                      │
│                                                                 │
│  ┌──────────────────┐         ┌───────────────────────────┐    │
│  │   Orchestrator   │◄───────►│    Control Plane API      │    │
│  │  (provisioning,  │         │  POST /agents/register    │    │
│  │   scheduling,    │         │  POST /agents/heartbeat   │    │
│  │   fleet mgmt)    │         └───────────────────────────┘    │
│  └────────┬─────────┘                                          │
│           │ creates ISO                                         │
└───────────┼─────────────────────────────────────────────────────┘
            │ attaches ISO to VM
            ▼
┌───────────────────────────────────────────────────────────────────────┐
│  Ubuntu 24 VM                                                         │
│                                                                       │
│  ┌─────────────────┐    reads     ┌──────────────────────────────┐   │
│  │  ISO Config     │◄─────────────│   plusclouds-agent daemon    │   │
│  │  Drive          │              │                              │   │
│  │  /media/plsc..  │              │  HTTP  :8080  (inbound API)  │   │
│  │  ├ instance.json│              │  gRPC  :8081  (inbound API)  │   │
│  │  ├ network.json │              │  Prom  :8080/metrics         │   │
│  │  ├ services.json│              └──────────────────────────────┘   │
│  │  ├ credentials  │                        │outbound                │
│  │  └ user-data    │                        ▼                        │
│  └─────────────────┘            Control Plane API                    │
│                                 POST /agents/register                 │
│                                 POST /agents/heartbeat                │
└───────────────────────────────────────────────────────────────────────┘
```

**Key principle:** The agent never trusts its own local config for identity or
credentials. All secrets and identity come from the ISO config drive attached
by the orchestrator at VM creation time.

---

## 2. Token Lifecycle

Three distinct tokens are used. Understanding their separation is critical.

```
Provisioning time                  Runtime                    Expiry
──────────────┬────────────────────────┬────────────────────────────────►
              │                        │
   Orchestrator creates ISO            Agent boots
   with agent_token                    │
              │                        ├─► reads agent_token from ISO
              │                        ├─► POST /agents/register
              │                        │     Authorization: Bearer {agent_token}
              │                        │
              │                        ├─► Orchestrator validates agent_token
              │                        │   returns session_token + expires_at
              │                        │
              │                        ├─► agent stores session_token in memory
              │                        │
              │                        ├─► starts HTTP/gRPC servers
              │                        │
              │                        └─► heartbeat loop:
              │                              POST /agents/heartbeat
              │                                Authorization: Bearer {session_token}
```

| Token | Name in ISO | Who creates it | Used for | Lifetime |
|-------|-------------|----------------|----------|----------|
| **AgentToken** | `credentials.agent_token` | Orchestrator (at VM provisioning time) | One-time: authenticating the registration request | Until registration succeeds (rotate on re-provision) |
| **SessionToken** | — (not in ISO) | Control Plane (returned in registration response) | Heartbeats and future outbound orchestrator calls | Short-lived; `expires_at` in response |
| **APIKey** | `credentials.api_key` | Orchestrator (at VM provisioning time) | Authenticating **inbound** HTTP/gRPC requests **to** the agent | VM lifetime; rotate by pushing new ISO or via future RotateKey RPC |

> The `APIKey` and `AgentToken` can be the same value for simplicity, but
> keeping them separate allows rotating them independently.

---

## 3. ISO Config Drive Provisioning

The orchestrator must create a vFAT ISO image and attach it to the VM before
first boot. The agent reads this ISO on startup.

### Mount path

The agent expects the ISO to be mounted at:
```
/media/plusclouds-config
```

This is configurable via `iso.mount_path` in `agent.yaml` or the environment
variable `PLUSCLOUDS_AGENT_ISO_MOUNT_PATH`.

### Required files

| File | Required | Description |
|------|----------|-------------|
| `instance.json` | Yes | VM identity |
| `network.json` | Yes | Network configuration |
| `credentials.json` | Yes | Tokens and control plane URL |
| `services.json` | No | Service manifest (used in Phase 2) |
| `user-data` | No | Cloud-init compatible bootstrap script |

### `instance.json`

```json
{
  "vm_id":       "vm-abc123",
  "tenant_id":   "tenant-xyz",
  "tenant_name": "Acme Corp",
  "datacenter":  "ist-1",
  "region":      "eu-west",
  "plan_tier":   "standard-2vcpu-4gb",
  "tags": {
    "env":  "production",
    "role": "web"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `vm_id` | string | **yes** | Globally unique VM identifier. Used as the agent's identity in all orchestrator calls |
| `tenant_id` | string | **yes** | Tenant identifier. Included in every outbound call |
| `tenant_name` | string | no | Human-readable tenant name |
| `datacenter` | string | **yes** | Datacenter slug (e.g. `ist-1`, `ams-2`) |
| `region` | string | **yes** | Geographic region slug (e.g. `eu-west`, `tr-east`) |
| `plan_tier` | string | no | Compute plan slug (e.g. `standard-2vcpu-4gb`) |
| `tags` | object | no | Arbitrary key/value labels forwarded as `labels` in the registration payload |

### `network.json`

```json
{
  "ip_address": "192.168.1.10",
  "gateway":    "192.168.1.1",
  "dns":        ["8.8.8.8", "8.8.4.4"],
  "hostname":   "web-01",
  "domain":     "tenant.plusclouds.net"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ip_address` | string | **yes** | Primary IPv4 address; forwarded in the registration payload as `ip_address` |
| `gateway` | string | **yes** | Default gateway |
| `dns` | array of strings | no | DNS resolvers |
| `hostname` | string | **yes** | Short hostname; forwarded in the registration payload as `hostname` |
| `domain` | string | no | DNS search domain |

### `credentials.json`

```json
{
  "api_key":           "apk_live_abc123...",
  "control_plane_url": "https://api.plusclouds.com",
  "agent_token":       "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `api_key` | string | **yes** | Shared secret for authenticating **inbound** calls to the agent's HTTP/gRPC API. The orchestrator must use this key when calling any agent endpoint |
| `control_plane_url` | string | **yes** (for production) | Base URL of the control plane. If empty, the agent runs in local-only mode |
| `agent_token` | string | **yes** (if `control_plane_url` is set) | One-time provisioning token. The agent uses this as the Bearer token in the registration request. Should be a signed JWT containing at minimum `vm_id`, `tenant_id`, `iat`, and `exp` claims |

### `services.json`

```json
{
  "services": [
    { "name": "nginx.service",             "enabled": true, "config": {} },
    { "name": "plusclouds-agent.service",  "enabled": true, "config": {} },
    { "name": "postgresql.service",        "enabled": false, "config": {
        "max_connections": "200"
    }}
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `services[].name` | string | yes | Systemd unit name (`.service` suffix required) |
| `services[].enabled` | bool | yes | Whether to enable the unit at boot |
| `services[].config` | object | no | Service-specific key/value config (used by Phase 2 service modules) |

### Building the ISO

```bash
mkdir -p /tmp/plusclouds-config

# Write all required files...
# (see examples above)

genisoimage \
  -o /var/lib/plusclouds/isos/${VM_ID}-config.iso \
  -V plusclouds-config \
  -r -J \
  /tmp/plusclouds-config/

# Attach to the VM as a secondary disk (e.g. via libvirt/QEMU)
virsh attach-disk ${VM_ID} \
  /var/lib/plusclouds/isos/${VM_ID}-config.iso \
  sdb --type cdrom --config
```

---

## 4. Endpoints the Agent Calls (Outbound)

These are endpoints the **orchestrator must implement**. The agent calls them.

### POST /agents/register

Called once at agent startup. This is a **mandatory gate** — the agent will not
start its API servers until registration succeeds.

**URL:** `{control_plane_url}/agents/register`

**Method:** `POST`

**Headers:**
```
Authorization: Bearer {agent_token}
Content-Type: application/json
```

**Request body:**

```json
{
  "vm_id":         "vm-abc123",
  "tenant_id":     "tenant-xyz",
  "hostname":      "web-01",
  "ip_address":    "192.168.1.10",
  "agent_version": "0.1.0",
  "capabilities": [
    "system.info",
    "system.metrics",
    "services.manage",
    "metadata.read",
    "grpc.v1"
  ],
  "labels": {
    "env":  "production",
    "role": "web"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `vm_id` | string | From `instance.json` |
| `tenant_id` | string | From `instance.json` |
| `hostname` | string | From OS (`/proc/sys/kernel/hostname`); may differ from `network.json` hostname |
| `ip_address` | string | From `network.json` |
| `agent_version` | string | Semantic version of the running agent binary |
| `capabilities` | array | See [Capabilities Reference](#8-capabilities-reference) |
| `labels` | object | From `instance.json.tags`; may be null if no tags set |

**Required success response:** HTTP `200` or `201`

```json
{
  "agent_id":      "vm-abc123",
  "session_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_at":    1712432078,
  "message":       "Agent registered successfully"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent_id` | string | **yes** | Must match the `vm_id` sent in the request. The agent logs this as confirmation |
| `session_token` | string | **yes** | Token the agent will use on all subsequent outbound calls. Must be non-empty or the agent treats the registration as failed and retries |
| `expires_at` | int64 | **yes** | Unix timestamp of session expiry. Used for logging; automatic renewal will be added in a future version |
| `message` | string | no | Human-readable string. Logged at INFO level |

**Error responses:**

| Status | Meaning | Agent behaviour |
|--------|---------|-----------------|
| `400` | Malformed request body | Hard failure — exits immediately |
| `401` | `agent_token` is invalid or expired | Hard failure — exits immediately |
| `403` | VM not provisioned / tenant mismatch | Hard failure — exits immediately |
| `404` | Unknown VM | Hard failure — exits immediately |
| `409` | Already registered (idempotent re-register) | **Return 200 with a new session_token** — agent treats this as success |
| `5xx` | Transient orchestrator error | Retried with exponential backoff (see below) |

**Retry behaviour:**

```
Attempt 1 → wait 5s
Attempt 2 → wait 10s
Attempt 3 → wait 20s
Attempt 4 → wait 40s
Attempt 5 → wait 60s
→ agent exits
```

4xx responses are **never retried**.

---

### POST /agents/heartbeat

Called periodically by the agent (default: every 30 s) after successful
registration. Authenticated with the `session_token` issued at registration.

**URL:** `{control_plane_url}/agents/heartbeat`

**Method:** `POST`

**Headers:**
```
Authorization: Bearer {session_token}
Content-Type: application/json
```

**Request body:**

```json
{
  "vm_id":          "vm-abc123",
  "tenant_id":      "tenant-xyz",
  "timestamp":      1712345678,
  "uptime_seconds": 86432,
  "cpu_percent":    12.5,
  "memory_percent": 25.0,
  "agent_version":  "0.1.0"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `vm_id` | string | VM identifier |
| `tenant_id` | string | Tenant identifier |
| `timestamp` | int64 | Unix timestamp of this heartbeat |
| `uptime_seconds` | int64 | System uptime in seconds |
| `cpu_percent` | float64 | CPU usage percentage (0–100) at time of heartbeat |
| `memory_percent` | float64 | RAM usage percentage (0–100) at time of heartbeat |
| `agent_version` | string | Running agent version |

**Expected response:** HTTP `200` or `204`. Response body is not read by the agent.

**Error responses:**

| Status | Meaning | Agent behaviour |
|--------|---------|-----------------|
| `200` / `204` | OK | Continue |
| `401` | `session_token` expired or invalid | Logs a warning; continues (auto-renewal in future version) |
| `5xx` | Transient error | Logs a warning; retries on the next tick |

> The heartbeat interval is configurable via `registry.heartbeat_interval` in
> `agent.yaml` or `PLUSCLOUDS_AGENT_REGISTRY_HEARTBEAT_INTERVAL` (e.g. `30s`, `1m`).

---

## 5. Endpoints the Orchestrator Calls on the Agent (Inbound)

The orchestrator controls VMs by calling the agent's HTTP API. All requests
require the `api_key` from `credentials.json`.

**Base URL:** `http://{vm-ip}:8080`

**Auth header (either form):**
```
Authorization: Bearer {api_key}
X-API-Key: {api_key}
```

**Standard response envelope:**
```json
{
  "success": true,
  "data":    { ... },
  "error":   null,
  "meta": {
    "version":   "0.1.0",
    "agent_id":  "vm-abc123",
    "timestamp": 1712345678
  }
}
```

On error:
```json
{
  "success": false,
  "data":    null,
  "error": {
    "code":    "SERVICE_NOT_FOUND",
    "message": "unit nginx.service not found"
  },
  "meta": { ... }
}
```

---

### Health & Liveness

#### GET /healthz

Liveness probe. Returns `200` if the agent process is running.

```bash
curl -H "X-API-Key: {api_key}" http://192.168.1.10:8080/healthz
```

```json
{
  "status":  "ok",
  "version": "0.1.0"
}
```

> Use this as a Kubernetes liveness probe or load-balancer health check.
> Supply `X-API-Key` via `httpHeaders` in the probe spec.

---

### System Information

#### GET /api/v1/system/info

Returns OS-level identity information for the VM.

```bash
curl -H "Authorization: Bearer {api_key}" \
  http://192.168.1.10:8080/api/v1/system/info
```

```json
{
  "success": true,
  "data": {
    "hostname":       "web-01",
    "os":             "Ubuntu 24.04 LTS",
    "kernel_version": "6.8.0-31-generic",
    "architecture":   "x86_64",
    "uptime":         86432,
    "boot_time":      1712259246,
    "vm_id":          "vm-abc123",
    "tenant_id":      "tenant-xyz"
  }
}
```

#### GET /api/v1/system/metrics

Returns all resource metrics in a single call.

```json
{
  "success": true,
  "data": {
    "cpu": {
      "usage_percent": 12.5,
      "core_count":    4,
      "model_name":    "Intel(R) Xeon(R) Gold 6226R CPU @ 2.90GHz",
      "load_avg_1":    0.42,
      "load_avg_5":    0.38,
      "load_avg_15":   0.31
    },
    "memory": {
      "total_bytes":    8589934592,
      "used_bytes":     2147483648,
      "free_bytes":     6442450944,
      "usage_percent":  25.0,
      "swap_total":     2147483648,
      "swap_used":      0
    },
    "disk": {
      "partitions": [
        {
          "device":        "/dev/sda1",
          "mountpoint":    "/",
          "fstype":        "ext4",
          "total_bytes":   53687091200,
          "used_bytes":    10737418240,
          "free_bytes":    42949672960,
          "usage_percent": 20.0
        }
      ]
    },
    "network": {
      "interfaces": [
        {
          "name":         "eth0",
          "ip_addresses": ["192.168.1.10/24"],
          "bytes_sent":   1048576,
          "bytes_recv":   2097152,
          "packets_sent": 1024,
          "packets_recv": 2048,
          "is_up":        true
        }
      ]
    },
    "collected_at": 1712345678
  }
}
```

**Individual metric endpoints** (same auth, same envelope):

| Endpoint | Returns |
|----------|---------|
| `GET /api/v1/system/cpu` | `CPUStats` only |
| `GET /api/v1/system/memory` | `MemoryStats` only |
| `GET /api/v1/system/disk` | `DiskStats` only |
| `GET /api/v1/system/network` | `NetworkStats` only |

---

### Service Management

#### GET /api/v1/services

Lists all systemd units loaded on the VM.

```bash
curl -H "Authorization: Bearer {api_key}" \
  http://192.168.1.10:8080/api/v1/services
```

```json
{
  "success": true,
  "data": [
    {
      "name":        "nginx.service",
      "description": "A high performance web server and a reverse proxy server",
      "state":       "active",
      "sub_state":   "running",
      "enabled":     true,
      "pid":         1234,
      "since":       1712259300
    },
    {
      "name":        "postgresql.service",
      "description": "PostgreSQL Database Server",
      "state":       "inactive",
      "sub_state":   "dead",
      "enabled":     false,
      "pid":         0,
      "since":       0
    }
  ]
}
```

**Service state values:** `active` | `inactive` | `failed` | `unknown`

#### GET /api/v1/services/{name}

Returns state for a single service. The `.service` suffix is optional.

```bash
curl -H "Authorization: Bearer {api_key}" \
  http://192.168.1.10:8080/api/v1/services/nginx
```

#### POST /api/v1/services/{name}/{action}

Performs a lifecycle action on a service.

| Action | Equivalent to | Notes |
|--------|---------------|-------|
| `start` | `systemctl start` | No-op if already running |
| `stop` | `systemctl stop` | No-op if already stopped |
| `restart` | `systemctl restart` | Always restarts |
| `reload` | `systemctl reload` | Reloads config without restart; fails if unit does not support reload |
| `enable` | `systemctl enable` | Enables at boot |
| `disable` | `systemctl disable` | Disables at boot |

```bash
curl -X POST \
  -H "Authorization: Bearer {api_key}" \
  http://192.168.1.10:8080/api/v1/services/nginx/restart
```

```json
{
  "success": true,
  "data": {
    "service": "nginx.service",
    "action":  "restart",
    "success": true,
    "message": "job completed: done"
  }
}
```

On failure:
```json
{
  "success": false,
  "error": {
    "code":    "SERVICE_ACTION_FAILED",
    "message": "job completed: failed"
  }
}
```

**HTTP status codes for service actions:**

| Status | Meaning |
|--------|---------|
| `200` | Action completed (check `data.success` for outcome) |
| `404` | Unit not found on this VM |
| `422` | systemd job returned a non-done status |
| `500` | D-Bus communication error |

---

### VM Metadata

Returns the ISO config drive metadata as read by the agent. Credentials are
**never** included in these responses.

#### GET /api/v1/metadata

Returns all available metadata in one response.

```json
{
  "success": true,
  "data": {
    "instance": {
      "vm_id":       "vm-abc123",
      "tenant_id":   "tenant-xyz",
      "tenant_name": "Acme Corp",
      "datacenter":  "ist-1",
      "region":      "eu-west",
      "plan_tier":   "standard-2vcpu-4gb",
      "tags":        { "env": "production", "role": "web" }
    },
    "network": {
      "ip_address": "192.168.1.10",
      "gateway":    "192.168.1.1",
      "dns":        ["8.8.8.8", "8.8.4.4"],
      "hostname":   "web-01",
      "domain":     "tenant.plusclouds.net"
    },
    "services": {
      "services": [
        { "name": "nginx.service", "enabled": true, "config": {} }
      ]
    }
  }
}
```

| Endpoint | Returns |
|----------|---------|
| `GET /api/v1/metadata` | All metadata (no credentials) |
| `GET /api/v1/metadata/instance` | `InstanceMetadata` only |
| `GET /api/v1/metadata/network` | `NetworkMetadata` only |
| `GET /api/v1/metadata/services` | `ServicesManifest` only |

---

### Prometheus Metrics Scraping

```
GET http://{vm-ip}:8080/metrics
Authorization: Bearer {api_key}
```

Authentication is required. See [Section 11](#11-prometheus-integration) for
the full scrape configuration and metrics reference.

---

## 6. Agent Startup Sequence

```
VM boots
    │
    ├─► Load agent.yaml config
    ├─► Init zap logger
    ├─► Read ISO from /media/plusclouds-config
    │     ├─ instance.json  → vm_id, tenant_id, labels
    │     ├─ network.json   → ip_address
    │     └─ credentials.json → api_key, control_plane_url, agent_token
    │
    ├─► Merge ISO credentials into runtime config
    │     (ISO values override agent.yaml values)
    │
    ├─► Connect to systemd D-Bus
    │
    ├─► Init system module (gopsutil)
    ├─► Init services module (go-systemd/dbus)
    │
    ├─► ╔══════════════════════════════════════════╗
    │   ║  MANDATORY GATE: Register with           ║
    │   ║  orchestrator via POST /agents/register  ║
    │   ║                                          ║
    │   ║  SUCCESS → store session_token           ║
    │   ║  ErrNoControlPlane → local-only mode     ║
    │   ║  Any other error → EXIT (non-zero)       ║
    │   ╚══════════════════════════════════════════╝
    │
    ├─► Start heartbeat loop (background goroutine)
    ├─► Register Prometheus metrics
    │
    ├─► Start HTTP server :8080  ← only reached after registration
    ├─► Start gRPC server :8081  ← only reached after registration
    │
    └─► Wait for SIGINT / SIGTERM → graceful shutdown
```

---

## 7. VM Lifecycle

### Provisioning a new VM

```
Orchestrator                                 VM
─────────────                                ──
1. Allocate vm_id, api_key, agent_token
2. Create ISO config drive
3. Attach ISO to VM
4. Boot VM
                                             5. Agent reads ISO
                                             6. POST /agents/register
5. Validate agent_token                ◄────
6. Record agent in inventory
7. Return session_token + expires_at   ────►
                                             8. Start HTTP/gRPC servers
                                             9. Begin heartbeat loop
10. Poll /api/v1/system/info    ────────────►
11. Monitor heartbeats
```

### Controlling a running VM

```
Orchestrator                                 Agent
─────────────                                ─────
POST /api/v1/services/nginx/restart  ───────►
                                             systemd restart nginx.service
◄─────────────────────────────────── return ActionResult{success: true}
```

### Deprovisioning a VM

```
Orchestrator                                 Agent
─────────────                                ─────
1. Stop workloads via service API    ───────►
2. Send SIGTERM to agent process     ───────► graceful shutdown
3. Detach and delete ISO
4. Destroy VM
5. Mark agent record as deprovisioned in inventory
```

---

## 8. Capabilities Reference

Capabilities are reported by the agent in the registration payload. The
orchestrator should store these and use them to know which operations are
safe to call on a given agent version.

| Capability | Meaning | Endpoints |
|------------|---------|-----------|
| `system.info` | Can return OS info and uptime | `GET /api/v1/system/info` |
| `system.metrics` | Can return CPU/RAM/disk/network metrics | `GET /api/v1/system/metrics` and sub-endpoints |
| `services.manage` | Can list and control systemd units | `GET/POST /api/v1/services/...` |
| `metadata.read` | Can serve ISO metadata via API | `GET /api/v1/metadata/...` |
| `grpc.v1` | gRPC server is running (Phase 1 stub; no services registered yet) | `:8081` |

**Planned capabilities (Phase 2+):**

| Capability | Phase | Endpoints |
|------------|-------|-----------|
| `packages.manage` | 2 | `GET/POST /api/v1/packages/...` |
| `logs.stream` | 2 | `GET /api/v1/logs/stream` (SSE) |
| `network.manage` | 2 | `GET/POST /api/v1/network/...` |
| `users.manage` | 2 | `GET/POST /api/v1/users/...` |
| `docker.manage` | 3 | `GET/POST /api/v1/docker/...` |
| `swarm.manage` | 3 | `GET/POST /api/v1/swarm/...` |
| `kubernetes.manage` | 4 | `GET/POST /api/v1/kubernetes/...` |
| `nginx.manage` | 5 | `GET/POST /api/v1/nginx/...` |
| `postgresql.manage` | 5 | `GET/POST /api/v1/postgresql/...` |
| `redis.manage` | 5 | `GET/POST /api/v1/redis/...` |

---

## 9. Error Codes & Handling

### Registration error codes (outbound, 4xx)

The orchestrator should return structured errors:

```json
{
  "error": {
    "code":    "TOKEN_EXPIRED",
    "message": "agent_token has expired"
  }
}
```

| Code | HTTP Status | Meaning |
|------|-------------|---------|
| `TOKEN_EXPIRED` | 401 | `agent_token` JWT is past its `exp` claim |
| `TOKEN_INVALID` | 401 | `agent_token` signature verification failed |
| `VM_NOT_FOUND` | 404 | No VM with this `vm_id` is provisioned |
| `TENANT_MISMATCH` | 403 | `tenant_id` in body does not match token claim |
| `ALREADY_REGISTERED` | 409 | VM is already registered — **return 200 with new session_token** |

### Agent API error codes (inbound)

| Code | HTTP Status | Meaning |
|------|-------------|---------|
| `MISSING_CREDENTIALS` | 401 | No `Authorization` or `X-API-Key` header |
| `INVALID_CREDENTIALS` | 401 | API key does not match |
| `AGENT_NOT_CONFIGURED` | 401 | Agent has no API key configured (ISO not read yet) |
| `SERVICE_NOT_FOUND` | 404 | systemd unit does not exist on this VM |
| `SERVICE_ACTION_FAILED` | 422 | systemd job returned a non-done result |
| `DBUS_ERROR` | 500 | Failed to communicate with systemd over D-Bus |
| `INTERNAL_ERROR` | 500 | Unhandled agent error |

---

## 10. Security Model

### Phase 1 (current)

| Concern | Implementation |
|---------|----------------|
| Agent identity | ISO `agent_token` (signed JWT) presented at registration |
| Inbound auth (orchestrator → agent) | Shared `api_key` per VM, constant-time comparison |
| Outbound auth (agent → orchestrator) | `session_token` as Bearer token, issued at registration |
| Transport | Plain HTTP (TLS termination expected at network boundary) |
| Secret storage | Credentials held in memory only; never written to disk by the agent |

### Recommendations for the orchestrator

- **`agent_token`** should be a short-lived JWT (TTL: 10–30 minutes) signed with the
  orchestrator's private key. Include `vm_id`, `tenant_id`, `iat`, and `exp` claims.
  The agent is expected to register within the TTL of the first boot.

- **`session_token`** should also be a JWT with a reasonable TTL (e.g. 24 hours).
  Set `expires_at` accurately so future agent versions can renew proactively.

- **`api_key`** should be a high-entropy random token (32+ bytes, base64url encoded).
  It is long-lived — rotate it by pushing a new ISO or via the planned `RotateKey` RPC.

- All inbound traffic to `:8080`/`:8081` should be restricted to the orchestrator's
  IP range at the network/firewall level. The agent does not implement IP allowlisting.

### Phase 2 (planned)

- mTLS on gRPC: client certificates issued by the PlusClouds CA (CA cert in ISO)
- TLS on HTTP: self-signed cert or ACME cert on the agent
- `RotateKey` gRPC RPC: push a new `api_key` without VM restart
- Automatic `session_token` renewal before expiry

---

## 11. Prometheus Integration

The agent exposes a standard Prometheus metrics endpoint. Authentication is
required (same `api_key`).

```
GET http://{vm-ip}:8080/metrics
Authorization: Bearer {api_key}
```

### Available metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `plusclouds_agent_cpu_usage_percent` | Gauge | — | CPU usage % |
| `plusclouds_agent_memory_usage_percent` | Gauge | — | RAM usage % |
| `plusclouds_agent_disk_usage_percent` | Gauge | `mountpoint` | Disk usage % per partition |
| `plusclouds_agent_service_state` | Gauge | `name`, `state` | 1 if the service is in this state |
| `plusclouds_agent_http_requests_total` | Counter | `method`, `path`, `status` | Inbound HTTP request count |
| `plusclouds_agent_http_request_duration_seconds` | Histogram | `method`, `path` | Inbound request latency |
| `plusclouds_agent_heartbeats_total` | Counter | — | Outbound heartbeats sent |

### Prometheus scrape config

```yaml
scrape_configs:
  - job_name: 'plusclouds-agents'
    authorization:
      credentials: 'your-api-key-here'
    static_configs:
      - targets:
          - '192.168.1.10:8080'
          - '192.168.1.11:8080'
    relabel_configs:
      - source_labels: [__address__]
        target_label: instance
      - source_labels: [__address__]
        regex: '([^:]+):.*'
        replacement: '$1'
        target_label: vm_ip
```

For dynamic fleet scraping with service discovery, the orchestrator can expose
a Prometheus HTTP SD endpoint that lists all registered agents.

---

## 12. Roadmap

| Phase | New capabilities | New outbound calls | New inbound endpoints |
|-------|-----------------|-------------------|----------------------|
| **1** *(current)* | system.info, system.metrics, services.manage, metadata.read | `/agents/register`, `/agents/heartbeat` | `/api/v1/system/*`, `/api/v1/services/*`, `/api/v1/metadata/*` |
| **2** | packages.manage, logs.stream, network.manage, users.manage | — | `/api/v1/packages/*`, `/api/v1/logs/stream`, `/api/v1/network/*`, `/api/v1/users/*` |
| **3** | docker.manage, swarm.manage, autoheal | `/agents/events` (push) | `/api/v1/docker/*`, `/api/v1/swarm/*` |
| **4** | kubernetes.manage | — | `/api/v1/kubernetes/*` |
| **5** | nginx.manage, postgresql.manage, redis.manage, snapshot quiesce | `/agents/snapshot/ack` | `/api/v1/nginx/*`, `/api/v1/postgresql/*`, `/api/v1/redis/*`, `/api/v1/snapshot/prepare` |
| **6** | mTLS gRPC, RotateKey RPC, session renewal | — | gRPC `AgentService`, `ServiceManagerService`, `EventService` |
