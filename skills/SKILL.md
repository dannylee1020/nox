---
name: nox
description: Delegate a coding task through Nox in an isolated sandbox. Run locally or submit to the configured remote Docker/gVisor worker, validate it, and publish successful changes to a local branch or pull request. Use only when the user explicitly invokes $nox or asks to delegate a coding task through Nox.
---

# Nox sandbox execution

Use Nox when the user explicitly asks for sandboxed execution. Do not launch Nox merely because a task could benefit from isolation.

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
9. Write the hydrated contract to a persistent Nox-owned task file such as `~/.nox/tasks/<task-id>.md`, including the validation command under `Validation > Commands`. Keep it until the delegated run reaches a terminal state.
10. Choose a concise pull-request title from the user's requested change.
11. Do not run the long-lived execution command in the main thread. Use one background Codex worker subagent per Nox task.

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

   Then spawn the background worker subagent with only the returned run ID. It runs:

   ```bash
   nox watch --remote <run-id>
   ```

   The monitor reports the terminal state, branch, commit, or pull-request URL. It does not resubmit, reinterpret the contract, merge the pull request, or cancel the run when monitoring stops. Use `nox cancel --remote <run-id>` only when the user explicitly requests cancellation.

   Local mode (`NOX_REMOTE_URL` is unset):

   Choose a unique result branch such as `nox/<short-task-slug>-<timestamp>`. Spawn the background worker subagent to own the complete foreground lifecycle:

   ```bash
   nox doctor && \
   nox launch \
     --repo <repository-root> \
     --from <exact-committed-base-sha> \
     --output-branch <new-local-branch> \
     --task-file <task-file> \
     --validate <validation-command>
   ```

   The worker must not impose a shorter wrapper timeout, modify the caller checkout, switch the result branch, or clean up evidence. It reports the run ID, result branch, commit, validation, and evidence path when complete.

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
- Codex runs autonomously inside the disposable sandbox; the original checkout is not mounted or modified.
- Nox creates the host-side result commit only after validation succeeds.
- The main thread must not wait on the long-lived local launch or remote monitor subagent.
- Never use shell backgrounding or a wrapper timeout shorter than Nox's execution deadline and teardown grace.
- Worker subagents execute or monitor only; they do not rehydrate, resubmit, merge, switch branches, or clean up evidence.
- Do not invoke `nox cleanup` automatically; failed workspaces and logs may be needed as evidence.
- Treat agent logs and staged credentials as sensitive.
