# Messaging overview

How callers talk to the agent. The agent is driven by command envelopes and answers with result envelopes; it can also push telemetry and events.

## Transports

| Transport | Commands in | Results and events out | Push |
|---|---|---|---|
| **NATS** | subject `agent.compute.{uuid}.cmd` | result on `agent.compute.{uuid}.evt`, or on the command's `reply_to` inbox | heartbeat, telemetry, capabilities, IPMI, xapi events |
| **HTTPS** | `POST /v1/command` | the HTTP response body | none (caller polls) |

Both use the same envelope ([protocol.md](protocol.md)), the same allowlist and the same result format. Enable either or both; see [provisioning.md](provisioning.md) and [https-api.md](https-api.md).

## NATS subjects

```
agent.compute.{uuid}.cmd     ← commands to one agent
agent.compute.{uuid}.evt     → results, heartbeat, telemetry, capabilities, ipmi, xapi_event
vm.{uuid}.telemetry          → client-facing telemetry copy (JetStream, short retention)
```

`{uuid}` is the agent's identity (see provisioning). The agent's NATS credentials are scoped to its own subjects.

### Sync commands

If a command carries `reply_to`, the agent publishes the result directly to that inbox subject instead of the `.evt` subject, so a caller can use request/reply. Without `reply_to` the result goes to `.evt` and the caller matches it by `command_id`.

### Durable delivery

When the platform captures `agent.>` in a JetStream stream, commands sent while an agent is offline are delivered on reconnect. This is configured on the NATS server, not in the agent.

## Message flow

```
caller ──command──▶ dispatcher ──▶ allowlist ─▶ parameter checks ─▶ module (xe, systemd, …)
                                       │ not allowed / invalid
                                       ▼
caller ◀──result── completed | failed | rejected | accepted (background job)
```

1. The transport hands the envelope to the dispatcher.
2. The dispatcher checks the protocol version, the operation allowlist and, for destructive operations, `confirm`.
3. The module runs the operation (with timeouts, secret masking and readable errors) or starts it as a background job (`async`).
4. The result is returned on the same transport.

## Versioning

Every envelope carries `v`. See [protocol.md](protocol.md#versioning).

## Related documentation

- [protocol.md](protocol.md): envelope and payload schemas
- [https-api.md](https-api.md): HTTPS guide
- [operations.md](operations.md): every operation and parameter
- [provisioning.md](provisioning.md): identity, installation, credentials
- [backups.md](backups.md): snapshots, exports and restore
- [security.md](security.md): controls and hardening
