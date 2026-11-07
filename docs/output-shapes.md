# Output shapes

What the read operations return in `payload.output`. Two families exist on purpose:

- **Summary operations** (`*.list`, `*.get`, `host.info`, `pool.info`): a small, stable record with **snake_case** field names and typed values (numbers and booleans, sizes in bytes). Meant for dashboards and quick lookups.
- **Parameter operations** (`*.param_list`, `*.param_get`, `inventory.snapshot`): the full xe parameter set with **xe-style keys** exactly as `xe` names them (`power-state`, `VCPUs-max`, `memory-static-max`, `PV-drivers-up-to-date`). All values are strings, as xe prints them. Use these when you need a field a summary does not carry.

Both are kept; pick per use. Operation names are prefixed `xenserver.` below.

## Parameter list shape

```json
{
  "uuid":   "<object uuid>",
  "params": { "name-label": "web-1", "power-state": "running", "VCPUs-max": "2" },
  "maps":   { "other-config": { "key": "value" }, "software-version": { "product_version": "8.2.1" } },
  "sets":   { "allowed-operations": ["clean_shutdown", "hard_reboot"] }
}
```

- `params`: scalar fields. `maps`: xe map fields as objects. `sets`: xe set fields as arrays.
- Keys are the bare xe names: the access annotations `( RO)`, `( RW)`, `(SRO)`, `(MRW)` and tags such as `[DEPRECATED]` are removed.
- Secrets (any key containing `password`, `secret`, `passwd` or `token`, such as an SR's `device-config`) are replaced with `***`.
- An empty value is an empty string; a map or set with no entries is `{}` or `[]`.
- A map value that itself contains `"; "` is split heuristically; read exact values with `*.param_get` and `param_key`.

Operations returning this shape: `vm.param_list`, `host.param_list`, `pif.param_list`, `network.param_list`, `vlan.param_list`, `sr.param_list`, `pbd.param_list`, `vdi.param_list`, `vbd.param_list`, `vif.param_list`. Selectors: `vm.param_list` takes `uuid` or `name`; `host.param_list` takes `uuid` (default: this host); `sr.param_list` takes `uuid` or `name`; `vdi/vbd/vif.param_list` take their own uuid; the rest take `uuid`.

Fields callers commonly need (xe names, all in `params` unless noted):

| Object | Fields |
|---|---|
| host | `uuid`, `name-label`, `hostname`, `address`, `enabled`, `memory-total`, `memory-free`, `memory-free-computed`, `capabilities` (set), maps `software-version` (`product_version`, `product_brand`, `xapi`, …), `cpu_info` (`cpu_count`, `socket_count`, `modelname`, `vendor`, `speed`, …), `other-config` (`boot_time`, `agent_start_time`, …); sets `PIFs`, `PBDs`, `resident-VMs` |
| pif | `device`, `MAC`, `IP`, `netmask`, `gateway`, `DNS`, `IP-configuration-mode`, `VLAN`, `MTU`, `network-uuid`, `host-uuid`, `currently-attached`, `management`, `physical`, `bond-slave-of`, `bond-master-of`, `VLAN-master-of`, `carrier` |
| network | `uuid`, `name-label`, `bridge`, `MTU`, `managed`, sets `PIF-uuids`, `VIF-uuids`, map `other-config` |
| sr | `uuid`, `name-label`, `type`, `content-type`, `shared`, `physical-size`, `physical-utilisation`, `virtual-allocation`, sets `PBDs`, `VDIs`, `allowed-operations` |
| pbd | `uuid`, `sr-uuid`, `host-uuid`, `currently-attached`, map `device-config` (secrets masked) |
| vm | `uuid`, `name-label`, `power-state`, `VCPUs-max`, `VCPUs-at-startup`, `memory-static-max`, `memory-dynamic-max`, `memory-actual`, `is-a-snapshot`, `is-a-template`, `is-control-domain`, `HVM-boot-policy`, `PV-drivers-detected`, `resident-on`, maps `other-config`, `platform`, `xenstore-data`; sets `VIFs`, `VBDs`, `allowed-operations` |

`vm.param_list` (and `inventory.snapshot` with `include_vms`) also include a `VIFs` set with the VM's network card uuids. XenServer's own `vm-param-list` does not print one, so the agent adds it; for card details use `vif.list`.

Host **uptime** is not an xe field: use `system.info` (`uptime`, `boot_time`) on the same agent, or `other-config:boot_time` of the host.

## Summary shapes

All sizes are bytes; booleans and numbers are typed.

| Operation | Returns | Fields |
|---|---|---|
| `pool.info` | object | `uuid`, `name_label`, `master`, `ha_enabled`, `host_count`, `is_pool_member` (more than one host) |
| `host.list` | array | `uuid`, `name_label`, `hostname`, `address`, `enabled` |
| `host.info` | object | `uuid`, `name_label`, `hostname`, `address`, `enabled`, `software_version` (raw string) |
| `vm.list` | array | `uuid`, `name_label`, `power_state`, `resident_on`, `vcpus`, `memory_bytes` (the VM's current actual memory). By default control domains and templates are excluded; filters `name_label`, `is_control_domain`, `is_a_template` change that and keep this shape. With `fields` (xe parameter names) each row is instead an object of exactly those xe-style string fields |
| `vm.get` | object | `uuid`, `name_label`, `description`, `power_state`, `resident_on`, `vcpus`, `memory_bytes`, `os_version`, `is_a_template` |
| `vm.snapshot_list` | array | `uuid`, `name_label`, `snapshot_of`, `snapshot_time` |
| `vm.disk_list` | array | `position`, `vbd_uuid`, `type` (`Disk`/`CD`), `mode`, `bootable`, `currently_attached`, and for disks `vdi_uuid`, `name_label`, `virtual_size_bytes`, `sr_uuid` |
| `sr.list`, `sr.get` | array / object | `uuid`, `name_label`, `type`, `physical_size_bytes`, `physical_utilisation_bytes` (filters: `type`, `name_label`, `content_type`) |
| `vdi.list` | array | `uuid`, `name_label`, `virtual_size_bytes`, `physical_utilisation_bytes`, `sr_uuid`, `type` |
| `network.list` | array | `uuid`, `name_label`, `bridge` (filters: `name_label`, `bridge`, `pif_uuid`) |
| `vif.list` | array | `uuid`, `device`, `mac`, `network_uuid`, `locking_mode`, `ipv4_allowed`, `ipv6_allowed` |
| `pif.list` | array | `uuid`, `device`, `vlan` (-1 = untagged), `network_uuid`, `host_uuid`, `mac`, `currently_attached`, `ip`, `netmask`, `gateway`, `management`, `physical`, `mtu`, `bond_slave_of`, `bond_master_of` (filters: `vlan`, `network_uuid`, `host_uuid`) |
| `vlan.list` | array | `uuid`, `tag`, `tagged_pif`, `untagged_pif` |
| `pbd.list` | array | `uuid`, `sr_uuid`, `host_uuid`, `currently_attached`, `device_config` (secrets masked) |
| `task.list` | array | `uuid`, `name_label`, `status`, `progress`, `created`, `finished`, `result`, `error_info` |

## One-call snapshot

`inventory.snapshot` returns the pool and every host-level object in one request, each as a parameter list:

```json
{
  "pool":     { "uuid": "…", "name_label": "…", "master": "…", "ha_enabled": false, "host_count": 1, "is_pool_member": false },
  "hosts":    [ { "uuid": "…", "params": {…}, "maps": {…}, "sets": {…} } ],
  "pifs":     [ … ],
  "networks": [ … ],
  "vlans":    [ … ],
  "srs":      [ … ],
  "pbds":     [ … ]
}
```

With `"include_vms": true` it adds `"vms": [ … ]` (guest VMs only: control domains, templates and snapshots are excluded). Cost is one `xe` call per class on the agent, so it scales with the number of objects; leave `include_vms` off unless needed.

## Errors and `success`

Read operations either return the shapes above with `status: completed`, or `status: failed` with a readable `message` (for example an unknown uuid). State-changing operations return the action record described in [https-api.md](https-api.md#output-for-xenserver-operations).
