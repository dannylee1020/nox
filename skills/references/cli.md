# Nox CLI reference

Nox runs one coding task inside a local Docker/gVisor sandbox and publishes validated changes to a new local branch.

## Setup

```bash
nox doctor
```

`doctor` must pass before launching. Nox requires Docker with the `runsc` runtime and never falls back to `runc`.

## Launch

```bash
nox launch \
  --repo <repository-root> \
  --from <committed-ref> \
  --output-branch <new-local-branch> \
  --agent codex \
  --task-file <task-file> \
  --validate <command>
```

Useful options:

- `--network online|none`
- `--image <runner-image>`
- `--timeout <duration>`
- `--codex-home <path>`
- `--state-root <path>`

The launch command prints a run ID immediately and suggests `nox watch <run-id>`.

## Monitor and inspect

```bash
nox watch <run-id>
nox inspect <run-id>
nox diff <run-id>
nox runs
```

Run state and logs are stored under `~/.nox/runs/<run-id>` by default.

## Result contract

A successful launch:

- runs the agent and validation inside the same `runsc` container;
- creates one host-side squashed commit on the requested local branch;
- preserves the source checkout's branch, HEAD, and dirty changes;
- removes the container and managed volumes after teardown.

A failed or cancelled launch does not publish a branch. Nox retains the exported workspace and logs for inspection.

Nox does not push, open pull requests, switch the source checkout, or overwrite an existing local branch.
