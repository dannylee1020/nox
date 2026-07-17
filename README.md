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
> Nox is currently a local v0 intended for testing on a Linux Docker host. It does not yet provision a Linux VM for macOS or provide hosted execution.

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

- Linux Docker daemon with the `runsc` runtime registered
- Git and Docker CLI
- Go compatible with the version declared in `go.mod`
- Locally built `nox-runner:v0` image
- Codex authentication under `~/.codex` when using the Codex adapter

On macOS, use a Linux-backed Docker context such as a dedicated Colima profile with `runsc` registered. Nox transfers the workspace and Codex home into VM-native Docker volumes, so the Docker daemon does not need to write directly to the macOS filesystem.

## Install

Build a project-local binary:

```bash
mkdir -p bin
go build -o bin/nox ./cmd/nox
```

Build the runner image:

```bash
docker build -t nox-runner:v0 images/runner
```

Confirm that Docker can start the image with gVisor:

```bash
./bin/nox doctor
```

Expected output:

```text
ok: Docker can run nox-runner:v0 with runsc
```

## Quick start

### Deterministic local agent

Use the generic adapter to verify the full lifecycle without calling a hosted model:

```bash
./bin/nox launch \
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
./bin/nox launch \
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

Every workload uses:

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
./bin/nox inspect <run-id>   # print run metadata
./bin/nox diff <run-id>      # print the published patch
./bin/nox cleanup <run-id>   # remove one run, its container, and volumes
./bin/nox cleanup --stale    # remove all Nox-managed containers and volumes
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
./bin/nox launch --help
```

## Current limitations

Nox v0 intentionally does not:

- Provision a Linux VM for macOS
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
