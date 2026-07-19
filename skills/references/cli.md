# Nox CLI reference

Nox runs one coding task inside a local or remote Docker/gVisor sandbox and publishes validated changes to a new local branch or pull request.

## Setup

For local execution, a worker must run:

```bash
nox doctor
```

`doctor` must pass before local launching. Remote execution does not require local Docker or `nox doctor`. Nox requires Docker with the `runsc` runtime and never falls back to `runc`.

## Launch

```bash
nox launch \
  --repo <repository-root> \
  --from <committed-ref> \
  --output-branch <new-local-branch> \
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

## Remote submit

When `NOX_REMOTE_URL` is configured, submit through the remote worker instead of running local Docker:

```bash
nox submit \
  --repo <repository-root> \
  --from <github-base-branch> \
  --title "<pull-request-title>" \
  --task-file <task-file> \
  --validate <command>
```

`nox submit` reads `NOX_API_TOKEN`, resolves the selected branch from GitHub `origin`, submits the pinned commit, and polls until the worker reports a pull request, no changes, failure, or cancellation. Local uncommitted work is ignored. It does not run `nox doctor` locally.

Use `--detach --json` when a parent agent will monitor the run separately:

```bash
nox submit --detach --json ...
nox watch --remote <run-id>
```

## Task input contract

Nox reads `--task` or `--task-file` on the host. The sandbox agent does not receive the parent conversation automatically.

When launching from a conversational agent, hydrate the structured contract in `task-contract.md` with the user's invocation and relevant context from that thread. Its stable sections capture the objective, hard and soft constraints, plan, affected surfaces, acceptance criteria, validation, and stop conditions. Sections may contain arbitrary Markdown; use `None specified` when no relevant content exists. `Context and extra` preserves useful information that does not fit elsewhere.

For Codex, Nox prepends a deterministic execution envelope containing the resolved base commit and required validation command, then preserves the hydrated contract unchanged as the final prompt payload. Nox does not parse, summarize, or semantically normalize the contract. Codex is the only production agent in v0. The sandbox workspace starts from the committed `--from` ref; uncommitted changes and unrecorded context from the source checkout are not included.

## Monitor and inspect

```bash
nox watch <run-id>                 # local run
nox watch --remote <run-id>       # remote run
nox cancel --remote <run-id>      # explicitly cancel remote work
nox inspect <run-id>
nox diff <run-id>
```

Stopping `nox watch --remote` does not cancel the server-owned run. Run state and logs for local runs are stored under `~/.nox/runs/<run-id>` by default; remote details remain on the worker.

## Result contract

A successful launch:

- runs the agent and validation inside the same `runsc` container;
- creates one host-side squashed commit on the requested local branch;
- preserves the source checkout's branch, HEAD, and dirty changes;
- removes the container and managed volumes after teardown.

A failed or cancelled launch does not publish a branch. Nox retains the exported workspace and logs for inspection.

Nox does not push the caller's source branch, switch the source checkout, overwrite an existing local branch, or merge remote pull requests. Remote execution pushes only the worker-generated `nox/<run-id>` branch on the server.
