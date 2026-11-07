# Provisioning and deployment

How an agent gets its identity and configuration, how to install it, and how to change or revoke its credentials.

## Identity

An agent needs a UUID and a credential:

| Transport | Identity | Credential |
|---|---|---|
| NATS | agent UUID (the NATS username) | agent API key (the NATS password) |
| HTTPS | agent UUID (reported by `/healthz`, stamped on results) | `http.api_key` bearer token |

The agent resolves the NATS identity at start-up in this order:

1. **Config-drive metadata** (if a drive is mounted at `iso.mount_path`): `virtual_machine_id` and `agent_api_key` override the config file.
2. **`agent.yaml`**: `nats.agent_uuid` and `nats.api_key`.
3. **Environment**: `PLUSCLOUDS_AGENT_NATS_AGENT_UUID` and `PLUSCLOUDS_AGENT_NATS_API_KEY` override the file.

A missing UUID logs a warning and NATS authentication will fail. The HTTPS bearer token is never read from the config drive; set it in `agent.yaml` or `PLUSCLOUDS_AGENT_HTTP_API_KEY`.

## Config-drive metadata (optional)

When the platform provisions a VM it can attach a small ISO whose root contains `metadata.yaml` (or `metadata.json`; YAML wins if both exist). The default mount point is `/media/plusclouds-config`; change it with `iso.mount_path`. The drive is read once at start-up and may be unmounted afterwards. A missing drive is normal and is only logged at debug level.

```yaml
hostname: web-1
username: ubuntu
password: "<initial os password>"
agent_api_key: "<nats token for this agent>"
virtual_machine_id: "550e8400-e29b-41d4-a716-446655440001"
virtualDisks:
  data:
    - disk_type: ssd
      device_number: 0
      total_disk: 53687091200        # bytes
virtualNetworkCards:
  data:
    - device_number: 0
      mac_addr: "aa:bb:cc:dd:ee:ff"
      network:
        data:
          gateway: 192.0.2.1
          netmask: 255.255.255.0
          dns_nameservers: [192.0.2.53]
          mtu: 1500
      ipList:
        data:
          - ip_addr: 192.0.2.10/24
serviceRoles:
  data:
    - name: web
SSHPublicKeys:
  - "ssh-ed25519 AAAA… user@example"
```

The agent uses only `virtual_machine_id` and `agent_api_key` for identity, and `virtual_machine_id` is also reported by `system.info`. The other fields are parsed so the platform and other tools can share one file; they do not change the agent's behaviour. `password` is sensitive: it is never logged or returned by any operation.

## Installing

### Requirements

- Linux with systemd (Windows builds exist but are untested in production).
- For XenServer/XCP-ng features: run as root on the hypervisor (Dom0) with `xe`, `ovs-vsctl` and `ovs-ofctl` installed. `ipmitool` is needed for IPMI sensors. Tested on XenServer 8.2.
- Outbound access to the NATS server (NATS mode), or an inbound firewall rule for the HTTPS port (HTTPS mode).

### Steps

```bash
# 1. Binary (from a release or `make build-linux`; verify against SHA256SUMS)
install -m 0755 plusclouds-agent-linux-amd64 /usr/local/bin/plusclouds-agent

# 2. Directories
mkdir -p /etc/plusclouds /var/log/plusclouds /var/lib/plusclouds/tls
chmod 0750 /var/log/plusclouds

# 3. Config: configs/agent.yaml (NATS) or configs/agent.https.yaml (HTTPS)
install -m 0600 agent.yaml /etc/plusclouds/agent.yaml

# 4. Service
install -m 0644 systemd/plusclouds-agent.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now plusclouds-agent
```

From a source checkout, `make install` installs the binary, the unit and a default config, and `make package-deb` builds a Debian package.

Optional `/etc/plusclouds/environment` (read by the unit) holds `PLUSCLOUDS_AGENT_*` overrides, which keeps secrets out of `agent.yaml`:

```bash
PLUSCLOUDS_AGENT_HTTP_API_KEY=<token>
PLUSCLOUDS_AGENT_NATS_API_KEY=<token>
```

Set its mode to `0600`.

### Choosing a mode

| Mode | Config | Notes |
|---|---|---|
| NATS only (default) | `nats.enabled: true`, `http.enabled: false` | Outbound only; telemetry, heartbeat and events are pushed |
| HTTPS only | `nats.enabled: false`, `http.enabled: true` | Caller drives and polls; nothing is pushed |
| Both | both enabled | Same dispatcher and allowlist for both |

At least one transport must be enabled or the agent refuses to start. See the [README](../README.md) for the full configuration reference and [https-api.md](https-api.md) for HTTPS details.

### Optional tools

- `xenserver.iso.create` needs `genisoimage` (or `mkisofs`) on the host; set `xenserver.iso_binary` if it lives elsewhere. Without it the operation fails with a clear message.
- Callbacks need `agent.callback_allowed_urls` (see [https-api.md](https-api.md#completion-callbacks)).

### Enable only what you need

State-changing operations ship commented out. Uncomment the ones your platform uses under `agent.allowed_operations` and restart. A hardened default for a monitoring-only host is the sample's read-only set.

### systemd sandbox

The sample unit sets `ProtectSystem=strict`, so the agent can write only to `/var/log/plusclouds`, `/etc/plusclouds`, `/var/lib/plusclouds` (logs, config, TLS material) and `/run/sr-mount` (ISO libraries, for `xenserver.iso.create`). If exports or imports target another directory (for example an NFS mount under `/mnt`) and fail with a read-only file system error, add that path to `ReadWritePaths=` in the unit, then `systemctl daemon-reload` and restart.

## Verifying

```bash
systemctl status plusclouds-agent
journalctl -fu plusclouds-agent
```

Expected log lines: `agent started` naming the transports; in NATS mode also `connected to NATS`, `capabilities published` and `heartbeat published`. For HTTPS:

```bash
curl -sk https://<host>:8443/healthz
# {"agent_uuid":"…","agent_version":"v1.2.0","protocol_versions":[1],"status":"ok"}
```

For debugging, stop the service and run it in the foreground with `log.level: debug` in the config:

```bash
plusclouds-agent --config /etc/plusclouds/agent.yaml
```

## Rotating and revoking credentials

- **NATS:** change or remove the agent's token on the platform; the agent is rejected on its next connection attempt. Update `nats.api_key` (or the config drive) and restart to reconnect.
- **HTTPS:** generate a new token (`openssl rand -base64 48`), update `http.api_key` or the environment file, restart the agent, and update callers. There is one token per agent; the old one stops working at restart.
- **TLS certificate:** delete `/var/lib/plusclouds/tls/agent.crt` and `agent.key` and restart to generate a new self-signed pair (clients must re-pin the new fingerprint), or supply your own files.

## Upgrading

Replace the binary and restart; the config is forward compatible, and new operations appear in the catalog automatically but stay disabled until you list them. Running background jobs are cancelled by a restart. Check `/healthz` (HTTPS) or the heartbeat's `version` (NATS) to confirm the new build. Release notes list behaviour changes.

## Uninstalling

```bash
systemctl disable --now plusclouds-agent
rm /usr/local/bin/plusclouds-agent /etc/systemd/system/plusclouds-agent.service
rm -r /etc/plusclouds /var/lib/plusclouds /var/log/plusclouds   # config, certificates, logs
```
