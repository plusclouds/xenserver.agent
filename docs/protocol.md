# Agent Protocol

All NATS messages between the platform and agents — in either direction — share a common JSON envelope. The `payload` field carries type-specific data.

## Envelope (both directions)

```json
{
  "v":          1,
  "id":         "550e8400-e29b-41d4-a716-446655440000",
  "type":       "command|telemetry|heartbeat|alert|disk_health|ipmi|result",
  "agent_type": "storage",
  "agent_uuid": "550e8400-e29b-41d4-a716-446655440001",
  "timestamp":  1748000000,
  "payload":    {}
}
```

| Field | Type | Description |
|---|---|---|
| `v` | integer | Protocol version. Always `1` for now. |
| `id` | uuid string | Unique message ID. For commands, this becomes `command_id` in the result. |
| `type` | string | Message type — determines `payload` shape (see below). |
| `agent_type` | string | Free-text agent category. No enum constraint — new types work without changes. |
| `agent_uuid` | uuid string | UUID of the specific agent member record. |
| `timestamp` | integer | Unix timestamp (seconds) when the message was created. |
| `payload` | object | Type-specific data. |

---

## Agent → Platform payload types

Published to `agent.{type}.{uuid}.evt`.

### `heartbeat`

Sent every ~30 seconds to keep `is_alive` current on the member record.

```json
{
  "version":      "1.0.0",
  "uptime_s":     86400,
  "tasks_queued": 0
}
```

### `telemetry`

Periodic performance snapshot. Sent every 60–300 seconds depending on agent config.

```json
{
  "cpu": {
    "usage_pct": 12.5,
    "cores": 8
  },
  "ram": {
    "used_bytes":  4294967296,
    "total_bytes": 34359738368
  },
  "network": {
    "interfaces": [
      {
        "name":      "eth0",
        "rx_bps":    1048576,
        "tx_bps":    524288,
        "rx_errors": 0,
        "tx_errors": 0
      }
    ]
  },
  "storage": {
    "pools": [
      {
        "name":        "tank",
        "used_bytes":  107374182400,
        "total_bytes": 1099511627776,
        "state":       "online"
      }
    ],
    "volumes": [
      {
        "name":        "vol1",
        "used_bytes":  10737418240,
        "total_bytes": 107374182400,
        "state":       "online"
      }
    ]
  },
  "uptime_s": 86400
}
```

The `storage` key is only present for storage agents. Compute agents omit it and may include a `vms` key instead.

### `disk_health`

SMART data for every physical disk. Sent on connect and whenever a disk status changes.

```json
{
  "disks": [
    {
      "id":          "sda",
      "device":      "/dev/sda",
      "model":       "WD Red 4TB",
      "serial":      "WD-12345",
      "size_bytes":  4000787030016,
      "health":      "ok",
      "temperature_c": 38,
      "smart": {
        "reallocated_sectors":   0,
        "pending_sectors":       0,
        "uncorrectable_errors":  0,
        "power_on_hours":        12000
      }
    }
  ]
}
```

`health` values: `ok`, `warning`, `failed`.

### `alert`

Fired when the agent detects a condition requiring human attention. The platform calls `Events::fire()` so existing alert handlers pick it up.

```json
{
  "severity":   "warning",
  "code":       "DISK_REALLOCATED_SECTORS",
  "message":    "Drive sda has 5 reallocated sectors",
  "object_type":"disk",
  "object_id":  "sda",
  "details":    {}
}
```

`severity` values: `info`, `warning`, `critical`, `emergency`.

`object_type` values: `disk`, `volume`, `pool`, `network`, `system`.

### `ipmi`

BMC/IPMI sensor data. Sent on connect and periodically thereafter.

```json
{
  "power_state": "on",
  "temperatures": [
    { "sensor": "CPU1 Temp", "celsius": 45, "status": "ok" }
  ],
  "fan_speeds": [
    { "name": "FAN1", "rpm": 2400, "status": "ok" }
  ],
  "power_supplies": [
    { "id": "PSU1", "input_watts": 200, "status": "ok" }
  ],
  "voltages": [
    { "sensor": "CPU VCORE", "volts": 1.0, "status": "ok" }
  ]
}
```

`status` on each sensor: `ok`, `warning`, `critical`, `absent`.

### `result`

Response to a command. The `command_id` matches the `id` from the originating command envelope. The platform uses this to close out the `agent_commands` DB record.

```json
{
  "command_id": "550e8400-e29b-41d4-a716-446655440000",
  "status":     "completed",
  "message":    "NFS export /mnt/vol1 created successfully",
  "output":     {}
}
```

`status` values: `completed`, `failed`, `rejected`, `accepted`.

`rejected` means the agent refused the command (operation not in `allowed_operations`, invalid parameters, a destructive call without `confirm`). `failed` means it tried and encountered an error. `completed` means it ran; for XenServer operations also check `output.success`, which carries the operation's own outcome. `accepted` means a long operation was started in the background: `output` holds the job (`job_id`, `status`, `progress`); poll it with `agent.job.status`. Over HTTPS `accepted` is returned with HTTP 202.

Results are returned the same way on both transports: on NATS as a `result` message on the event subject, on HTTPS as the response body of `POST /v1/command`.

---

## Platform → Agent payload (commands)

Published to `agent.{type}.{uuid}.cmd`.

The envelope `type` is always `"command"`. The `id` field becomes the `command_id` the agent echoes in its `result`.

```json
{
  "v": 1,
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "type": "command",
  "agent_type": "compute",
  "agent_uuid": "550e8400-e29b-41d4-a716-446655440001",
  "timestamp": 1748000000,
  "payload": {
    "operation": "xenserver.vm.snapshot_create",
    "params": { "uuid": "<vm-uuid>", "label": "before-upgrade" }
  }
}
```

Operations and their parameters are listed in [operations.md](operations.md). The same envelope is accepted over HTTPS at `POST /v1/command` (see [https-api.md](https-api.md)).

`timeout_s` is advisory — the agent should abandon the operation and send a `failed` result if it cannot complete within this many seconds.

---

## Versioning

The `v` field allows the protocol to evolve. The platform publishes with `v: 1`. Agents should reject messages with an unknown version and log a warning. New optional fields can be added within a version without bumping `v`. Breaking changes (removed or renamed required fields) require a new version.
