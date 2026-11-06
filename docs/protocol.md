# Protocol

Every message between a caller and the agent, in either direction and on either transport (NATS or HTTPS), is one JSON **envelope**. The `payload` carries type-specific data.

## Envelope

```json
{
  "v":          1,
  "id":         "550e8400-e29b-41d4-a716-446655440000",
  "type":       "command",
  "agent_type": "compute",
  "agent_uuid": "550e8400-e29b-41d4-a716-446655440001",
  "timestamp":  1748000000,
  "reply_to":   "_INBOX.abc123",
  "payload":    {}
}
```

| Field | Type | Description |
|---|---|---|
| `v` | integer | Protocol version; see [Versioning](#versioning). |
| `id` | string | Unique message id. For a command it becomes `command_id` in the result. Any string is accepted from callers. |
| `type` | string | `command` (to the agent) or `result`, `heartbeat`, `telemetry`, `capabilities`, `ipmi`, `xapi_event` (from the agent). |
| `agent_type` | string | Agent category; `compute` for this agent. |
| `agent_uuid` | string | The agent's uuid. |
| `timestamp` | integer | Unix seconds when the message was created. |
| `reply_to` | string, optional | NATS only: publish the result to this inbox instead of the event subject. |
| `payload` | object | Type-specific data. |

## Command (caller → agent)

NATS: published to `agent.compute.{uuid}.cmd`. HTTPS: `POST /v1/command` (see [https-api.md](https-api.md)).

```json
{
  "v": 1,
  "id": "req-1",
  "type": "command",
  "payload": {
    "operation": "xenserver.vm.snapshot_create",
    "params": { "uuid": "<vm-uuid>", "label": "before-upgrade" },
    "timeout_s": 300
  }
}
```

`operation` and `params` are listed in [operations.md](operations.md). `timeout_s` is optional and advisory. Any operation can carry `"async": true` in `params` if it supports background execution.

## Result (agent → caller)

NATS: on `agent.compute.{uuid}.evt` (or `reply_to`). HTTPS: the response body.

```json
{
  "command_id": "req-1",
  "status":     "completed",
  "message":    "",
  "output":     {}
}
```

| `status` | Meaning |
|---|---|
| `completed` | The operation ran. For XenServer operations also check `output.success`, which carries the operation's own outcome. |
| `failed` | The agent could not run it or it errored; `message` explains. |
| `rejected` | Refused: unsupported protocol version, operation not in `allowed_operations`, invalid parameters, or a destructive call without `confirm`. |
| `accepted` | A long operation started in the background; `output` is the job (`job_id`, `status`, `progress`). Poll `agent.job.status`. HTTPS returns HTTP 202. |

The result envelope's `v` echoes the request's version (a rejected unsupported version is answered as version 1).

## Messages the agent pushes (NATS only)

All are published to `agent.compute.{uuid}.evt`.

### `heartbeat`

Every `agent.heartbeat_interval` (default 30 s).

```json
{ "version": "v1.2.0", "uptime_s": 86400, "tasks_queued": 0 }
```

### `telemetry`

Every `agent.telemetry_interval` (default 30 s; changeable with `telemetry.set_interval`). A copy is also published to `vm.{uuid}.telemetry`. The payload has `cpu`, `memory`, `disks` and `network` as shown in the [README](../README.md#telemetry-payload). With `xenserver.rrd_enabled`, a `vms` array of per-VM utilisation (the same data as `xenserver.metrics.rrd`) is added.

### `capabilities`

Sent on connect and on request (`agent.allowed_operations`): the schema of every enabled operation.

```json
{ "operations": [ { "operation": "xenserver.vm.start", "description": "…",
                    "params": [ { "name": "uuid", "type": "string", "required": false, "description": "…" } ] } ],
  "exec_commands": ["/usr/bin/df"] }
```

### `ipmi`

With `xenserver.ipmi_enabled`, every `xenserver.ipmi_interval`.

```json
{ "hostname": "host1", "sensors": [ … ] }
```

`sensors` has the same shape as the output of `xenserver.ipmi.sensors`.

### `xapi_event`

With `xenserver.events_enabled`: one message per XenServer object change.

```json
{ "hostname": "host1", "class": "vm", "uuid": "<object-uuid>", "operation": "mod", "snapshot": { … } }
```

## Versioning

`v` is the protocol version. This agent speaks version **1**.

- A request with `v: 1`, or with `v` omitted, is accepted. An omitted version is treated as 1 so simple callers keep working; send it explicitly.
- Any other value is rejected with `status: "rejected"` and a message naming the supported versions (`unsupported protocol version 2; this agent supports [1]`). Over HTTPS the response is HTTP 400; over NATS the rejection is published as a normal result.
- Discovery: `GET /healthz` returns `agent_version` and `protocol_versions`; every HTTPS response carries `X-Agent-Version` and `X-Protocol-Version` headers; the heartbeat carries the agent `version`.
- New optional fields may be added to a version without changing `v`. A breaking change (a removed or renamed required field) gets a new version, and the agent will list both until the old one is retired.
