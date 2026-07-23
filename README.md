# Nox

Nox is a sandbox environment for AI agents. It provides safe, isolated environment for agents to execute and test code before pushing to production.

```text
task + committed source
        ↓
isolated gVisor sandbox
        ↓
setup → agent → validation
        ↓
implementation publication OR test evidence
```

## Why Nox

Coding agents often need broad permissions to install tools, edit files, and run tests. Nox gives them that freedom inside a disposable `runsc` sandbox while keeping the developer's working tree and Docker host outside the execution boundary.

Nox is designed to be invoked by an agent through the `$nox` skill. Humans define the task, constraints, and validation command; Nox owns isolation, lifecycle, evidence, and publication.

## Core components

- **Agent interface** — the `$nox` skill turns conversation context into a self-contained task and delegates it without blocking the main session.
- **Orchestrator** — resolves an exact source commit, creates the run, moves it through setup, agent execution, validation, outcome, and teardown.
- **Sandbox** — runs repository setup, the agent, and validation inside an isolated Docker container using gVisor `runsc`.
- **Validator** — executes the required validation command independently of the agent's own claims.
- **Trusted publisher** — reconstructs implementation results outside the sandbox and publishes a host-generated commit.
- **Run evidence** — retains metadata, setup output, agent output, validation output, and implementation patches; test runs retain logs and metadata only.

## Execution modes

| Execution | Source | Execution edge | Validated result |
| --- | --- | --- | --- |
| Local implementation | Committed local ref | Linux host or persistent Colima VM | New local branch |
| Remote implementation | Pinned GitHub ref | Private Linux Nox worker | GitHub branch and pull request |
| Local test | Committed local ref | Linux host or persistent Colima VM | Evidence-only completion; no code publication |
| Remote test | Pinned GitHub ref | Private Linux Nox worker | Evidence-only completion; no code publication |

Both modes use the same sandbox and validation lifecycle. Local execution is the simplest path for individual development; remote execution lets agents submit work asynchronously to a self-hosted worker.

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
  --mode feat \
  --repo . \
  --from main \
  --output-branch nox/example \
  --task "Implement the requested change" \
  --validate "go test ./..."
```

For agent-driven end-to-end testing, omit publication inputs:

```bash
nox launch \
  --mode test \
  --repo . \
  --from main \
  --task-file test-contract.md \
  --validate "npm run e2e"
```

Test mode lets the tester construct dependencies and imitate the supplied real-world workflow. It retains setup, agent, validation, and metadata evidence, but no workspace, patch, branch, or pull request.

## View tasks

Run `nox ui` to open a read-only dashboard for active and recent tasks:

```bash
nox ui
```

Open `http://127.0.0.1:8081` to view lifecycle progress, task metadata, validation, publication results, and logs. On a remote worker, reach the same dashboard through an SSH tunnel. See [run monitoring](docs/ui.md).

Useful inspection commands:

```text
$nox status            conversational status for delegated tasks
nox ui                 open the read-only task dashboard
nox inspect <run-id>   inspect run metadata
nox inspect --remote <run-id>
                       inspect one remote status snapshot
nox diff <run-id>      inspect the published patch
```

Nox does not automatically switch to, merge, or push a local result branch. Test mode cannot create one.

## Security boundary

The coding agent has broad authority inside its disposable sandbox. It does not receive the host Docker socket or direct access to the caller's checkout.

Repository setup runs before Codex credentials are copied into the sandbox, but code executed later in an online run can read those credentials. Nox does not yet broker short-lived credentials or provide fine-grained egress filtering. Use trusted setup scripts and appropriately scoped credentials.

## Current boundaries

- Codex is the production coding-agent adapter.
- gVisor `runsc` is the supported sandbox runtime.
- Remote source and publication use GitHub.
- Remote execution is self-hosted and single-node; durable queue and restart recovery are not included.
- Validation runs in the same sandbox as the agent rather than a separate clean-room environment.
- Test mode preserves logs and metadata only; bounded screenshots, reports, and downloadable artifacts are not supported yet.

## Development

```bash
go test -race ./...
go vet ./...
```

Detailed remote configuration, API examples, and operator diagnostics live in [docs/remote.md](docs/remote.md).
