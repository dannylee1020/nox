# Nox CLI reference

Nox runs one implementation or agent-driven end-to-end test inside a local or remote Docker/gVisor sandbox. Implementation runs publish validated changes; test runs retain validation evidence without publication.

## Setup

For local execution, a worker must run:

```bash
nox doctor
```

`doctor` must pass before local launching. Remote execution does not require local Docker or `nox doctor`. Nox requires Docker with the `runsc` runtime and never falls back to `runc`.

## Launch

```bash
nox launch \
  --mode feat \
  --repo <repository-root> \
  --from <committed-ref> \
  --output-branch <new-local-branch> \
  --task-file <task-file> \
  --validate <command>
```

For test mode, omit `--output-branch`:

```bash
nox launch \
  --mode test \
  --repo <repository-root> \
  --from <committed-ref> \
  --task-file <test-task-file> \
  --validate <command>
```

Useful options:

- `--mode feat|test` (defaults to `feat`)
- `--network online|none`
- `--image <runner-image>`
- `--timeout <duration>`
- `--codex-home <path>`
- `--state-root <path>`

The launch command prints a run ID immediately and points to `nox ui` for live monitoring and `nox inspect <run-id>` for a metadata snapshot.

## Remote submit

When `NOX_REMOTE_URL` is configured, submit through the remote worker instead of running local Docker:

```bash
nox submit \
  --mode feat \
  --repo <repository-root> \
  --from <github-base-branch> \
  --title "<pull-request-title>" \
  --task-file <task-file> \
  --validate <command>
```

`nox submit` reads `NOX_API_TOKEN`, resolves the selected branch from GitHub `origin`, submits the pinned commit, and polls until the worker reports a pull request, evidence-only test completion, no changes, failure, or cancellation. `--title` is required only for feat mode. Local uncommitted work is ignored. It does not run `nox doctor` locally.

Use `--detach --json` when a parent agent will monitor the run separately:

```bash
nox submit --detach --json ...
nox inspect --remote <run-id>
```

## Task input contract

Nox reads `--task` or `--task-file` on the host. The sandbox agent does not receive the parent conversation automatically.

When launching from a conversational agent, hydrate the structured contract in `task-contract.md` with the user's invocation and relevant context from that thread. Its stable sections capture the objective, hard and soft constraints, plan, affected surfaces, acceptance criteria, validation, and stop conditions. Sections may contain arbitrary Markdown; use `None specified` when no relevant content exists. `Context and extra` preserves useful information that does not fit elsewhere.

For Codex, Nox prepends a deterministic execution envelope containing the resolved base commit and required validation command, then preserves the hydrated contract unchanged as the final prompt payload. Test mode adds a tester-only responsibility: the agent may construct runtime state and imitate the user flow but must not implement or modify tracked source. Nox independently runs validation and checks tracked source integrity. Nox does not parse, summarize, or semantically normalize the contract. Codex is the only production agent in v0. The sandbox workspace starts from the committed `--from` ref; uncommitted changes and unrecorded context from the source checkout are not included.

## Monitor and inspect

```bash
nox ui                             # live local or host-side console
nox cancel --remote <run-id>      # explicitly cancel remote work
nox inspect <run-id>               # one local metadata snapshot
nox inspect --remote <run-id>      # one remote status snapshot
nox diff <run-id>
```

`nox ui` serves stored run evidence on `127.0.0.1:8081`. On a remote worker it runs as a separate loopback-only service and is reached through an SSH tunnel. The console never changes run state. `nox inspect` is non-blocking and emits one JSON snapshot for agent-side checks; repeated remote inspection does not cancel the server-owned run.

## Skill status command

`$nox status` is a conversational skill command, not a Nox CLI subcommand:

```text
$nox status            # every active task in the current thread
$nox status <run-id>   # one tracked task
```

The skill asks each running Nox worker for a non-disruptive point-in-time report and returns fixed fields for the worker, run state, current activity, monitor, result, validation, blocker, and evidence. If no task remains active, the unqualified command reports the most recently dispatched task. It never launches, interrupts, cancels, or cleans up work. Local and remote status are corroborated with the corresponding `nox inspect` command.

## Result contract

A successful launch:

- runs the repository's optional `.nox/setup.sh` before staging Codex credentials;
- runs the agent and validation inside the same `runsc` container;
- creates one host-side squashed commit on the requested local branch;
- preserves the source checkout's branch, HEAD, and dirty changes;
- removes the container and managed volumes after teardown.

A failed or cancelled implementation launch does not publish a branch and retains the exported workspace and logs for inspection. Test runs never publish, emit patches, or retain/export workspaces on success or failure; they retain metadata and setup, agent, and validation logs only.

Nox does not push the caller's source branch, switch the source checkout, overwrite an existing local branch, or merge remote pull requests. Remote implementation execution pushes only the worker-generated `nox/<run-id>` branch on the server; test execution cannot push.
