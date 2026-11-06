# PlusClouds Agent Integration Guide

> **Partly outdated.** The ISO config drive provisioning described here is still current, but endpoints and REST calls to `/api/v1/...` belong to the earlier REST design and no longer exist. For controlling the agent today, see the [README](../README.md), [https-api.md](https-api.md) and [operations.md](operations.md).

This guide is for the PlusClouds orchestration/provisioning team. It covers
everything needed to provision VMs with the agent, integrate with the control
plane, and manage VMs remotely via the agent API.

---

## Provisioning the ISO Config Drive

The agent reads configuration from a vFAT ISO image that is attached as a
secondary disk to the VM. The provisioner must create and attach this ISO
before the VM boots.

### Creating the ISO

```bash
# Create the source directory
mkdir -p /tmp/plusclouds-config

# Write metadata files (see schemas below)
cat > /tmp/plusclouds-config/instance.json << 'EOF'
{
  "vm_id": "vm-abc123",
  "tenant_id": "tenant-xyz",
  "tenant_name": "Acme Corp",
  "datacenter": "ist-1",
  "region": "eu-west",
  "plan_tier": "standard-2vcpu-4gb",
  "tags": {
    "env": "production",
    "role": "web"
  }
}
EOF

cat > /tmp/plusclouds-config/network.json << 'EOF'
{
  "ip_address": "192.168.1.10",
  "gateway": "192.168.1.1",
  "dns": ["8.8.8.8", "8.8.4.4"],
  "hostname": "web-01",
  "domain": "tenant.plusclouds.net"
}
EOF

cat > /tmp/plusclouds-config/services.json << 'EOF'
{
  "services": [
    { "name": "nginx.service", "enabled": true, "config": {} },
    { "name": "plusclouds-agent.service", "enabled": true, "config": {} }
  ]
}
EOF

cat > /tmp/plusclouds-config/credentials.json << 'EOF'
{
  "api_key": "secret-api-key-for-agent-api",
  "control_plane_url": "https://api.plusclouds.com",
  "agent_token": "eyJhbGci..."
}
EOF

cat > /tmp/plusclouds-config/user-data << 'EOF'
#cloud-config
package_upgrade: true
packages:
  - nginx
EOF

# Build the ISO image
genisoimage -o /var/lib/plusclouds/isos/vm-abc123-config.iso \
  -V plusclouds-config \
  -r -J \
  /tmp/plusclouds-config/
```

### JSON Field Reference

#### `instance.json`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `vm_id` | string | yes | Unique VM identifier (used as agent ID) |
| `tenant_id` | string | yes | Tenant/customer identifier |
| `tenant_name` | string | no | Human-readable tenant name |
| `datacenter` | string | yes | Datacenter slug (e.g. `ist-1`) |
| `region` | string | yes | Region slug (e.g. `eu-west`) |
| `plan_tier` | string | no | Compute plan identifier |
| `tags` | object | no | Key/value labels for the VM |

#### `network.json`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ip_address` | string | yes | Primary IPv4 address |
| `gateway` | string | yes | Default gateway |
| `dns` | array | no | DNS server IPs |
| `hostname` | string | yes | Short hostname |
| `domain` | string | no | DNS search domain |

#### `services.json`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `services[].name` | string | yes | Systemd unit name |
| `services[].enabled` | bool | yes | Enable on boot |
| `services[].config` | object | no | Service-specific config |

#### `credentials.json`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `api_key` | string | yes | Shared secret for agent API auth |
| `control_plane_url` | string | yes | Control plane base URL |
| `agent_token` | string | yes | Per-VM JWT for control plane calls |

---

## Agent Registration Endpoint

Registration is a **mandatory startup gate**. The agent reads the `agent_token`
from the ISO `credentials.json`, presents it to this endpoint, and will not
start its HTTP or gRPC servers until it receives a valid `session_token` in
response. If the orchestrator rejects the token (4xx) the agent exits
immediately. Transient errors (5xx, network) are retried up to 5 times with
exponential backoff (5 s → 10 s → 20 s → 40 s → 60 s) before the agent exits.

```
POST /agents/register
Authorization: Bearer {agent_token}   ← one-time provisioning token from ISO
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
  "capabilities":  [
    "system.info",
    "system.metrics",
    "services.manage",
    "metadata.read",
    "grpc.v1"
  ],
  "labels": { "env": "production", "role": "web" }
}
```

**Required response:** HTTP 200 or 201 with the following JSON body.

```json
{
  "agent_id":      "vm-abc123",
  "session_token": "eyJhbGci...",
  "expires_at":    1712432078,
  "message":       "Agent registered successfully"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `agent_id` | string | Canonical VM identifier confirmed by the orchestrator |
| `session_token` | string | **Required.** Short-lived token the agent uses for subsequent calls to the orchestrator (heartbeats). Distinct from the ISO `agent_token` and from the agent's own HTTP API key |
| `expires_at` | int64 | Unix timestamp when `session_token` expires. Future versions will re-register automatically before expiry |
| `message` | string | Human-readable confirmation (logged by the agent) |

> **Important:** A missing or empty `session_token` in the response is treated
> as a registration failure and triggers a retry.

---

## Heartbeat Endpoint

The agent calls this endpoint every `registry.heartbeat_interval` seconds
(default: 30 s). It authenticates with the `session_token` received during
registration — **not** with the original ISO `agent_token`.

```
POST /agents/heartbeat
Authorization: Bearer {session_token}   ← issued at registration
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

**Expected response:** HTTP 200. The agent logs a warning on non-2xx but continues.
A 401 response from the heartbeat endpoint indicates the session has expired;
future versions will trigger automatic re-registration.

---

## Controlling a VM Remotely

All API calls require the `api_key` from `credentials.json`.

### Check agent health

```bash
curl -H "X-API-Key: secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/healthz
```

### Get system info

```bash
curl -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/system/info
```

### Get all metrics

```bash
curl -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/system/metrics
```

### List services

```bash
curl -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/services
```

### Start a service

```bash
curl -X POST \
  -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/services/nginx/start
```

### Stop a service

```bash
curl -X POST \
  -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/services/nginx/stop
```

### Restart a service

```bash
curl -X POST \
  -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/services/nginx/restart
```

### Enable a service on boot

```bash
curl -X POST \
  -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/services/nginx/enable
```

### Get VM metadata

```bash
curl -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/metadata
```

### Get network metadata only

```bash
curl -H "Authorization: Bearer secret-api-key-for-agent-api" \
  http://192.168.1.10:8080/api/v1/metadata/network
```

---

## gRPC Endpoint

The gRPC server listens on port **8081** (configurable via `server.grpc.port`).

**Authentication:** Supply the API key in gRPC metadata:
```
x-api-key: <api-key>
```

**Phase 1 status:** The gRPC server is fully operational (auth + logging interceptors)
but has no registered services yet. Service definitions will be added in Phase 2
once the `.proto` files are finalised.

**Planned services (Phase 2):**
- `AgentService` — real-time metric streaming via server-side streaming RPC
- `ServiceManagerService` — remote service lifecycle management via gRPC
- `EventService` — subscribe to agent events via bidirectional streaming

---

## Prometheus Metrics

Scrape the agent's metrics endpoint:

```
GET http://<vm-ip>:8080/metrics
Authorization: Bearer <api-key>
```

Authentication is **required** on `/metrics`. The following metrics are available:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `plusclouds_agent_cpu_usage_percent` | Gauge | — | Overall CPU usage % |
| `plusclouds_agent_memory_usage_percent` | Gauge | — | RAM usage % |
| `plusclouds_agent_disk_usage_percent` | Gauge | `mountpoint` | Disk usage % |
| `plusclouds_agent_service_state` | Gauge | `name`, `state` | Service state (1=current) |
| `plusclouds_agent_http_requests_total` | Counter | `method`, `path`, `status` | Request count |
| `plusclouds_agent_http_request_duration_seconds` | Histogram | `method`, `path` | Request latency |
| `plusclouds_agent_heartbeats_total` | Counter | — | Heartbeats sent |

**Prometheus scrape config example:**
```yaml
scrape_configs:
  - job_name: 'plusclouds-agents'
    authorization:
      credentials: 'secret-api-key-for-agent-api'
    static_configs:
      - targets:
          - '192.168.1.10:8080'
          - '192.168.1.11:8080'
    relabel_configs:
      - source_labels: [__address__]
        target_label: vm_ip
```

> **Kubernetes liveness/readiness probes** must also supply the key:
> ```yaml
> livenessProbe:
>   httpGet:
>     path: /healthz
>     port: 8080
>     httpHeaders:
>       - name: X-API-Key
>         value: secret-api-key-for-agent-api
> ```

---

## Error Handling and Retry Behavior

### Agent startup

- ISO not mounted: agent logs a warning and starts in local-only mode (no control plane calls).
- systemd D-Bus unavailable: agent fails to start with a fatal error.
- Config file missing: agent falls back to defaults and environment variables.

### Registration failures

- **4xx from orchestrator** (bad token, unknown VM, tenant mismatch): agent exits immediately — retrying will not help. Check the ISO `credentials.json` and orchestrator logs.
- **5xx / network errors**: retried up to 5 times with exponential backoff (5 s → 10 s → 20 s → 40 s → 60 s). If all retries fail, the agent exits.
- **No control plane URL configured**: agent starts in local-only mode (no registration, no heartbeat). Intended for development only.
- **Missing `session_token` in response**: treated as a transient failure and retried.

### Heartbeat failures

- Failed heartbeats are logged as warnings; the agent continues and retries on the next tick.
- A 401 heartbeat response means the session has expired. Re-registration on session expiry will be added in a future version.

### Service action failures

- If a systemd job does not return `done`, the agent returns an `ActionResult` with
  `success: false` and a descriptive message. The HTTP status is 422.
- D-Bus connection errors return HTTP 500.

---

## Security Model

### Phase 1

- **API key authentication:** A single shared secret per VM, provisioned via the
  ISO config drive. Rotated by re-creating the ISO and rebooting the VM (or sending
  SIGHUP in a future version).
- **No TLS on HTTP:** The agent API is intended to be accessed only from the control
  plane or via a trusted network. TLS termination should be handled by a sidecar
  (e.g. nginx with self-signed cert) or a VPN.
- **gRPC auth:** Same API key supplied in `x-api-key` metadata.

### Phase 2 (planned)

- **mTLS:** The gRPC server will require client certificates signed by the
  PlusClouds CA. The CA certificate will be provisioned via the ISO config drive.
- **API key rotation:** The control plane will push a new API key via the gRPC
  `AgentService.RotateKey` RPC without requiring a VM restart.
- **JWT validation:** The `agent_token` in `credentials.json` will be a short-lived
  JWT. The agent will refresh it via the control plane before expiry.

---

## Webhook / Event Format

Currently the agent does not push webhooks to the control plane on state changes.
Phase 2 will introduce a bidirectional gRPC stream for real-time event delivery.

The control plane can poll service state via the heartbeat endpoint and
`GET /api/v1/services` in the meantime.
