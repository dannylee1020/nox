# Nox

Nox is a sandbox for delegated coding work. It gives coding agents an isolated environment, requires explicit validation, and publishes only validated results without changing the caller's checkout.

```text
task + committed source
        ↓
isolated gVisor sandbox
        ↓
setup → agent → validation
        ↓
trusted publication
```

## Why Nox

Coding agents often need broad permissions to install tools, edit files, and run tests. Nox gives them that freedom inside a disposable `runsc` sandbox while keeping the developer's working tree and Docker host outside the execution boundary.

Nox is designed to be invoked by an agent through the `$nox` skill. Humans define the task, constraints, and validation command; Nox owns isolation, lifecycle, evidence, and publication.

## Core components

- **Agent interface** — the `$nox` skill turns conversation context into a self-contained task and delegates it without blocking the main session.
- **Orchestrator** — resolves an exact source commit, creates the run, moves it through setup, execution, validation, publication, and teardown.
- **Sandbox** — runs repository setup, the coding agent, and validation inside an isolated Docker container using gVisor `runsc`.
- **Validator** — executes the required validation command independently of the agent's own claims.
- **Trusted publisher** — reconstructs the result outside the sandbox and publishes a host-generated commit.
- **Run evidence** — retains metadata, setup output, agent output, validation output, and the resulting patch for inspection.

## Execution modes

| Mode | Source | Execution edge | Validated result |
| --- | --- | --- | --- |
| Local | Committed local ref | Linux host or persistent Colima VM | New local branch |
| Remote | Pinned GitHub ref | Private Linux Nox worker | GitHub branch and pull request |

Both modes use the same sandbox and validation lifecycle. Local execution is the simplest path for individual development; remote execution lets agents submit work asynchronously to a self-hosted worker.

## Core guarantees

- Runs start from an exact committed source snapshot. Dirty and untracked caller changes are excluded.
- gVisor `runsc` is mandatory. Nox fails closed instead of falling back to `runc`.
- Repository-owned setup runs before the coding agent in the same environment later used for validation.
- Validation is an explicit publication gate. Failed or cancelled work is never published.
- Sandbox-controlled Git metadata is not trusted for publication.
- The caller's checkout is never switched, reset, mounted into the sandbox, or modified.
- Each run has isolated workspace and credential volumes that are removed during teardown.
- Logs and patches remain available as evidence after the sandbox exits.

## Install

Install the local profile for agent-driven work on the current machine:

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- local
```

The local profile installs the Nox CLI and Codex skill, prepares Docker/gVisor, and configures a persistent Colima VM on macOS. Restart Codex after installation so it discovers the skill.

Install the remote profile on a private Linux worker:

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- remote
```

Remote installation and operation require service credentials and GitHub access. See [self-hosted remote execution](docs/remote.md) for configuration and deployment.

## Use

Delegate from a Codex session:

```text
$nox Implement the requested change and validate it.
```

Nox hydrates the task with relevant constraints and acceptance criteria, launches the sandbox through a background worker, and returns control to the main conversation.

The underlying local CLI is also available directly:

```bash
nox launch \
  --repo . \
  --from main \
  --output-branch nox/example \
  --task "Implement the requested change" \
  --validate "go test ./..."
```

Useful inspection commands:

```text
$nox status            conversational status for delegated tasks
nox watch <run-id>     follow lifecycle and logs
nox inspect <run-id>   inspect run metadata
nox diff <run-id>      inspect the published patch
```

Nox does not automatically switch to, merge, or push a local result branch.

## Security boundary

The coding agent has broad authority inside its disposable sandbox. It does not receive the host Docker socket or direct access to the caller's checkout.

Repository setup runs before Codex credentials are copied into the sandbox, but code executed later in an online run can read those credentials. Nox does not yet broker short-lived credentials or provide fine-grained egress filtering. Use trusted setup scripts and appropriately scoped credentials.

## Current boundaries

- Codex is the production coding-agent adapter.
- gVisor `runsc` is the supported sandbox runtime.
- Remote source and publication use GitHub.
- Remote execution is self-hosted and single-node; durable queue and restart recovery are not included.
- Validation runs in the same sandbox as the agent rather than a separate clean-room environment.

## Development

```bash
go test -race ./...
go vet ./...
```

Detailed remote configuration, API examples, and operator diagnostics live in [docs/remote.md](docs/remote.md).
