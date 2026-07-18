# Nox

Nox is a sandbox environment for codex. It runs agent code in an isolated environment for safer execution, then publishes validated results without modifying the caller's source checkout.


## Install

Nox has one binary but two explicit installation profiles. Choose the profile that matches where the execution edge runs; the installer never silently guesses.

### Local profile

`local` installs the synchronous CLI workflow:

- `~/.local/bin/nox`
- the Codex skill under `~/.agents/skills/nox`
- the local Docker/gVisor sandbox and `nox-runner:v0` image
- Colima and the Docker CLI on macOS when they are missing

The installer downloads the source and builds it locally, so `curl`, `tar`, Go, and Git are required. On macOS, install Homebrew first; Nox provisions Colima, Docker, and `runsc`. On Linux, Docker must already be installed with `runsc` registered.

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- local
# or from a checkout:
./install.sh local
# or:
task install:local
```

Add `~/.local/bin` to `PATH` if needed. Restart Codex after installation so it discovers the skill, then explicitly invoke it with `$nox`.

The skill delegates to the installed `nox` CLI; it does not add another sandbox or daemon. It only runs when explicitly invoked as `$nox`.

The local profile prepares and verifies the backend, builds the runner image, installs the skill, and finishes with `nox doctor`. Common local VM overrides:

```bash
NOX_COLIMA_CPUS=4 \
NOX_COLIMA_MEMORY=8 \
NOX_COLIMA_DISK=40 \
./install.sh local
```

### Remote profile

`remote` installs the same `nox` binary as a Linux systemd worker. It verifies Docker/runsc, builds the runner image, installs `deploy/nox.service`, creates `/etc/nox` and `/var/lib/nox`, and writes a configuration template without enabling or starting the service. It does not install Colima or the Codex skill.

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- remote
# or from a checkout:
sudo ./install.sh remote
# or:
task install:remote
```

The remote profile requires root or `sudo` for system paths and user/service setup. Configure tokens and service-user Codex authentication before starting the unit. See [self-hosted remote execution](docs/remote.md) for the complete setup.

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

Run `./install.sh local --help` or `./install.sh remote --help` for profile-specific options. A custom image must also be passed to `nox launch --image <tag>`.

The v0 installer defaults to source branch `main` and gVisor `latest`. For reproducible inputs, set `NOX_SOURCE_REF` to a 40-character commit SHA and set `NOX_GVISOR_VERSION` or `--gvisor-version` for the local profile. You can also provide `NOX_SOURCE_ARCHIVE_URL`.

## Quick start

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
  --task 'Fix the authentication tests' \
  --validate 'go test ./...' \
  --network online
```

Nox stages `~/.codex` into a read-only per-run volume and gives Codex a disposable writable home. Select another host directory with `--codex-home /path/to/codex-home`.

After installing the skill, use the skill directly:

```text
$nox Implement the requested change.
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

No branch is created if there are no changes or if the agent, validation, export, cancellation, timeout, or branch-collision path fails.

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
nox submit --repo <path> --from <branch> --title <title> --task-file <file> --validate <command>
nox inspect <run-id>       # print metadata
nox watch <run-id>         # follow lifecycle and logs
nox diff <run-id>          # print the published patch
nox cleanup <run-id>       # remove the run, container, and volumes
nox cleanup --stale        # remove all Nox-managed containers and volumes
```

Run `nox launch --help` or `nox submit --help` for execution options.

## Remote self-hosting

Nox can run as a private, single-node remote worker on Linux. Start `nox serve` on a VM with Docker/gVisor and submit authenticated GitHub run requests. Successful validated runs push a `nox/<run-id>` branch and create a pull request; remote users receive concise status and the PR URL rather than raw logs or patches.

Remote API requests require `Authorization: Bearer <token>`, matching the server's `NOX_API_TOKEN`.

For normal Codex use, configure `NOX_REMOTE_URL` and `NOX_API_TOKEN` on the client. The `$nox` skill then uses `nox submit` to verify the current GitHub branch, dispatch the contract, poll the remote worker, and report the resulting pull request. Manual API calls are intended for diagnostics and integrations.

See [docs/remote.md](docs/remote.md) for deployment, configuration, API examples, and operator diagnostics.

## Current limitations

Nox v0 remote mode does not:

- Support providers other than GitHub
- Provide durable queues, restart recovery, or multiple workers
- Expose remote logs or patch download endpoints
- Broker per-request agent credentials
- Automatically merge pull requests

Both local and remote modes still:

- Never check out or modify the caller's source worktree
- Require explicit validation before publication
- Support only the `runsc` sandbox runtime
- Do not validate in a separate clean-room container

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

These tests are skipped unless `NOX_RUNSC_INTEGRATION=1` is set. They cover the `runsc` image smoke test and a real Nox launch using VM-native workspace storage. A skipped test does not prove that `runsc` works.
