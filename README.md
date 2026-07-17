# Nox

Nox runs coding agents against local Git branches inside disposable Docker containers isolated with [gVisor](https://gvisor.dev/).

It gives an agent a clean copy of your repository, runs an explicit validation command, and publishes successful changes as a new local branch. Your original checkout is never mounted into the container or switched by Nox.

```text
local Git ref
  → isolated clone
  → Docker + runsc sandbox
  → coding agent
  → validation
  → squashed local branch
  → teardown
```

> [!IMPORTANT]
> Nox v0 runs locally on a Linux Docker host or on macOS through Colima. It does not provision remote hosts or provide hosted execution.

## What Nox guarantees

- Resolves the source ref to an exact committed SHA.
- Excludes uncommitted source-worktree changes with a warning.
- Runs the agent and validation in an isolated, Nox-owned clone.
- Requires Docker's `runsc` runtime; it never silently falls back to `runc`.
- Publishes successful changes as one host-generated squashed commit.
- Creates only the requested local branch—no checkout, push, or pull request.
- Removes the container on success, failure, cancellation, or timeout.
- Retains failed workspaces and logs for investigation.

## Requirements

The curl installer downloads the source archive and builds Nox locally, so it requires `curl`, `tar`, and Go compatible with the version declared in `go.mod`. Direct checkout installs only require Go.

For local execution, Nox also requires:

- Git and Docker CLI
- Linux Docker daemon with the `runsc` runtime registered, or macOS with Colima
- Codex authentication under `~/.codex` when using the Codex adapter

On macOS, the local installer can install the Docker CLI and Colima with Homebrew, create a dedicated Colima profile, install and register `runsc`, build the runner image, and run `nox doctor`. Homebrew itself must already be installed. On Linux, it expects Docker and `runsc` to already be installed and registered; the installer does not modify the host Docker installation.

For a Linux host, install Docker and follow the [gVisor installation instructions](https://gvisor.dev/docs/user_guide/install/), then register the runtime and verify it before running the installer:

```bash
sudo /usr/local/bin/runsc install
sudo systemctl restart docker
docker info --format '{{json .Runtimes}}'
```

## Install

The v0 installer downloads the source archive and builds Nox locally because Nox does not publish release artifacts yet:

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash
```

This builds the CLI into `~/.local/bin/nox`. Add that directory to `PATH` if needed.

To install the CLI and prepare the local execution backend in one step:

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- --local
```

You can also run `./install.sh` directly from a Nox checkout.

On macOS, `--local` provisions the `nox` Colima profile and registers `runsc`. It selects the matching `colima-nox` Docker context for subsequent Nox commands; use `docker context use <name>` to switch back later. On Linux, it validates the existing Docker/`runsc` setup. It also builds the default `nox-runner:v0` image and runs:

```bash
nox doctor
```

Expected output:

```text
ok: Docker can run nox-runner:v0 with runsc
```

Useful installer overrides:

```bash
NOX_COLIMA_CPUS=4 \
NOX_COLIMA_MEMORY=8 \
NOX_COLIMA_DISK=40 \
./install.sh --local
```

Use `./install.sh --help` for all options, including custom install prefixes, Colima profiles, runner image tags, and gVisor release paths. If you use a custom runner image, pass the same tag to `nox launch --image <tag>`.

The installer uses the mutable `latest` gVisor release path by default. Set `NOX_GVISOR_VERSION` or `--gvisor-version` for a reproducible release path. For a reproducible source install, set `NOX_SOURCE_REF` to a 40-character commit SHA or provide `NOX_SOURCE_ARCHIVE_URL`.

## Quick start

`nox launch` defaults to `--network online`; use `--network none` for offline runs.

### Deterministic local agent

Use the generic adapter to verify the full lifecycle without calling a hosted model:

```bash
nox launch \
  --repo . \
  --from main \
  --output-branch nox/generic-smoke \
  --agent generic \
  --agent-command 'printf "nox-ok\n" > nox-proof.txt' \
  --task 'create a proof file' \
  --validate 'test "$(cat nox-proof.txt)" = nox-ok' \
  --network none
```

### Codex

Nox includes a built-in Codex adapter. No separate skill or integration layer is required:

```bash
nox launch \
  --repo . \
  --from main \
  --output-branch nox/fix-auth \
  --agent codex \
  --task 'Fix the authentication tests' \
  --validate 'go test ./...' \
  --network online
```

Nox stages `~/.codex` into a per-run read-only source volume and prepares a disposable writable Codex home inside the container. Use `--codex-home` to select another host directory:

```bash
--codex-home /path/to/codex-home
```

Most hosted-model agents require `--network online`.

## Launch behavior

A launch proceeds synchronously:

1. Resolve `--from` to an exact commit SHA.
2. Clone the repository without local hardlinks into `~/.nox/runs/<run-id>/workspace`.
3. Copy the workspace into a VM-native Docker volume.
4. Create and start a Docker container using `runsc`.
5. Run the selected agent inside the container.
6. Run the required `--validate` command in the same container.
7. Export the validated workspace back to the host.
8. Reconstruct the result in a separate host-side clone.
9. Create one squashed commit on the requested local branch.
10. Remove the container, workspace volume, and successful workspace.

On success, Nox prints the result branch and commit:

```text
completed: created local branch nox/fix-auth at <sha>
run: <run-id>
next: git switch nox/fix-auth
```

If the agent makes no changes, Nox completes without creating a branch. Agent failure, validation failure, cancellation, timeout, or branch collision also creates no result branch.

## Sandbox guardrails

The agent and validation workload container uses:

- Docker's `runsc` runtime
- Non-root UID and GID `1000:1000`
- All Linux capabilities dropped
- `no-new-privileges`
- CPU, memory, and PID limits
- No host networking
- No published ports
- No Docker socket
- A VM-native, isolated workspace volume
- An optional Codex authentication volume staged from the host and mounted read-only as the source for a disposable in-container Codex home

Short-lived runsc helper containers are used only to transfer workspace and Codex files into or out of VM-native volumes. They have no network access and are not the agent execution boundary.

Network access is intentionally coarse in v0:

```bash
--network online  # Docker bridge with outbound access
--network none    # no container networking
```

### Credential warning

Online sandbox code can read staged Codex credentials and may attempt to exfiltrate them. The host Codex directory is copied into a per-run VM-native volume; the source volume is mounted read-only and the container uses a disposable writable copy. Codex's internal sandbox is bypassed because Nox relies on the outer `runsc` boundary; never run this adapter without the required Nox sandbox. Nox v0 does not yet provide credential brokering, domain-level egress filtering, or metadata-service filtering. Use disposable credentials and non-sensitive repositories when evaluating untrusted code.

## Inspect and clean up runs

Run state is stored under `~/.nox/runs/<run-id>` and may include metadata, logs, a patch, and a retained failed workspace. Active workspaces and staged Codex data are held in labeled Docker volumes and removed during teardown.

```bash
nox inspect <run-id>   # print run metadata
nox diff <run-id>      # print the published patch
nox cleanup <run-id>   # remove one run, its container, and volumes
nox cleanup --stale    # remove all Nox-managed containers and volumes
```

## Command overview

```text
nox doctor
nox launch --repo <path> --from <ref> --output-branch <branch> [options]
nox inspect <run-id>
nox diff <run-id>
nox cleanup <run-id>
nox cleanup --stale
```

Run command-specific help for available options:

```bash
nox launch --help
```

## Current limitations

Nox v0 intentionally does not:

- Provision remote hosts or provide hosted execution
- Push branches or open pull requests
- Check out or modify the source worktree
- Overwrite an existing local branch
- Broker agent credentials
- Apply domain-level outbound network policies
- Validate in a separate clean-room container
- Support sandbox runtimes other than `runsc`

## Development

Run the local validation suite:

```bash
gofmt -w $(find cmd internal -name '*.go')
go test ./...
go vet ./...
```

Run the real gVisor smoke test on a compatible Linux host:

```bash
NOX_RUNSC_INTEGRATION=1 \
NOX_RUNNER_IMAGE=nox-runner:v0 \
go test -tags=integration -v ./...
```

The integration tests are skipped unless `NOX_RUNSC_INTEGRATION=1` is set. They cover both the `runsc` image smoke test and a real generic launch using VM-native workspace storage. A skipped test is not evidence that the `runsc` runtime works.
