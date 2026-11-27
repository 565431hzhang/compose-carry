# compose-carry

**Check host-path gaps before moving a Docker Compose project.**

[中文说明](README.zh-CN.md)

`compose-carry` compares the source host's bind mounts and file-backed configs/secrets with paths on the destination host. It answers a small question: **which listed paths are missing, have the wrong type, or could not be checked?**

It is not a backup tool, a migration executor, a Compose linter, or a guarantee that your application will start. v0.6 is an early CLI: Linux host-path scanning, portable offline comparison, and an optional Docker CLI-based external resource check. Docker Compose exports your configuration; `resources` and the external-resource section of `check` require Docker CLI access to a daemon.

## Quick start: one bundle, two commands (Linux)

Requires v0.6 or a current source build; an older published binary does not include `export/check`. First prepare the effective Compose JSON **on the source host**, using your deployment's file/environment/profile options:

```sh
umask 077
docker compose config --format json > compose.json
```

**Source host — export:**

```sh
./compose-carry export --config compose.json
```

This creates `carry.json` containing a source path snapshot and minimal external-resource manifest. Keep `compose.json` private on the source; transfer only `carry.json` and the appropriate binary yourself. Export does not query Docker, copy dependency file contents or perform migration. Successful export returns `0` even with source problems; its summary names unverified/excluded entries. Existing output is never overwritten; use `--out another.json` for another run.

**Destination host — check:**

```sh
./compose-carry check --docker-context default
```

Run on the destination Linux host in the filesystem namespace containing its mounts. Use the intended Docker context rather than blindly copying `default`: a remote context is not necessarily this path-scanning host. If there are no external resources, just `./compose-carry check` suffices. With resources but no context, paths are still checked, resources stay `unverified/context_required`, and the command returns `1` without calling Docker.

One report covers both sections; successful rows are hidden by default. No target snapshot file or separate compare step is needed. Use `--all` for all rows, `--format json` for automation, and `--bundle another.json` for another input. Existing `--path-map` and `--resource-map` files work when destination locations/names differ.

Check returns `0` only with at least one in-scope path/resource and all scoped checks passing; gaps, unverified entries, or nothing to check return `1`; input/platform/output errors return `2`. An empty section does not fail a successful other section. `--fail-on-excluded` also fails on path exclusions, even when an overlapping external resource is present: existence does not validate what was excluded from path checks.

The bundle is a strict versioned JSON file, capped at **16 MiB total**, with private `0600` output on Linux. Names/paths may still be sensitive; validation is not authentication or proof of completeness. `check` never modifies the bundle. JSON has one top-level `exit_code`, full per-section summaries and explicit `only_problems`/`fail_on_excluded` flags; filtered results are arrays, not complete inventories. The two sections may overlap, so do not add their counts as unique dependencies.

Both new commands require Linux. Existing commands below remain available for advanced workflows and cross-platform offline comparison. No SSH, deployment, automatic transfer or repair is performed.

## Install a packaged binary

Binary packages are published only after a release draft is reviewed. If the [Releases page](https://github.com/565431hzhang/compose-carry/releases) has no published assets, use the source build below; GitHub's automatic source archives are not binary packages.

| Host | Package suffix | Support |
| --- | --- | --- |
| Linux x86-64 | `linux_amd64.tar.gz` | All commands |
| Linux ARM64 | `linux_arm64.tar.gz` | All commands; current source and packages tested on native ARM64 CI |
| Windows x86-64 | `windows_amd64.zip` | Offline compare and Docker resources; no path scan |

Download the package for your host and `SHA256SUMS` from the **same version** into a new empty directory. On Linux, verify the downloaded package before extracting (replace VERSION and ARCH with the selected release and architecture):

```sh
sha256sum --check --ignore-missing SHA256SUMS
mkdir unpacked
tar -xzf compose-carry_VERSION_linux_ARCH.tar.gz -C unpacked
mkdir -p "$HOME/.local/bin"
install -m 0755 unpacked/compose-carry "$HOME/.local/bin/compose-carry"
"$HOME/.local/bin/compose-carry" --version
```

Require an `OK` for the archive you downloaded; do not proceed on an error or if nothing was checked. Add `$HOME/.local/bin` to your shell PATH if needed. No root privileges are required.

On Windows, compare the hash to the matching filename in `SHA256SUMS` before extraction:

```powershell
Get-FileHash .\compose-carry_VERSION_windows_amd64.zip -Algorithm SHA256
Get-Content .\SHA256SUMS
Expand-Archive .\compose-carry_VERSION_windows_amd64.zip -DestinationPath .\unpacked
New-Item -ItemType Directory -Force "$HOME\AppData\Local\Programs\compose-carry"
Copy-Item .\unpacked\compose-carry.exe "$HOME\AppData\Local\Programs\compose-carry\compose-carry.exe"
& "$HOME\AppData\Local\Programs\compose-carry\compose-carry.exe" --version
```

Add that installation directory to your user PATH using Windows environment settings if desired. Package version and commit are printed by `--version`. Checksums detect corrupted downloads; they are not signatures and do not establish publisher identity.

To upgrade, close the executable, keep a copy of the old binary, verify the new package, and replace only the installed executable (PowerShell may require `Copy-Item -Force`). To uninstall, delete the installed executable and remove any PATH entry you added. Your snapshots and configuration are separate and are not deleted.

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

Use `GOARCH=arm64` for a Linux ARM64 host. Run the scanner directly on the Docker daemon's host, in the filesystem namespace containing its mounts, not in an ordinary container or on a client pointing at a remote daemon. Scanning on Windows/macOS is rejected; offline `compare` works there. `resources` requires a working Docker CLI/context rather than local Linux path access.

## Advanced: separate path snapshots and comparison

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

The path workflow performs no SSH, data transfer, directory creation, deployment or application startup. Snapshot output is explicitly created by the command, but existing output files are never overwritten.

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

## Focus on problems and control exclusions

```sh
./compose-carry compare --source source.json --target target.json --only-problems
./compose-carry compare --source source.json --target target.json --only-problems --fail-on-excluded --format json
```

Every comparison includes a full `summary` with six counts: `matched`, `target_missing`, `type_mismatch`, `source_unverified`, `target_unverified`, and `excluded`, including zeros. Counts are per dependency entry, not unique path; `checked` is the number of in-scope entries, not successes.

`--only-problems` hides only matched result rows, retaining exclusions. It never changes the full summary, counts or exit decision. JSON explicitly includes `only_problems` and `fail_on_excluded`; a filtered empty `results` is `[]`, not a claim that nothing was checked. Text shows the full summary before rows and the number shown.

`--fail-on-excluded` returns `1` when any **recorded** excluded entry exists, even when all in-scope paths match. Default behavior remains unchanged. These comparison options do not change source/target behavior or snapshot formats v1/v2; unified `check` uses `--all` for display and also supports `--fail-on-excluded`. Networks and other uninventoried resources remain unchecked even with this option: this is not a complete migration gate.

For successfully written JSON, `exit_code` equals the process exit code. An output failure returns `2`; any partial report must be discarded.

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

## External Docker volumes and networks

```sh
./compose-carry resources --config compose.json --docker-context destination --format json
```

Use an existing, explicitly selected Docker context (`default` is allowed). Endpoint overrides (`DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_TLS`, `DOCKER_TLS_VERIFY`, `DOCKER_CERT_PATH`) are removed only from the query subprocess environment; context storage and credentials are preserved. This separate command reads rendered Compose JSON or a minimal resource manifest and queries that context's daemon, which may be remote. It does not consume or change path snapshots, merge reports, transfer configuration, or verify that the selected daemon is the host used for path scanning. Keep the rendered input private; do not transfer a secret-containing configuration just to run this command.

Only service-referenced `external: true` volumes and networks are checked. The top-level `name` overrides the logical key without a project prefix. Shared references are grouped by kind/name; the report retains service/key references. Implicit external `default` networks are included unless the service uses `network_mode`. Managed resources, anonymous volumes and unused definitions are ignored. Use rendered JSON with boolean `external`, not legacy object syntax.

The tool requests projected volume names and network names/full IDs using read-only list commands, once per needed category. Volumes match exact names; networks match exact names, full IDs or unique ID prefixes. Duplicate names or name/ID conflicts are conservatively `unverified`, without guessing Docker's resolution priority. Names containing `$` or control characters remain unverified.

Results are `present`, `missing`, or `unverified`, with summary counts and reason codes. Missing means absent from a successfully parsed list. Failed queries, missing Docker CLI, timeouts, oversized output or malformed inventories never mean missing. Each query has a 15-second deadline, a 16 MiB stdout cap and a one-second post-exit pipe wait limit; raw stderr is discarded. Only referenced resources are reported, not unrelated daemon inventory.

Exit codes: `0` requires at least one resource and all present; `1` means missing, unverified or no resources; `2` means input/argument/report-write failure. No resources means no Docker call. Presence does not prove volume contents, driver/subnet compatibility, connectivity, permissions or application readiness. Docker contexts and their transport helpers are user-managed and must be trusted; no containers are started and no Docker resources are modified by this command.

### Carry only the resource manifest

On the source machine, export a minimal manifest without contacting Docker:

```sh
./compose-carry resource-manifest --config compose.json --out resources.json
```

Transfer only `resources.json` using your own secure transport, then check the selected destination context:

```sh
./compose-carry resources --manifest resources.json --docker-context destination --format json
```

The manifest contains only referenced external resource kinds, names, and service/logical-key references. Environment values, inline secrets, labels, driver options and daemon details are not copied. Names can still be sensitive: inspect the file before sharing it. The original Compose export stays private on the source. Export works without Docker installed and returns `0` even for an empty list: **export is not verification**. Checking an empty manifest returns `1` without invoking Docker.

`--config` and `--manifest` are mutually exclusive. Resource manifests use a separate `compose-carry.resources` version 1 format, not path snapshots. Unknown fields/versions, duplicate keys/resources and conflicting references are rejected; this does not prove authenticity, freshness or completeness. Inputs and generated manifests are capped at 16 MiB. Outputs are new files only, mode `0600` on Linux, never overwritten. A failed summary write can return `2` after the manifest was already written; inspect it rather than blindly retrying to the same filename.

### Renamed destination resources

Create a private `names.json`; categories may be omitted:

```json
{
  "volumes": {"old-db-data": "new-db-data"},
  "networks": {"old-edge": "new-edge"}
}
```

```sh
./compose-carry resources --manifest resources.json --resource-map names.json --docker-context destination --format json
```

Maps also work with `--config`. Keys match the complete original name in their category, not a Compose logical key or prefix. Unmapped resources retain their original names. Unknown sources, duplicate keys, empty/non-string names, unresolved variables/control characters and many-to-one target-name collisions (including unmapped resources) fail before any Docker query. Volume and network names are separate namespaces. Network target references still support full IDs and unique ID prefixes; different references resolving to the same Docker object are not detected as name collisions, and presence is not identity verification.

Reports preserve `name` and `references`, adding `checked_name` for the actual query target. No resource is renamed or created, Compose is not rewritten, and source identity, volume content and network equivalence are not verified. Existing path snapshots v1/v2 and their commands are unchanged. No-map `resources --config` retains its prior status/exit behavior with the additional `checked_name` field.

### Resource diagnostics

Failures remain `unverified` with exit `1`; JSON and text include a fixed `hint` without raw stderr, partial stdout or exception details. In v0.4.1, query reasons distinguish:

| Reason | Next step |
| --- | --- |
| `command_not_found` | Install Docker CLI and check PATH. |
| `timeout` | Check selected context/daemon connectivity, then retry. |
| `output_limit` | Inspect the oversized inventory locally; stdout exceeded 16 MiB. |
| `command_failed` | Check context, access and credentials with Docker CLI locally. |
| `query_canceled` | Retry when ready; cancellation is not a timeout. |
| `io_error` | Check executable permissions or process/pipe failures. |
| `query_failed` | Unclassified failure; inspect Docker locally. |
| `invalid_inventory` | Check Docker CLI compatibility and projected list output. |
| `ambiguous_reference` | Use an unambiguous full ID/name and regenerate Compose JSON. |
| `unresolved_name` | Resolve Compose variables and inspect the name. |

`not_found` still means missing from a valid inventory and now suggests checking the name/context. `found` has no hint. `command_failed` does not prove the daemon is offline or access is denied: those require examining Docker's own diagnostics locally. Existing statuses/exit codes and path snapshot formats are unchanged. Consumers should accept unknown future reason codes instead of treating them as success.

## What the path workflow checks

- Long-form bind mounts from rendered Compose JSON; their expected type comes from the source host's metadata, not the path's extension.
- Only service-referenced, file-backed `configs` and `secrets`; these must be regular files.
- The exact absolute source path, or its explicit mapped destination path. File/ancestor symlinks are not followed; special files, unreadable metadata, missing source paths and non-canonical/unresolved paths remain unverified.
- Snapshot version, exact field names, dependency IDs, duplicate entries, legal states, complete dependency sets and preserved source evidence. Mismatched snapshots fail rather than silently comparing their intersection.

Named volumes, non-bind mounts, external/environment/inline config and secret sources are recorded as excluded entries. Networks and other Compose features are outside the scope and are not inventoried. Excluded entries are never counted as verified. An exit code of zero applies only to in-scope paths; by default, exclusions do not independently make it nonzero when in-scope paths match.

Paths containing `$` or backslashes are conservatively unverified even if those characters are literal. Relative paths and paths needing normalization are also unverified. This is not a full Compose schema validator; give it the effective JSON produced by Compose, not raw YAML or hand-authored shorthand.

## Path workflow exit codes

| Code | Meaning |
| --- | --- |
| `0` | At least one in-scope path; all in-scope checks succeeded (and match for target/compare). |
| `1` | A gap, type mismatch, unverified source/target, no in-scope paths, or recorded exclusions with `compare --fail-on-excluded`. |
| `2` | Bad arguments/input/snapshot, unsupported scan platform, or a command-level read/write failure. |

A scan that exits `1` still writes a valid snapshot; use it for diagnosis and comparison. Do not chain all three steps with `&&` if you want a report despite gaps. Use a new output filename for each scan.

## Safety and limits

- The path workflow makes no network calls, dependency file-content reads, recursive directory walks, or modifications to scanned paths; `resources` and `check` with an explicit context query Docker. Linux scanning uses metadata-only handles and rejects symlinks at every path component.
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

That test invokes `docker compose config`, not `up`, and pulls no images. The native Linux CI jobs run race, Compose export, Docker resource and package smoke tests on both AMD64 and ARM64. Windows tests cover offline comparison and explicit scan rejection.

To explicitly test against a disposable Docker daemon, the integration test creates uniquely named empty volumes and internal networks, removes them by recorded name/ID, and verifies cleanup. It never starts containers:

```sh
COMPOSE_CARRY_TEST_RESOURCES=1 go test ./cmd/compose-carry -run TestDockerResourcesIntegration -v
```

The resource integration test is opt-in locally and explicitly enabled on disposable native Linux CI runners. Unit tests cover failed queries, ambiguity, output limits and deadlines without a daemon.

### Native architecture and migration CI

Two source jobs run concurrently on standard `ubuntu-24.04` and `ubuntu-24.04-arm`. Each runs two independent privileged DinD containers: source and destination. The scanner executes inside the corresponding filesystem namespace as UID 1000 with zero effective capabilities; only daemon setup is privileged. Tests cover missing/type-mismatched paths, repairs, mapping, symlinks, permission denial and independent external resources. No host Docker socket is mounted and no daemon TCP port is published.

Two destination jobs then run concurrently, checking an actual AMD64-generated export bundle on native ARM64 and the reverse. Each bundle includes the source path snapshot and minimal resource manifest; tests cover missing resources, context-required diagnostics, repairs, both maps and strict exclusions without copying the source Compose configuration or writing a target snapshot. Only a small synthetic bundle, bounded with its envelope to 16 KiB after base64 encoding, crosses jobs through outputs; missing/mismatched outputs fail the test. No QEMU or external relay is used. These are container-isolated environments, not two physical servers.

All jobs require a public repository and use standard runners. Linux jobs stop after 25 minutes, Windows after 10. Setup caches are disabled; no Actions artifacts or persistent build caches are uploaded. Packages exist only temporarily on the runner and are removed after tests. DinD containers and their anonymous volumes are removed on normal/failure exits with an always-run fallback; GitHub disposes of the runner after hard cancellation. Explicit tag-triggered Release attachments are separate from test artifacts. Free execution relies on GitHub's public-repository standard-runner policy; these safeguards do not control other account workflows or charges.

## Packaging and release review

From a clean committed checkout, with Go and Git on PATH:

```sh
go run ./cmd/package-release --out dist
COMPOSE_CARRY_RELEASE_DIR=../../dist go test ./cmd/package-release -run TestReleaseArtifacts -count=1 -v
```

`dist` must not exist. The packager produces three archives and a separate `SHA256SUMS`, with only the executable, license and two READMEs in each archive. `internal/buildinfo/version.go` is the single version source; `--version vX.Y.Z` must match it. Source builds show `commit dev`, while packaged binaries embed the actual commit. Archives have fixed metadata/order; byte reproducibility across different Go versions is not promised.

The tag-triggered release workflow accepts only `vMAJOR.MINOR.PATCH` matching the source version, waits for the Linux/Windows test workflow, and creates a **draft** release. No tag is pushed automatically. An existing release is never overwritten; review notes and assets before manually publishing. CI is configured to check package hashes/contents and execute native Windows amd64 and Linux amd64/arm64 packages. Consult the corresponding workflow result for actual validation; historical release notes are not rewritten.

## Direction

Keep the project focused on host dependency gaps. Potential next increments, guided by actual migration cases: richer external resource metadata and carefully scoped permission diagnostics. No deployment platform or automatic migration is planned.

[Replicated Troubleshoot](https://github.com/replicatedhq/troubleshoot), [DCLint](https://github.com/zavoloklom/docker-compose-linter), and [Compose Volume Check](https://github.com/Profen-BLF/compose-volume-check) solve adjacent problems; this project concentrates on checking the destination host against a source-host path snapshot, rather than replacing those tools. No claim of being the first or only such tool is made.

MIT licensed. Developed with AI assistance; review and testing remain necessary.
