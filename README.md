# Nox

Nox runs a coding agent against a local Git branch inside a Docker container using gVisor.

## v0 scope

```text
local branch → isolated clone → run agent → run validation → local branch → teardown
```

Nox does not push, open pull requests, check out branches, or modify the original worktree.

## Requirements

- Linux Docker daemon with the `runsc` runtime registered.
- Git and Docker CLI.
- A locally built `nox-runner:v0` image.
- Codex authentication under `~/.codex` for the default Codex adapter.

The current v0 does not provision a Linux VM for macOS. Use a Docker context backed by a Linux daemon.

## Build

```bash
docker build -t nox-runner:v0 images/runner

go build -o nox ./cmd/nox
./nox doctor
```

`doctor` fails closed unless Docker can start the runner image with `runsc`.

## Launch

```bash
./nox launch \
  --repo . \
  --from main \
  --output-branch nox/fix-auth \
  --agent codex \
  --task "Fix the authentication tests" \
  --validate "npm test"
```

For a deterministic local test agent:

```bash
./nox launch \
  --repo . \
  --from main \
  --output-branch nox/test \
  --agent generic \
  --agent-command 'printf change > generated.txt' \
  --task 'make the requested change' \
  --validate 'test -f generated.txt'
```

The source branch is resolved to an exact committed SHA. Uncommitted changes in the source worktree are excluded with a warning. The source checkout is never mounted into the container; Nox clones it into `~/.nox/runs/<run-id>/workspace` without hardlinks.

On successful validation, Nox creates one host-generated squashed commit on the requested local branch and leaves the current checkout unchanged:

```text
completed: created local branch nox/fix-auth at <sha>
next: git switch nox/fix-auth
```

Validation failure, agent failure, cancellation, timeout, ref collision, or no changes creates no result branch. Failed workspaces and logs are retained for inspection.

## Guardrails

Each workload uses Docker `runsc`, a non-root user, dropped capabilities, `no-new-privileges`, CPU/memory/PID limits, no host networking, no published ports, no Docker socket, and only the isolated workspace plus explicitly approved Codex auth mount.

Networking is coarse by design:

```bash
--network online   # Docker bridge; unrestricted outbound access
--network none     # offline execution
```

Online execution is required for most hosted-model agents and may allow sandbox code to read injected credentials and exfiltrate data. Nox does not yet provide credential brokering or domain-level egress filtering.

## Inspect and clean up

```bash
./nox inspect <run-id>
./nox diff <run-id>
./nox cleanup <run-id>
./nox cleanup --stale
```

## Development validation

```bash
gofmt -w $(find cmd internal -name '*.go')
go test ./...
go vet ./...
```

Real gVisor integration requires a Linux Docker daemon with `runsc`; it cannot be certified from a host without that daemon/runtime.
