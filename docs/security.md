# Security

The agent can start, stop and delete virtual machines and run commands as root, so it should be treated like an administrative API.

## Controls

| Control | What it does |
|---|---|
| **Operation allowlist** | `agent.allowed_operations` is a hard gate. Anything not listed is `rejected`. State-changing operations ship commented out in the sample configs, so a fresh install is read-only |
| **`confirm` for destructive calls** | Deleting a VM, disk, network, VLAN or SR attachment, and taking an interface down, need `"confirm": true` |
| **Safe defaults** | Attached disks are not destroyed, the management interface cannot be unplugged, exports never overwrite files |
| **Exec allowlist** | `exec` is off by default; when enabled only binaries in `allowed_commands` run, with arguments passed directly (no shell) |
| **Argument safety** | Parameters reach `xe`, `mount` and `umount` as discrete, validated arguments; paths must be absolute and `--` precedes positional arguments |
| **HTTPS** | TLS, constant-time bearer-token comparison, 1 MiB request limit, token of at least 32 characters enforced at start-up |
| **NATS** | Outbound-only connection, per-agent scoped JWT, revocable on the platform |
| **Secret masking** | Passwords, secrets, tokens, SR device-config and `xenstore-data` values are masked in logs, error output and parameter listings |
| **Child processes** | Anything the agent launches dies with it, and hung commands are terminated after a timeout |
| **Callbacks** | Async-job callbacks are off unless `agent.callback_allowed_urls` lists https prefixes; other URLs are refused, so the agent cannot be used to reach arbitrary hosts. Callback tokens are masked in logs |
| **Config-drive ISOs** | `iso.create` writes only into an ISO library, with validated flat file names, size limits and no overwrite unless asked; file contents are never logged |
| **Dry-run** | `agent.dry_run: true` validates requests without executing them |

## Hardening checklist

1. Generate a long random token (`openssl rand -base64 48`); keep it out of version control. Environment variable `PLUSCLOUDS_AGENT_HTTP_API_KEY` overrides the file.
2. Bind `http.listen` to a management address and firewall the port to the callers that need it.
3. Pin the certificate fingerprint on clients, or supply your own certificate.
4. Enable only the operations you use. Leave `exec`, `vm.reboot` and `vm.shutdown` off unless required; those two power-cycle the agent's own host.
5. Restrict `nfsmount.mount`: it accepts any absolute mount point.
6. Rotate the token if it was ever shared, logged or committed; restart the agent after changing it.
7. Set restrictive permissions on the config (`chmod 600 /etc/plusclouds/agent.yaml`) and the TLS key.
8. Run `log.level: info` in production; `debug` records parameters (secrets are masked, but treat the logs as sensitive).
9. Keep the host and agent up to date. Check release checksums (`SHA256SUMS`) before deploying a binary.

## Known limits

- One static token authenticates all HTTPS callers: no per-user identity, roles or audit trail beyond the agent's log.
- No rate limiting or lockout on repeated bad tokens; rely on network controls and a long token.
- Mount points are not confined to a base directory.
- `exec` runs allow-listed binaries as the agent's user (root on Dom0).

## Reporting a vulnerability

Email **support@plusclouds.com** with details and, if possible, a reproduction. Please do not open a public issue for security problems.
