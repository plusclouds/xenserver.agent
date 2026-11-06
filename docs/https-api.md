# HTTPS API guide

The agent can serve its operations over HTTPS, with or without NATS. This guide covers enabling it, calling it and reading the results. Every operation and parameter is listed in [operations.md](operations.md).

## Enable it

In `agent.yaml` (a ready-made sample is `configs/agent.https.yaml`):

```yaml
nats:
  enabled: false            # HTTPS-only: no NATS connection and nothing is pushed
  agent_uuid: "<agent-uuid>"

http:
  enabled: true
  listen: "<management-ip>:8443"
  self_signed: true         # generate a certificate on first start
  api_key: "<at least 32 random characters>"
  hosts: ["agent.example.com"]   # extra names/IPs for the certificate

agent:
  allowed_operations:       # only what you need
    - xenserver.vm.list
    - xenserver.vm.start
```

Generate a token with `openssl rand -base64 48`. The agent refuses to start if the token is shorter than 32 characters or still `CHANGE_ME…`.

Leave `nats.enabled` at `true` to run both transports side by side.

## Certificate

- **Self-signed** (`http.self_signed: true`): an ECDSA P-256 certificate is created at `http.cert_file` / `http.key_file` on first start (default `/var/lib/plusclouds/tls/`), reused on later starts and renewed 30 days before expiry. A certificate that already exists is never overwritten. Add the names or IPs clients use to `http.hosts`.
- **Your own certificate**: set `self_signed: false` and point `cert_file` / `key_file` at it.
- **Pinning:** the SHA-256 fingerprint is logged at startup (`HTTPS server starting`). Pin it on the client rather than disabling verification:

  ```bash
  curl --cacert /path/to/agent.crt https://agent.example.com:8443/healthz
  # or compare the fingerprint of the certificate the server presents
  ```

  `curl -k` skips verification and is fine for a quick test only.

## Endpoints

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /healthz` | none | Liveness: `{"status":"ok","agent_uuid":"…"}` |
| `POST /v1/command` | `Authorization: Bearer <api_key>` | Run one operation |

Requests are limited to 1 MiB. A wrong or missing token returns `401`. Other methods on `/v1/command` return `405`.

## Calling an operation

```bash
curl -s https://agent.example.com:8443/v1/command \
  --cacert agent.crt \
  -H "Authorization: Bearer $AGENT_API_KEY" \
  -d '{
    "v": 1,
    "id": "req-1",
    "type": "command",
    "payload": {
      "operation": "xenserver.vm.snapshot_create",
      "params": { "uuid": "<vm-uuid>", "label": "before-upgrade" }
    }
  }'
```

`id` is yours to choose and is echoed back as `payload.command_id`. `params` holds the operation's parameters; `docs/operations.md` lists them.

## Reading the result

The reply is an envelope whose `payload` looks like:

```json
{
  "command_id": "req-1",
  "status": "completed",
  "message": "",
  "output": { "operation": "vm-snapshot", "target": "<vm-uuid>", "success": true,
              "message": "Snapshot created.", "output": "<snapshot-uuid>" }
}
```

| `payload.status` | HTTP | Meaning |
|---|---|---|
| `completed` | 200 | The operation ran. **Also check `output.success`**: xe-based operations report their own failure there |
| `failed` | 200 | The agent could not run it (bad input, missing precondition, or the command failed). `message` says why |
| `rejected` | 200 | Refused: not in `allowed_operations`, bad parameters, or a destructive call without `confirm` |
| `accepted` | 202 | A long operation started in the background; `output` is the job (see below) |

HTTP 4xx/5xx are reserved for transport problems (bad token, bad JSON, wrong method). Application outcomes are always in `payload.status`.

### `output` for XenServer operations

| Field | Meaning |
|---|---|
| `operation`, `target` | What ran and on which object |
| `success` | `true`/`false` |
| `message` | Plain-English summary or error |
| `output` | Raw string result, such as a new uuid |
| `data` | Structured result, such as `{"uuid": "<new-object>"}` |
| `detail` | On failure: `command`, `exit_code`, `error_code`, `stderr`, `stdout` |
| `diagnostics` | On failure: extra explanations the agent gathered (for example why a VM could not start) |

List operations return their array or object directly in `output`.

## Errors

Failures are written for people. Examples:

- Starting a running VM: `Cannot start "web-1": the VM is running, but it must be halted.`
- Plugging a card twice: `Cannot plug network card "web-1": the network card is already plugged in.`
- Resizing a plugged disk: `Cannot resize disk …: it is plugged into a running VM and this storage does not support online resize … Unplug it first … or stop the VM, resize, then plug it again.`
- Destroying an attached disk: refused unless `allow_attached` is set.

Branch on `output.detail.error_code` (the xapi error code) when you need to automate, and show `message` to people.

## Destructive operations

Operations such as `vm.destroy`, `vdi.destroy`, `network.destroy`, `vlan.destroy`, `sr.remove` and `pif.unplug` need `"confirm": true`. Without it the call is rejected and nothing happens.

## Background jobs

Add `"async": true` to a long operation (see the list in the README). The reply is HTTP 202:

```json
{ "payload": { "status": "accepted",
  "output": { "job_id": "9f1c…", "operation": "xenserver.vm.export", "status": "running",
              "progress": { "bytes_done": 0 } } } }
```

Poll it:

```bash
… -d '{"v":1,"id":"p","type":"command","payload":{"operation":"agent.job.status","params":{"job_id":"9f1c…"}}}'
```

```json
{ "job_id": "9f1c…", "status": "running", "elapsed_s": 41.2,
  "progress": { "bytes_done": 1288490188, "bytes_total": 8589934592, "percent": 15 } }
```

`status` is `running`, `completed`, `failed` or `cancelled`. When it finishes, `result` holds the operation's normal output; on failure `error` explains. `agent.job.list` shows recent jobs and `agent.job.cancel` stops one and removes a half-written export. Jobs live in memory and are lost if the agent restarts. Only `agent.max_jobs` (default 2) run at once. See [backups.md](backups.md).

## Dry run

With `agent.dry_run: true` the same calls are authenticated and validated but never executed; the reply has `"dry_run": true, "executed": false`. Useful to test an integration against a live host safely.

## Timing

Typical times on a XenServer 8.2 host over a LAN: reads 0.1–0.6 s; start of a VM a few seconds; disk create about 0.5 s; hot-plug 1–2 s; snapshot a few seconds; exports are bounded by the target's write speed (use `async`). Synchronous calls time out server-side after 30 s for ordinary operations; long ones have their own limits (10 min medium, 2 h long).

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Connection refused / timeout | Agent not running, `http.enabled` false, wrong `listen`, firewall |
| Agent exits at start naming `api_key` | Token empty, placeholder or under 32 characters |
| `401` | Wrong or missing `Authorization: Bearer` token |
| `rejected … not permitted on this agent` | Operation missing from `allowed_operations` |
| Certificate error | Name not in `http.hosts`, or the certificate was regenerated; re-pin |
| `status: accepted` never finishes | Check `agent.job.status`; jobs do not survive an agent restart |

Run the agent in the foreground with `log.level: debug` to see each command, parameters (secrets masked) and result.
