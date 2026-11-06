# PlusClouds Agent Architecture

> **Partly outdated.** The ISO config drive provisioning described here is still current, but endpoints and REST calls to `/api/v1/...` belong to the earlier REST design and no longer exist. For controlling the agent today, see the [README](../README.md), [https-api.md](https-api.md) and [operations.md](operations.md).

## Overview

The PlusClouds agent (`plusclouds-agent`) is a Go daemon that runs on every
Ubuntu 24 VM managed by the PlusClouds Cloud Service Provider platform. It
provides real-time system metrics, remote service management, and maintains a
live connection to the PlusClouds control plane.

---

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        plusclouds-agent (daemon)                        │
│                                                                         │
│  ┌──────────────┐    ┌──────────────────────────────────────────────┐  │
│  │  ISO Config  │    │              Module Layer                    │  │
│  │    Drive     │───▶│  ┌──────────────┐  ┌──────────────────────┐ │  │
│  │  (vFAT ISO)  │    │  │ system.Module│  │  services.Module     │ │  │
│  │              │    │  │  (gopsutil)  │  │  (go-systemd/dbus)   │ │  │
│  │ instance.json│    │  └──────┬───────┘  └──────────┬───────────┘ │  │
│  │ network.json │    │         │                     │             │  │
│  │ services.json│    └─────────┼─────────────────────┼─────────────┘  │
│  │credentials.js│              │                     │                 │
│  │ user-data    │    ┌─────────▼─────────────────────▼─────────────┐  │
│  └──────────────┘    │             Event Bus                        │  │
│                      │  ServiceStarted / ServiceStopped /           │  │
│                      │  ServiceFailed / MetricThresholdExceeded /   │  │
│                      │  AgentRegistered / ConfigDriftDetected       │  │
│                      └───────────────────────┬─────────────────────┘  │
│                                              │                         │
│  ┌───────────────────────────────────────────▼─────────────────────┐  │
│  │                        API Layer                                 │  │
│  │                                                                  │  │
│  │  ┌─────────────────────────────────┐  ┌──────────────────────┐  │  │
│  │  │         HTTP Server             │  │    gRPC Server       │  │  │
│  │  │         (Chi router)            │  │  (Phase 1 stub)      │  │  │
│  │  │  :8080                          │  │  :8081               │  │  │
│  │  │                                 │  │                      │  │  │
│  │  │  GET /healthz     (no auth)     │  │  Auth interceptor    │  │  │
│  │  │  GET /metrics     (no auth)     │  │  Log interceptor     │  │  │
│  │  │  /api/v1/system/...             │  │                      │  │  │
│  │  │  /api/v1/services/...           │  └──────────────────────┘  │  │
│  │  │  /api/v1/metadata/...           │                            │  │
│  │  └─────────────────────────────────┘                            │  │
│  └──────────────────────────────────────────────────────────────────┘  │
│                                                                         │
│  ┌────────────────────────┐   ┌──────────────────────────────────────┐ │
│  │  Telemetry / Prometheus│   │           Registry                   │ │
│  │  :9100                 │   │  - Register() on startup             │ │
│  │  cpu_usage_percent     │   │  - StartHeartbeat() ticker loop      │ │
│  │  memory_usage_percent  │   │  - POST to control plane             │ │
│  │  disk_usage_percent    │   └──────────────────────────────────────┘ │
│  │  service_state         │                                            │
│  │  http_requests_total   │                                            │
│  └────────────────────────┘                                            │
└─────────────────────────────────────────────────────────────────────────┘
         │                          │                       │
         ▼                          ▼                       ▼
  ┌─────────────┐          ┌──────────────┐       ┌──────────────────┐
  │  systemd    │          │  Prometheus  │       │  PlusClouds      │
  │  D-Bus      │          │  (scrapes    │       │  Control Plane   │
  │             │          │   /metrics)  │       │  (registration + │
  └─────────────┘          └──────────────┘       │   heartbeats)    │
                                                  └──────────────────┘
```

---

## ISO Config Drive Format

The config drive is a vFAT-formatted ISO image attached to the VM as a
secondary block device. Ubuntu's cloud-init or a udev rule mounts it at
`/media/plusclouds-config`.

### File Structure

```
/media/plusclouds-config/
├── instance.json      # VM identity metadata
├── network.json       # Network configuration
├── services.json      # Services manifest
├── credentials.json   # API keys and control plane URL (sensitive)
└── user-data          # Cloud-init user-data (plain text)
```

### JSON Schemas

#### `instance.json`

```json
{
  "vm_id":       "string  — unique VM identifier (e.g. vm-abc123)",
  "tenant_id":   "string  — PlusClouds tenant identifier",
  "tenant_name": "string  — human-readable tenant name",
  "datacenter":  "string  — datacenter slug (e.g. ist-1)",
  "region":      "string  — region slug (e.g. eu-west)",
  "plan_tier":   "string  — compute plan (e.g. standard-2vcpu-4gb)",
  "tags":        "object  — optional key/value labels"
}
```

#### `network.json`

```json
{
  "ip_address": "string  — primary IPv4 address",
  "gateway":    "string  — default gateway IPv4 address",
  "dns":        "array   — list of DNS server IPs",
  "hostname":   "string  — short hostname",
  "domain":     "string  — DNS search domain (optional)"
}
```

#### `services.json`

```json
{
  "services": [
    {
      "name":    "string  — systemd unit name (e.g. nginx.service)",
      "enabled": "bool    — whether to enable on boot",
      "config":  "object  — optional service-specific key/value config"
    }
  ]
}
```

#### `credentials.json`

```json
{
  "api_key":           "string — shared secret for agent API authentication",
  "control_plane_url": "string — base URL of the PlusClouds control plane",
  "agent_token":       "string — per-VM JWT for control plane calls"
}
```

---

## Module Descriptions

### system.Module

Uses `github.com/shirou/gopsutil/v3` to collect:
- Host info (hostname, OS, kernel, architecture, uptime/boot time)
- CPU usage percentage and load averages (1/5/15 minute)
- Physical memory and swap utilisation
- Per-partition disk usage
- Per-interface network I/O counters

### services.Module

Uses `github.com/coreos/go-systemd/v22/dbus` to manage systemd units:
- List all loaded `.service` units
- Get unit status (active state, sub-state, PID)
- Start, stop, restart, reload units
- Enable/disable units for boot

---

## Event Bus

The event bus (`internal/events`) is an in-process pub/sub system that decouples
agent modules. Events are dispatched synchronously in the goroutine that publishes
them.

| Event Type | Published by | Payload |
|------------|-------------|---------|
| `ServiceStarted` | services.Module | `ServiceInfo` |
| `ServiceStopped` | services.Module | `ServiceInfo` |
| `ServiceFailed` | services.Module | `ServiceInfo` |
| `MetricThresholdExceeded` | system.Module | `SystemMetrics` |
| `AgentRegistered` | registry.Registry | registration payload |
| `ConfigDriftDetected` | future autoheal module | drift detail |

---

## Auth Flow

```
Client Request
    │
    ▼
Chi Router
    │
    ▼
middleware.Auth
    ├─── /healthz, /metrics → skip auth → handler
    │
    ├─── Extract key from "Authorization: Bearer <key>" header
    │    or "X-API-Key: <key>" header
    │
    ├─── Key == "" → 401 MISSING_CREDENTIALS
    ├─── cfg.Auth.APIKey == "" → 401 AGENT_NOT_CONFIGURED
    ├─── key != cfg.Auth.APIKey → 401 INVALID_CREDENTIALS
    │
    └─── key matches → handler
```

The API key originates from `credentials.json` on the ISO and is loaded into
`cfg.Auth.APIKey` at startup. It can also be set via the
`PLUSCLOUDS_AGENT_AUTH_API_KEY` environment variable.

---

## Self-Registration

On startup, the agent posts a registration payload to:

```
POST {control_plane_url}/api/v1/agents/register
Authorization: Bearer {agent_token}
Content-Type: application/json
```

**Payload:**
```json
{
  "vm_id":         "vm-abc123",
  "tenant_id":     "tenant-xyz",
  "hostname":      "web-01.tenant.plusclouds.net",
  "ip_address":    "192.168.1.10",
  "agent_version": "0.1.0",
  "capabilities":  ["system.info", "system.metrics", "services.manage", "metadata.read", "grpc.v1"],
  "labels":        { "env": "production" }
}
```

If the control plane URL is empty (local-only mode), registration is skipped
and a warning is logged.

---

## Heartbeat

After registration, the agent ticks every `registry.heartbeat_interval` (default 30s):

```
POST {control_plane_url}/api/v1/agents/heartbeat
Authorization: Bearer {agent_token}
```

**Payload:**
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

---

## Telemetry / Prometheus Metrics

The agent exposes metrics at `GET /metrics` (port 8080) for Prometheus scraping.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `plusclouds_agent_cpu_usage_percent` | Gauge | — | Current CPU usage % |
| `plusclouds_agent_memory_usage_percent` | Gauge | — | Current RAM usage % |
| `plusclouds_agent_disk_usage_percent` | Gauge | `mountpoint` | Disk usage % per mount |
| `plusclouds_agent_service_state` | Gauge | `name`, `state` | 1 if service is in this state |
| `plusclouds_agent_http_requests_total` | Counter | `method`, `path`, `status` | HTTP request count |
| `plusclouds_agent_http_request_duration_seconds` | Histogram | `method`, `path` | HTTP latency |
| `plusclouds_agent_heartbeats_total` | Counter | — | Total heartbeats sent |
