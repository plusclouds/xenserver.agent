# PlusClouds Agent REST API Reference

> **Legacy document.** This describes the REST API and `plsctl` client of an early (v0.1) version of the agent. The current agent has no `/api/v1` REST API: it is driven by command envelopes over NATS or `POST /v1/command` over HTTPS. See the [README](../README.md), [https-api.md](https-api.md) and [operations.md](operations.md). Kept for reference only.

## Overview

The PlusClouds agent exposes a REST API on port **8080** (configurable). All API
responses use a consistent JSON envelope regardless of success or failure.

---

## Authentication

**Every request** to the agent — including `/healthz` and `/metrics` — must
include a valid API key. The agent may be deployed in a DMZ or on a network
reachable from outside the private cluster, so no endpoint is exempt.

Two header formats are accepted:

```
Authorization: Bearer <api-key>
X-API-Key: <api-key>
```

The API key is provisioned via the ISO config drive (`credentials.json`) and is
automatically loaded at agent startup. It can also be set via the environment
variable `PLUSCLOUDS_AGENT_AUTH_API_KEY`.

> **Prometheus scraping**: configure a `bearer_token` or `authorization` block
> in your `prometheus.yml` scrape config for the agent target.
>
> **Kubernetes/orchestrator liveness probes**: pass the API key via the probe's
> `httpHeaders` field (`X-API-Key: <key>`).

---

## Base URL

```
http://<vm-ip>:8080
```

---

## Response Format

Every response is wrapped in a standard envelope:

```json
{
  "success": true,
  "data": { ... },
  "error": null,
  "meta": {
    "version": "0.1.0",
    "agent_id": "vm-abc123",
    "timestamp": 1712345678
  }
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `success` | boolean | `true` for 2xx responses, `false` for errors |
| `data` | object/array | Response payload (omitted on error) |
| `error` | object | Error detail (omitted on success) |
| `error.code` | string | Machine-readable error code |
| `error.message` | string | Human-readable error description |
| `meta.version` | string | Agent version |
| `meta.agent_id` | string | VM ID from ISO metadata |
| `meta.timestamp` | integer | Unix timestamp of the response |

### HTTP Status Codes

| Code | Meaning |
|------|---------|
| 200 | Success |
| 401 | Missing or invalid API key |
| 404 | Resource not found |
| 422 | Action failed (service operation returned non-done job result) |
| 500 | Internal agent error |
| 503 | Service unavailable (e.g. ISO not mounted) |

---

## Endpoints

### Health Check

#### `GET /healthz`

Returns the agent liveness status. Does not require authentication.

**Response:**
```json
{
  "success": true,
  "data": {
    "status": "ok",
    "version": "0.1.0"
  },
  "meta": { "version": "0.1.0", "agent_id": "vm-abc123", "timestamp": 1712345678 }
}
```

---

### System

#### `GET /api/v1/system/info`

Returns static VM identity and OS information.

**Response:**
```json
{
  "success": true,
  "data": {
    "hostname": "web-01.tenant.plusclouds.net",
    "os": "Ubuntu 24.04 LTS",
    "kernel_version": "6.8.0-35-generic",
    "architecture": "x86_64",
    "uptime": 86432,
    "boot_time": 1712259246,
    "vm_id": "vm-abc123",
    "tenant_id": "tenant-xyz"
  },
  "meta": { "version": "0.1.0", "agent_id": "vm-abc123", "timestamp": 1712345678 }
}
```

---

#### `GET /api/v1/system/metrics`

Returns a full snapshot of all resource metrics.

**Response:**
```json
{
  "success": true,
  "data": {
    "cpu": {
      "usage_percent": 12.5,
      "core_count": 4,
      "model_name": "Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz",
      "load_avg_1": 0.42,
      "load_avg_5": 0.38,
      "load_avg_15": 0.31
    },
    "memory": {
      "total_bytes": 8589934592,
      "used_bytes": 2147483648,
      "free_bytes": 6442450944,
      "usage_percent": 25.0,
      "swap_total": 2147483648,
      "swap_used": 0
    },
    "disk": {
      "partitions": [
        {
          "device": "/dev/sda1",
          "mountpoint": "/",
          "fstype": "ext4",
          "total_bytes": 107374182400,
          "used_bytes": 21474836480,
          "free_bytes": 85899345920,
          "usage_percent": 20.0
        }
      ]
    },
    "network": {
      "interfaces": [
        {
          "name": "eth0",
          "ip_addresses": ["192.168.1.10/24"],
          "bytes_sent": 1048576,
          "bytes_recv": 2097152,
          "packets_sent": 1024,
          "packets_recv": 2048,
          "is_up": true
        }
      ]
    },
    "collected_at": 1712345678
  },
  "meta": { "version": "0.1.0", "agent_id": "vm-abc123", "timestamp": 1712345678 }
}
```

---

#### `GET /api/v1/system/cpu`

Returns CPU statistics only.

**Response data:** Same as the `cpu` object in `/system/metrics`.

---

#### `GET /api/v1/system/memory`

Returns memory statistics only.

**Response data:** Same as the `memory` object in `/system/metrics`.

---

#### `GET /api/v1/system/disk`

Returns disk usage for all mounted partitions.

**Response data:** Same as the `disk` object in `/system/metrics`.

---

#### `GET /api/v1/system/network`

Returns network interface I/O counters.

**Response data:** Same as the `network` object in `/system/metrics`.

---

### Services

#### `GET /api/v1/services`

Returns a list of all loaded systemd `.service` units.

**Response:**
```json
{
  "success": true,
  "data": [
    {
      "name": "nginx.service",
      "description": "A high performance web server and reverse proxy server",
      "state": "active",
      "sub_state": "running",
      "enabled": false,
      "pid": 1234,
      "since": 1712259300
    },
    {
      "name": "ssh.service",
      "description": "OpenBSD Secure Shell server",
      "state": "active",
      "sub_state": "running",
      "enabled": false,
      "pid": 987,
      "since": 1712259280
    }
  ],
  "meta": { "version": "0.1.0", "agent_id": "vm-abc123", "timestamp": 1712345678 }
}
```

**ServiceState values:** `active`, `inactive`, `failed`, `unknown`

---

#### `GET /api/v1/services/{name}`

Returns details for a single service. The `.service` suffix is appended automatically
if omitted.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `name` | Systemd unit name (e.g. `nginx` or `nginx.service`) |

**Error response (404):**
```json
{
  "success": false,
  "error": {
    "code": "SERVICE_NOT_FOUND",
    "message": "unit not found: nginx.service"
  },
  "meta": { ... }
}
```

---

#### `POST /api/v1/services/{name}/start`

Starts the named service. Waits for the systemd job to complete before responding.

**Response:**
```json
{
  "success": true,
  "data": {
    "service": "nginx.service",
    "action": "start",
    "success": true,
    "message": "Service nginx.service started successfully."
  },
  "meta": { ... }
}
```

**Error response (422):**
```json
{
  "success": true,
  "data": {
    "service": "nginx.service",
    "action": "start",
    "success": false,
    "message": "job result: failed"
  },
  "meta": { ... }
}
```

---

#### `POST /api/v1/services/{name}/stop`

Stops the named service.

**Response data:** Same shape as `/start` with `"action": "stop"`.

---

#### `POST /api/v1/services/{name}/restart`

Restarts the named service.

**Response data:** Same shape as `/start` with `"action": "restart"`.

---

#### `POST /api/v1/services/{name}/reload`

Sends a reload signal to the named service (equivalent to `systemctl reload`).

**Response data:** Same shape as `/start` with `"action": "reload"`.

---

#### `POST /api/v1/services/{name}/enable`

Enables the named service to start on boot.

**Response data:** Same shape as `/start` with `"action": "enable"`.

---

#### `POST /api/v1/services/{name}/disable`

Disables the named service from starting on boot.

**Response data:** Same shape as `/start` with `"action": "disable"`.

---

### Metadata

#### `GET /api/v1/metadata`

Returns all ISO metadata except credentials (API keys and tokens are never exposed).

**Response:**
```json
{
  "success": true,
  "data": {
    "instance": {
      "vm_id": "vm-abc123",
      "tenant_id": "tenant-xyz",
      "tenant_name": "Acme Corp",
      "datacenter": "ist-1",
      "region": "eu-west",
      "plan_tier": "standard-2vcpu-4gb",
      "tags": { "env": "production", "role": "web" }
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
    },
    "user_data": "#cloud-config\n..."
  },
  "meta": { ... }
}
```

---

#### `GET /api/v1/metadata/instance`

Returns instance identity metadata from `instance.json` on the config drive.

**Response data:** The `instance` object from `/metadata`.

---

#### `GET /api/v1/metadata/network`

Returns network configuration from `network.json` on the config drive.

**Response data:** The `network` object from `/metadata`.

---

#### `GET /api/v1/metadata/services`

Returns the services manifest from `services.json` on the config drive.

**Response data:** The `services` object from `/metadata`.

---

## Error Codes

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `MISSING_CREDENTIALS` | 401 | No API key provided |
| `AGENT_NOT_CONFIGURED` | 401 | Agent has no API key configured |
| `INVALID_CREDENTIALS` | 401 | API key does not match |
| `SERVICE_NOT_FOUND` | 404 | Named systemd unit does not exist |
| `METADATA_UNAVAILABLE` | 503 | Config drive is not mounted |
| `INSTANCE_METADATA_NOT_FOUND` | 404 | `instance.json` not present on ISO |
| `NETWORK_METADATA_NOT_FOUND` | 404 | `network.json` not present on ISO |
| `SERVICES_METADATA_NOT_FOUND` | 404 | `services.json` not present on ISO |
| `SYSTEM_INFO_ERROR` | 500 | Failed to collect system info |
| `METRICS_ERROR` | 500 | Failed to collect resource metrics |
| `CPU_ERROR` | 500 | Failed to collect CPU metrics |
| `MEMORY_ERROR` | 500 | Failed to collect memory metrics |
| `DISK_ERROR` | 500 | Failed to collect disk metrics |
| `NETWORK_ERROR` | 500 | Failed to collect network metrics |
| `SERVICE_LIST_ERROR` | 500 | Failed to list systemd units |
| `SERVICE_START_ERROR` | 500 | D-Bus error starting unit |
| `SERVICE_STOP_ERROR` | 500 | D-Bus error stopping unit |
| `SERVICE_RESTART_ERROR` | 500 | D-Bus error restarting unit |
| `SERVICE_RELOAD_ERROR` | 500 | D-Bus error reloading unit |
| `SERVICE_ENABLE_ERROR` | 500 | D-Bus error enabling unit |
| `SERVICE_DISABLE_ERROR` | 500 | D-Bus error disabling unit |
