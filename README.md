# Nox

Nox is a local sandbox for coding agents. It runs agent code in an isolated environment for safer execution, then publishes changes to a new local branch. Your original checkout is never mounted, modified, or switched.

```text
local Git ref
  → isolated clone
  → Docker + runsc sandbox
  → coding agent
  → validation
  → squashed local branch
  → teardown
```

## Install

Nox v0 installs everything needed for local Codex execution with one command:

- the Nox CLI under `~/.local/bin/nox`
- the Codex skill under `~/.agents/skills/nox`
- the local Docker/gVisor sandbox and `nox-runner:v0` image
- Colima and the Docker CLI on macOS when they are missing

The installer downloads the source and builds it locally, so `curl`, `tar`, Go, and Git are required. On macOS, install Homebrew first; Nox provisions Colima, Docker, and `runsc`. On Linux, Docker must already be installed with `runsc` registered.

Run:

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash
```

Add `~/.local/bin` to `PATH` if needed. Restart Codex after installation so it discovers the skill, then explicitly invoke it with `$nox`.

The skill delegates to the installed `nox` CLI; it does not add another sandbox or daemon. It only runs when explicitly invoked as `$nox`.

The installer always prepares and verifies the local backend. On macOS it installs Colima and the Docker CLI when needed, creates or reuses the `nox` Colima profile, installs `runsc`, and selects the `colima-nox` Docker context. On Linux it validates the existing Docker/runsc setup. Both paths build the runner image and finish with `nox doctor`.

On Linux, install Docker and follow the [gVisor installation guide](https://gvisor.dev/docs/user_guide/install/), then register and verify `runsc`:

```bash
sudo /usr/local/bin/runsc install
sudo systemctl restart docker
docker info --format '{{json .Runtimes}}'
```

A successful install ends with:

```text
ok: Docker can run nox-runner:v0 with runsc
```

You can also run `./install.sh` from a checkout. Common local VM overrides:

```bash
NOX_COLIMA_CPUS=4 \
NOX_COLIMA_MEMORY=8 \
NOX_COLIMA_DISK=40 \
./install.sh
```

Run `./install.sh --help` for custom prefixes, Colima profiles, image tags, and gVisor releases. A custom image must also be passed to `nox launch --image <tag>`.

The v0 installer defaults to source branch `main` and gVisor `latest`. For reproducible inputs, set `NOX_SOURCE_REF` to a 40-character commit SHA and set `NOX_GVISOR_VERSION` or `--gvisor-version`. You can also provide `NOX_SOURCE_ARCHIVE_URL`.

## Quick start

### Deterministic local agent

Test the full lifecycle without a hosted model:

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

The Codex adapter is built in and requires authentication under `~/.codex`.
Nox invokes Codex with `codex exec --dangerously-bypass-approvals-and-sandbox --ephemeral -`.
This is Codex's full-autonomy mode: Codex receives no approval prompts and no inner
Codex sandbox restrictions. The outer Nox `runsc` container remains the isolation
boundary. Host- or organization-managed Codex requirements may still restrict this
mode; Nox cannot override those policies.

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

Nox stages `~/.codex` into a read-only per-run volume and gives Codex a disposable writable home. Select another host directory with `--codex-home /path/to/codex-home`.

After installing the skill, use Codex directly:

```text
$nox Implement the requested change and validate it with go test ./...
```

`nox launch` defaults to `--network online`, which most hosted agents require. Use `--network none` for offline runs.

## How it works

Each launch:

1. Resolves `--from` to an exact committed SHA. Uncommitted source changes are excluded with a warning.
2. Clones that commit without local hardlinks into `~/.nox/runs/<run-id>/workspace`.
3. Copies the clone into a VM-native Docker volume and starts an isolated `runsc` container.
4. Runs the agent, then the required `--validate` command in the same container.
5. Exports the workspace, reconstructs it in a separate host clone, and creates one host-generated squashed commit on the requested local branch.
6. Removes the container and volumes. Successful workspaces are removed; failed workspaces and logs are retained for inspection.

Nox never checks out, pushes, or opens a pull request. No branch is created if there are no changes or if the agent, validation, export, cancellation, timeout, or branch-collision path fails.

At launch start, Nox prints the run ID and a monitoring command:

```text
run: <run-id>
watch: nox watch <run-id>
```

On success:

```text
completed: created local branch nox/fix-auth at <sha>
next: git switch nox/fix-auth
```

## Sandbox

The agent and validation container uses:

- Docker's `runsc` runtime, with no fallback to `runc`
- Root UID/GID `0:0` inside the disposable gVisor sandbox
- Docker's default bounded capability set and `no-new-privileges`
- An executable disposable `/tmp` for package managers and test runners
- CPU, memory, and PID limits
- No host networking, published ports, or Docker socket
- An isolated VM-native workspace volume
- An optional read-only Codex source volume and disposable writable Codex home

Short-lived `runsc` helper containers transfer files to and from VM-native volumes. They have no network access and do not run the agent.

Network policy is coarse in v0:

```bash
--network online  # Docker bridge with outbound access
--network none    # no container networking
```

### Credential warning

Online sandbox code can read staged Codex credentials and may exfiltrate them. Nox copies the host Codex directory into a read-only source volume, then runs Codex from a disposable writable copy. Codex's internal sandbox is bypassed because `runsc` is the outer boundary; never use this adapter without the Nox sandbox.

Nox v0 does not broker credentials or filter domains and metadata services. Use disposable credentials and non-sensitive repositories for untrusted code.

## Runs and commands

Run state lives under `~/.nox/runs/<run-id>` and may contain metadata, `agent.log`, `validation.log`, a patch, and a retained failed workspace. Active workspace and Codex data live in labeled Docker volumes removed during teardown. Use `nox watch <run-id>` to follow state transitions and logs from another terminal. E2E evidence is stored separately under `~/.nox/evidence/v0/`.

Published result commits use the effective `user.name` and `user.email` from the source repository. Nox fails before sandbox startup if either identity is not configured.

```text
nox doctor
nox launch --repo <path> --from <ref> --output-branch <branch> [options]
nox inspect <run-id>       # print metadata
nox watch <run-id>         # follow lifecycle and logs
nox diff <run-id>          # print the published patch
nox cleanup <run-id>       # remove the run, container, and volumes
nox cleanup --stale        # remove all Nox-managed containers and volumes
```

Run `nox launch --help` for all launch options.

## Current limitations

Nox v0 does not:

- Provision remote hosts or provide hosted execution
- Push branches or open pull requests
- Check out or modify the source worktree
- Overwrite an existing local branch
- Broker agent credentials
- Apply domain-level outbound network policies
- Validate in a separate clean-room container
- Support sandbox runtimes other than `runsc`

## Development

```bash
gofmt -w $(find cmd internal -name '*.go')
go test ./...
go vet ./...
```

Run the real gVisor tests on a compatible host:

```bash
NOX_RUNSC_INTEGRATION=1 \
NOX_RUNNER_IMAGE=nox-runner:v0 \
go test -tags=integration -v ./...
```

These tests are skipped unless `NOX_RUNSC_INTEGRATION=1` is set. They cover the `runsc` image smoke test and a real generic launch using VM-native workspace storage. A skipped test does not prove that `runsc` works.
