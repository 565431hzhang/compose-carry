# compose-carry

**Check host-path gaps before moving a Docker Compose project.**

[中文说明](README.zh-CN.md)

`compose-carry` compares the source host's bind mounts and file-backed configs/secrets with paths on the destination host. It answers a small question: **which listed paths are missing, have the wrong type, or could not be checked?**

It is not a backup tool, a migration executor, a Compose linter, or a guarantee that your application will start. v0.2 is an early CLI: Linux host scanning, portable offline comparison, no runtime dependencies outside the operating system. Docker Compose is needed only to export your configuration.

## Build

Requires Go 1.24 or later. From the repository root:

```sh
go build -o compose-carry ./cmd/compose-carry
./compose-carry --help
```

For a Linux host from another platform:

```sh
GOOS=linux GOARCH=amd64 go build -o compose-carry ./cmd/compose-carry
```

Use `GOARCH=arm64` for a Linux ARM64 host. Run the scanner directly on the Docker daemon's host, in the filesystem namespace containing its mounts, not in an ordinary container or on a client pointing at a remote daemon. Scanning on Windows/macOS is rejected; offline `compare` works there.

## Three-step workflow

### 1. Source host

Export the effective configuration using the same `-f`, environment, project directory and profile options as your deployment. Do not use `--no-path-resolution` or `--no-interpolate`.

```sh
umask 077
docker compose config --format json > compose.json
./compose-carry source --config compose.json --out source.json
```

**The exported Compose JSON may contain secrets.** Keep it on the source host; do not commit or share it. The scanner parses that input but does not copy environment values, inline config contents, or dependency file contents into its snapshot.

### 2. Destination host

Transfer only `source.json` and the appropriate binary using your own secure transport. Inspect the snapshot before scanning: it names paths to check. By default the paths are identical on both hosts. For different destination paths, provide an explicit map as described below.

```sh
./compose-carry target --manifest source.json --out target.json
```

No SSH, data transfer, directory creation, deployment or application startup is performed by the tool. Snapshot output is explicitly created by the command, but existing output files are never overwritten.

### 3. Compare anywhere

Put both snapshots on the same machine:

```sh
./compose-carry compare --source source.json --target target.json
./compose-carry compare --source source.json --target target.json --format json
```

Illustrative result statuses:

```text
target_missing     service="proxy" kind=config source_path="/srv/proxy/config.json" target_path="/etc/proxy/config.json" ...
type_mismatch      service="db" kind=bind source_path="/srv/db/data" target_path="/mnt/db/data" ...
matched            service="worker" kind=secret source_path="/srv/worker/token" target_path="/srv/worker/token" ...
```

`matched` means **existence and file/directory type only**, not identical contents or working permissions. JSON includes the scope, in-scope/excluded counts, exit code, and each dependency's expected type and both observations.

## Different destination paths

Create a private `paths.json` mapping exact source paths to destination paths:

```json
{
  "/srv/app/data": "/mnt/app/data",
  "/srv/app/config.json": "/etc/app/config.json"
}
```

```sh
./compose-carry target --manifest source.json --path-map paths.json --out mapped.json
./compose-carry compare --source source.json --target mapped.json
```

Keys must name in-scope source dependencies; both sides must be canonical absolute Linux paths. Duplicate keys, unknown sources, non-string values and invalid paths are rejected before scanning. This is **exact matching**, not a directory-prefix rewrite: mapping `/srv/app` does not map `/srv/app/data`. Shared references use the same mapping; omitted paths keep their source location. Existing invalid source paths remain unverified and cannot be repaired through a map.

Reports show `source_path` and `target_path` in text; JSON keeps the source in `dependency.path` and adds `target_path`. Source identity and observations are preserved. The tool does **not** change Compose configuration, move files, or verify that Compose uses the mapped paths. Review the map and the report; mapped success is only about those explicitly selected paths.

No map means snapshot format v1. Supplying a map, even `{}`, creates a target-only v2 snapshot with `target_path` on every entry (empty for excluded entries). Unmapped invalid paths are retained as unverified, never silently normalized. This version reads v1 and v2; v0.1 rejects v2. Use the new binary for both mapped scanning and comparison. Mapping files contain sensitive path metadata and share the 16 MiB input limit.

## What is checked

- Long-form bind mounts from rendered Compose JSON; their expected type comes from the source host's metadata, not the path's extension.
- Only service-referenced, file-backed `configs` and `secrets`; these must be regular files.
- The exact absolute source path, or its explicit mapped destination path. File/ancestor symlinks are not followed; special files, unreadable metadata, missing source paths and non-canonical/unresolved paths remain unverified.
- Snapshot version, exact field names, dependency IDs, duplicate entries, legal states, complete dependency sets and preserved source evidence. Mismatched snapshots fail rather than silently comparing their intersection.

Named volumes, non-bind mounts, external/environment/inline config and secret sources are recorded as excluded entries. Networks and other Compose features are outside the scope and are not inventoried. Excluded entries are never counted as verified. An exit code of zero applies only to in-scope paths; exclusions do not independently make it nonzero when in-scope paths match.

Paths containing `$` or backslashes are conservatively unverified even if those characters are literal. Relative paths and paths needing normalization are also unverified. This is not a full Compose schema validator; give it the effective JSON produced by Compose, not raw YAML or hand-authored shorthand.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | At least one in-scope path; all in-scope checks succeeded (and match for target/compare). |
| `1` | A gap, type mismatch, unverified source/target, or no in-scope paths. |
| `2` | Bad arguments/input/snapshot, unsupported scan platform, or a command-level read/write failure. |

A scan that exits `1` still writes a valid snapshot; use it for diagnosis and comparison. Do not chain all three steps with `&&` if you want a report despite gaps. Use a new output filename for each scan.

## Safety and limits

- No network calls, dependency file-content reads, recursive directory walks, or modifications to scanned paths. Linux scanning uses metadata-only handles and rejects symlinks at every path component.
- Reads of the explicitly supplied Compose JSON and snapshot files are necessary. Inputs and generated snapshots are limited to 16 MiB; an oversized snapshot is rejected before creating an output file. Outputs use mode `0600` on Linux; stdout reports and redirected JSON inherit your shell's permissions.
- Service names, references and absolute paths can still reveal sensitive metadata. Do not publish real snapshots. There is no general-purpose redaction or signing mechanism.
- A snapshot is a point-in-time observation, not proof of identity, freshness or authenticity. IDs detect inconsistent metadata, not maliciously coordinated edits. Two snapshots with identical dependencies and source observations are compatible regardless of which hosts produced them; choose the right files.
- Metadata visibility is not container access. Ownership, UID/GID, ACLs, SELinux, mount flags, file contents, named-volume contents, networks, available images, database consistency and application readiness are **not validated**.
- Inspect untrusted manifests before running them, avoid unnecessary root privileges, and keep inputs/output directories under your control. Paths on unavailable network filesystems can stall kernel filesystem operations; no scan timeout is promised.

## Development

```sh
go test ./...
go vet ./...
```

Linux tests exercise ancestor symlinks, FIFOs, missing/type-mismatched paths, non-root permission denial, and a built-binary source → target → compare flow. The end-to-end test changes temporary fixture paths between snapshots; it does not connect two real servers. Run the optional real Compose-export integration test with:

```sh
COMPOSE_CARRY_TEST_COMPOSE=1 go test ./cmd/compose-carry -run TestComposeExport -v
```

That test invokes `docker compose config`, not `up`, and pulls no images. CI runs Linux tests as a non-root user, exercises a real Compose export, and cross-compiles amd64/arm64 binaries. Windows tests cover offline comparison and explicit scan rejection.

## Direction

Keep the project focused on host dependency gaps. Potential next increments, guided by actual migration cases: external volume/network metadata and carefully scoped permission diagnostics. No deployment platform or automatic migration is planned.

[Replicated Troubleshoot](https://github.com/replicatedhq/troubleshoot), [DCLint](https://github.com/zavoloklom/docker-compose-linter), and [Compose Volume Check](https://github.com/Profen-BLF/compose-volume-check) solve adjacent problems; this project concentrates on checking the destination host against a source-host path snapshot, rather than replacing those tools. No claim of being the first or only such tool is made.

MIT licensed. Developed with AI assistance; review and testing remain necessary.
