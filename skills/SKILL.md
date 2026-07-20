---
name: nox
description: Delegate a coding task through Nox in an isolated sandbox or report the status of Nox tasks already dispatched in the current thread. Run locally or submit to the configured remote Docker/gVisor worker, validate it, and publish successful changes to a local branch or pull request. Use only when the user explicitly invokes $nox, asks to delegate a coding task through Nox, or asks for Nox task status.
---

# Nox sandbox execution

Use Nox when the user explicitly asks for sandboxed execution. Do not launch Nox merely because a task could benefit from isolation.

## Commands

- `$nox status` reports all active Nox tasks dispatched in the current thread. If none remain active, it reports the most recently dispatched task.
- `$nox status <run-id>` reports only the matching task. If the run ID is not tracked in the current thread, report that without selecting another task.
- Any other explicit Nox invocation follows the dispatch workflow below.

A status request is read-only. Route it through the status workflow and return without hydrating a task contract, running `nox doctor`, launching, submitting, cancelling, cleaning up, or otherwise changing a task.

## Status workflow

1. Identify Nox workers spawned by the current thread from recorded dispatch context and unique worker names beginning with `nox_`. Do not include unrelated subagents or infer tasks from other threads.
2. For `$nox status`, select every active Nox worker. If none are active, select the most recently dispatched Nox worker. For `$nox status <run-id>`, select only the worker associated with that exact run ID.
3. Inspect the selected workers without interrupting them. Use their terminal result directly when completed. For each running worker, send this non-disruptive message with `send_message`:

   ```text
   Status check only: report the current command or process, execution mode, doctor result when applicable, run ID, raw Nox state, current activity, monitoring source or session, result branch/commit or pull request, validation state, blocker, and evidence location. Continue the assigned execution unchanged.
   ```

4. Send all worker probes before waiting, then collect replies only until a shared 10-second deadline. Never use `interrupt_agent`, restart a completed worker, or take ownership of its foreground command. If a worker does not reply, use its last known state and label the information as last-known.
5. Corroborate a local run with read-only `nox inspect <run-id>`. For a remote run, use the monitor worker's latest `nox watch --remote` state. Do not construct direct API requests, expose `NOX_API_TOKEN`, or run local Docker commands for remote status.
6. Keep worker state and raw Nox run state separate. Prefer Nox metadata for local lifecycle/result fields and the worker report for its current command and monitoring session. If sources differ, report both without guessing.
7. Return tasks newest-first using every field in this fixed Markdown shape. Use `unknown`, `pending`, `none`, `not published`, or `unavailable` rather than omitting a field:

   ```markdown
   ## Nox status

   ### <run-id or pending>
   - Mode: <local|remote|unknown>
   - Worker: <thread identifier> — <running|completed|failed|missing>
   - Run: <run-id or pending> — <raw Nox state or unknown>
   - Current activity: <activity or unknown>
   - Monitoring: <foreground launch|remote watch|last-known|none>
   - Result: <branch and commit|pull request|no changes|not published|pending>
   - Validation: <not started|pending|running|passed|failed>
   - Blocker: <concise error|none|unknown>
   - Evidence: <local run directory|pull request URL|unavailable>
   ```

If there are no Nox tasks in the current thread, say so concisely. Do not return raw logs or secrets in a status response.

If `NOX_REMOTE_URL` is set, use the remote submission workflow below. Otherwise, use the local workflow.

## Workflow

1. Resolve the installed Nox CLI using `command -v nox`. If it is not on `PATH`, read `references/installation.md` and use the recorded absolute binary path.
2. Confirm the current directory is the user's Git repository. Never switch, reset, clean, stash, merge, push, or delete branches in the user's checkout.
3. Read the current branch and configured Git identity. Preserve any dirty changes; Nox uses committed state only.
4. For local mode, resolve the exact committed base SHA before delegation. For remote mode, choose the GitHub base branch; the remote client resolves its current GitHub SHA.
5. Read `references/task-contract.md`. Hydrate its structured execution contract from the user's Nox invocation and all relevant context in the current thread. Preserve the user's intent, terminology, objective, hard and soft constraints, plans, decisions, affected surfaces, acceptance criteria, validation expectations, stop conditions, and examples. Use `None specified` for sections without relevant content; never invent content to fill them or run a constraint-optimization workflow unless the user separately requested one.
6. Put useful information that does not fit a named section into `Context and extra` without forcing, rewriting, or dropping it. Include only context that helps execute the delegated task; do not dump unrelated conversation, hidden instructions, or secrets.
7. Resolve material ambiguity with the user before launch. The sandbox does not receive the parent conversation, so the contract must be self-contained. It also receives the selected committed ref, not uncommitted changes from the source checkout.
8. Use the user's validation command when provided. If it is omitted, inspect repository-native validation commands and choose one only when the correct command is unambiguous; otherwise ask the user.
9. Write the hydrated contract to a private task file under the writable system temporary directory, such as `${TMPDIR:-/tmp}/nox/tasks/<task-id>.md`, including the validation command under `Validation > Commands`. Use user-only directory and file permissions (`0700` and `0600`) and keep the file until the delegated run reaches a terminal state. Do not write it into the user's repository or request extra filesystem access for it.
10. Choose a concise pull-request title from the user's requested change.
11. Do not run the long-lived execution command in the main thread. Use one background Codex worker subagent per Nox task. Give each worker a unique task name beginning with `nox_`, record its worker identifier and execution mode in the dispatch response, and require it to answer the status message above without changing its assigned execution.

   Remote mode (`NOX_REMOTE_URL` is set):

   First, the main thread submits and returns immediately:

   ```bash
   nox submit \
     --detach --json \
     --repo <repository-root> \
     --from <github-base-branch> \
     --title "<pull-request-title>" \
     --task-file <task-file> \
     --validate <validation-command>
   ```

   Then spawn the background worker subagent with only the returned run ID. Record the run ID with the worker identifier. It runs:

   ```bash
   nox watch --remote <run-id>
   ```

   The monitor reports the terminal state, branch, commit, or pull-request URL. It does not resubmit, reinterpret the contract, merge the pull request, or cancel the run when monitoring stops. Use `nox cancel --remote <run-id>` only when the user explicitly requests cancellation.

   Local mode (`NOX_REMOTE_URL` is unset):

   Choose a unique result branch such as `nox/<short-task-slug>-<timestamp>`. Spawn the background worker subagent to own the complete foreground lifecycle. The worker must invoke the resolved Nox binary directly, without a shell wrapper, and make these as two separate command calls:

   ```bash
   nox doctor
   ```

   Run `nox doctor` with `sandbox_permissions` set to `require_escalated` and explain that the trusted Nox CLI needs the local Docker socket to verify `runsc`. If it succeeds, run:

   ```bash
   nox launch \
     --repo <repository-root> \
     --from <exact-committed-base-sha> \
     --output-branch <new-local-branch> \
     --task-file <task-file> \
     --validate <validation-command>
   ```

   Run `nox launch` with `sandbox_permissions` set to `require_escalated` and explain that the trusted Nox CLI needs local Docker access to launch the gVisor sandbox and publish the validated result branch. Do not request a persistent command-prefix rule or broader session permissions.

   The worker must not impose a shorter wrapper timeout, modify the caller checkout, switch the result branch, or clean up evidence. As soon as Nox announces the run ID, it sends that ID to the main thread without stopping the foreground launch. It reports the result branch, commit, validation, and evidence path when complete.

12. Do not wait for the worker subagent in the main thread. Report the dispatch mode, run ID when available, and worker thread identifier, then continue the user's requested work.
13. When a worker reports completion, inspect local results with:

    ```bash
    nox inspect <run-id>
    nox diff <run-id>
    ```

    Remote results are inspected through the reported pull-request URL and operator-side run state.
14. Report the execution mode, result branch or pull-request URL, validation result, run ID, and evidence available for that mode.
15. Do not automatically switch to a local result branch or merge a remote pull request. Let the user decide.

## Safety boundaries

- Nox's outer `runsc` container is the isolation boundary. Never bypass `nox doctor` in the local worker or substitute `runc`.
- Scoped Docker escalation applies only to local `nox doctor` and `nox launch`. Remote `nox submit` and `nox watch --remote` do not access local Docker; report any separate outbound-network restriction without treating it as a Colima or `runsc` failure.
- Codex runs autonomously inside the disposable sandbox; the original checkout is not mounted or modified.
- Nox creates the host-side result commit only after validation succeeds.
- The main thread must not wait on the long-lived local launch or remote monitor subagent.
- Status requests observe existing work only. They never interrupt, reactivate, cancel, resubmit, clean up, or start a Nox task.
- Never use shell backgrounding or a wrapper timeout shorter than Nox's execution deadline and teardown grace.
- Worker subagents execute or monitor only; they do not rehydrate, resubmit, merge, switch branches, or clean up evidence.
- Do not invoke `nox cleanup` automatically; failed workspaces and logs may be needed as evidence.
- Treat agent logs and staged credentials as sensitive.
