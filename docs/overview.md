# Agent System Overview

This document describes the architecture of the PlusClouds agent communication system built on NATS.

## What is an agent?

An agent is a long-running process deployed alongside infrastructure components (storage nodes, hypervisors, switches, firewalls, backup appliances, etc.) that:

- **Reports** health, telemetry, alerts, and sensor data to the platform in real time
- **Receives** commands from the platform (create NFS export, restart VM, update config, etc.)
- **Authenticates** with the platform using a unique `events_token` stored in its member table

## Agent types (current)

| Agent type | Member table | NATS subject prefix |
|---|---|---|
| `vm` | `iaas_virtual_machines` | `agent.vm.{uuid}` |
| `storage` | `iaas_storage_members` | `agent.storage.{uuid}` |
| `compute` | `iaas_compute_members` | `agent.compute.{uuid}` |
| `network` | `iaas_network_members` | `agent.network.{uuid}` |

New agent types can be added without protocol changes — `agent_type` is a free-text field throughout.

## Subject map

```
agent.{type}.{uuid}.cmd     ← platform sends commands to a specific agent
agent.{type}.{uuid}.evt     ← agent sends telemetry/alerts/results to platform
agent.{type}.broadcast      ← platform broadcasts to all agents of one type
agent.broadcast             ← platform broadcasts to every agent

client.{account_uuid}.evt   ← platform pushes model events to browser clients
```

## Authentication

Agents authenticate using the `events_token` stored in their member table row. On connect, the NATS server calls the PHP auth callout service (`NatsAuthCalloutService`), which:

1. Looks up the token across `iaas_*_members` tables
2. Issues a signed JWT granting the agent permission to subscribe to its own `.cmd` subject and publish to its own `.evt` subject
3. Denies the connection if the token is not found or the member is deleted

Revoking an agent: set `events_token = null` or soft-delete the member row. The agent is rejected on its next reconnect attempt.

## Message flow

### Agent → Platform (telemetry / events)

```
Agent publishes to agent.storage.{uuid}.evt
  └── NatsListenCommand receives it
        └── dispatches HandleAgentEventJob to queue
              ├── heartbeat  → update is_alive on StorageMembers
              ├── telemetry  → update used_cpu/ram/disk on StorageMembers
              ├── disk_health→ update StorageMemberDevices health fields
              ├── alert      → Events::fire() → platform alert flows
              ├── ipmi       → update management_data on StorageMembers
              └── result     → close out agent_commands record
```

### Platform → Agent (commands)

```
StorageAgentCommandService::createNfsExport($uuid, $params)
  └── AgentCommandService::dispatch(...)
        ├── INSERT INTO agent_commands (status=pending)
        ├── NatsService::publish("agent.storage.{uuid}.cmd", envelope)
        └── UPDATE agent_commands SET status=sent, sent_at=now()

Agent receives command → executes → publishes result to agent.storage.{uuid}.evt
  └── HandleAgentEventJob receives result
        └── UPDATE agent_commands SET status=completed, result=..., completed_at=now()
```

## Durable delivery (JetStream)

All `agent.>` subjects are captured by the `AGENT_COMMANDS` JetStream stream (file storage, 24h TTL, 1000 msgs/subject). Agents create durable consumers on connect so they receive commands queued while they were offline.

## HTTPS transport

NATS is not required. With `http.enabled: true` the agent also (or, with `nats.enabled: false`, only) serves `POST /v1/command` over HTTPS. The request body is the same command envelope and the response body is the result envelope, so a caller can drive an agent directly with no message bus. Nothing is pushed in HTTPS-only mode; the caller polls (`agent.job.status` for background jobs). See [https-api.md](https-api.md).

## Key files in this repository

| Path | Purpose |
|---|---|
| `cmd/agent/main.go` | Start-up: config, transports, dispatcher, jobs |
| `internal/dispatcher/` | Allowlist, parameter checks, operation routing, dry-run, async jobs |
| `internal/httpserver/` | HTTPS endpoint, bearer auth, self-signed certificate |
| `internal/nats/` | NATS client and subscriptions |
| `internal/publisher/` | Heartbeat, telemetry, capabilities, operation catalog |
| `internal/jobs/` | Background job manager |
| `internal/modules/xenserver/` | `xe`/xapi operations, IPMI, RRD |
| `internal/modules/openvswitch/`, `nfsmount/`, `services/`, `system/` | Other operation groups |
| `internal/executor/` | Safe subprocess execution with timeouts and secret masking |

## Related documentation

- [protocol.md](protocol.md): envelope format and all payload schemas
- [https-api.md](https-api.md): HTTPS endpoint guide
- [operations.md](operations.md): every operation and parameter
- [backups.md](backups.md): snapshots, exports and restore
- [security.md](security.md): controls and hardening checklist
