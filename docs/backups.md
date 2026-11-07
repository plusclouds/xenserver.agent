# Backups and restore

The agent can snapshot VMs, export them to files on a mounted NFS share (or any local path), copy and clone disks, and bring exports back. Exports run in the background with progress so a caller never waits on a long request.

All operations below are in [operations.md](operations.md). State-changing ones must be enabled in `allowed_operations`.

## What you can back up

| Goal | Operation | Result |
|---|---|---|
| Point-in-time copy kept on the same storage | `xenserver.vm.snapshot_create` | Snapshot (fast; not a backup against storage loss) |
| Snapshot as a file | `xenserver.vm.snapshot_export` | `.xva` image |
| Whole VM as a file | `xenserver.vm.export` | `.xva` image (VM should be halted or snapshotted first) |
| One disk as a raw file | `xenserver.vdi.export` | raw or other format file |
| Disk to another SR | `xenserver.vdi.copy` | New disk on the target SR |
| Fast same-SR copy | `xenserver.vdi.clone` | Copy-on-write clone |
| VM copy | `xenserver.vm.clone` | New VM |

For consistent backups of a running VM, export a **snapshot** rather than the live VM.

## Storage target

Mount an NFS share on the host, then export into it:

```json
{"operation":"nfsmount.mount","params":{"server":"nas.example.com","export_path":"/backups","mount_point":"/mnt/backup"}}
```

`server` must be a hostname or IP, `export_path` and `mount_point` absolute paths. Mount options are limited to letters, digits and `_ = . , : / -`. The share must be writable by root on the host (a server using `root_squash` will refuse); size the share for the exports. `nfsmount.list` shows current mounts, and `nfsmount.list_files` lists a mount's files by name, size or age, which is handy for choosing an image to restore.

## Snapshot, export, list

```text
1. xenserver.vm.snapshot_create   {"uuid":"<vm>","label":"nightly"}          → snapshot uuid in output
2. xenserver.vm.snapshot_export   {"uuid":"<snapshot>","filename":"/mnt/backup/web-1-2026-01-01.xva","async":true}
3. agent.job.status               {"job_id":"…"}                              → poll until completed
4. xenserver.vm.snapshot_delete   {"uuid":"<snapshot>"}                       → free the snapshot
```

Rules enforced by the agent:

- `filename` must be an absolute path and **must not exist**: exports never overwrite.
- Data is written to `<filename>.part` and renamed only when the export succeeds, so a finished file is always complete. A leftover `.part` file (after a crash) blocks a retry to the same name until it is removed.
- Cancelling a job, or a failed export, removes the partial file.
- `snapshot_export` and `vm.export` report size and duration in their result.

## Background jobs and progress

Add `"async": true`. The call returns immediately (`accepted`, HTTP 202) with a `job_id`. Poll `agent.job.status`:

- `progress.bytes_done` is the size of the file written so far.
- `progress.bytes_total` is an **estimate**: the allocated size of the disk chain, an upper bound. The real file is usually smaller, so `percent` is indicative and capped at 99 until the job truly finishes.
- Imports report elapsed time only.

At most `agent.max_jobs` (default 2) jobs run together; further requests fail until one ends. Jobs live in memory: an agent restart cancels the running job (its partial file is removed) and forgets history.

## Restore

A snapshot export imports as a **template**; promote it to a VM:

```text
1. xenserver.vm.import            {"filename":"/mnt/backup/web-1-2026-01-01.xva","sr_uuid":"<target-sr>","async":true}
2. agent.job.status               → when done, result.output is the imported object's uuid
3. xenserver.vm.promote_template  {"uuid":"<imported-template>","new_name":"web-1-restored"}
4. xenserver.vm.start             {"uuid":"<new-vm>"}
```

A whole-VM export (`vm.export`) imports as a VM directly. `vm.import` checks that the file and SR exist before starting, returns the new uuid in `data.uuid` (also in the async job result) and can name it with `new_name`. `nfsmount.mount` is idempotent: mounting the same export again succeeds, a different export at the same mount point is refused. This round trip (snapshot export, import, promote, boot) is tested against a real XenServer 8.2 host.

`xenserver.vm.import` with `preserve: true` keeps the original MAC addresses; leave it off when restoring next to the original to avoid duplicates.

## Disk-level workflow

```text
xenserver.vdi.export  {"vdi_uuid":"<vdi>","filename":"/mnt/backup/disk.raw","async":true}
xenserver.vdi.copy    {"vdi_uuid":"<vdi>","sr_uuid":"<other-sr>","async":true}
```

`vdi.export` needs the local xapi socket (`xenserver.xapi_socket_path`).

## Failure behaviour

| Situation | Behaviour |
|---|---|
| Target exists | Rejected before any work |
| Target not writable / share not mounted | Failed with the system's message |
| Export fails or is cancelled | Partial `.part` file removed |
| Agent restarted or stopped mid-export | The job is cancelled, the export process stops and the partial file is removed |
| Agent killed hard (`kill -9`, power loss) | The export process still stops, but the `.part` file may remain; delete it before retrying |
| NFS share becomes unreachable | The export fails when the operating system gives up; commands are terminated after a short grace period instead of hanging the agent |
| Too many jobs | Request fails; retry when one finishes |

## Not included

There is no built-in scheduling, retention or off-site (for example S3) upload. Drive those from your own tooling using the operations above.
