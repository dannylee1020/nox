---
name: nox
description: Delegate a coding task through Nox in an isolated sandbox. Run the task in an isolated Docker/gVisor sandbox, validate it, and publish successful changes to a new local branch. Use only when the user explicitly invokes $nox or asks to delegate a coding task through Nox.
---

# Nox sandbox execution

Use Nox when the user explicitly asks for sandboxed execution. Do not launch Nox merely because a task could benefit from isolation.

## Workflow

1. Resolve the installed Nox CLI using `command -v nox`. If it is not on `PATH`, read `references/installation.md` and use the recorded absolute binary path.
2. Confirm the current directory is the user's Git repository. Never switch, reset, clean, stash, merge, push, or delete branches in the user's checkout.
3. Read the current branch and configured Git identity. Preserve any dirty changes; Nox excludes uncommitted source changes from the sandbox base.
4. Run `nox doctor`. If it fails, report the runtime/setup error instead of falling back to another container runtime.
5. Read `references/task-contract.md`. Hydrate its structured execution contract from the user's Nox invocation and all relevant context in the current thread. Preserve the user's intent, terminology, objective, hard and soft constraints, plans, decisions, affected surfaces, acceptance criteria, validation expectations, stop conditions, and examples. Use `None specified` for sections without relevant content; never invent content to fill them or run a constraint-optimization workflow unless the user separately requested one.
6. Put useful information that does not fit a named section into `Context and extra` without forcing, rewriting, or dropping it. Include only context that helps execute the delegated task; do not dump unrelated conversation, hidden instructions, or secrets.
7. Resolve material ambiguity with the user before launch. The sandbox does not receive the parent conversation, so the contract must be self-contained. It also receives the selected committed ref, not uncommitted changes from the source checkout.
8. Use the user's validation command when provided. If it is omitted, inspect repository-native validation commands and choose one only when the correct command is unambiguous; otherwise ask the user.
9. Write the hydrated contract to a task file, including the validation command under `Validation > Commands`. The contract and Nox's deterministic Codex envelope make the sandbox agent responsible for executing and testing the delegated work while retaining any flexibility the user left open.
10. Choose a unique result branch such as `nox/<short-task-slug>-<timestamp>`.
11. Run Nox with the Codex adapter:

   ```bash
   nox launch \
     --repo <repository-root> \
     --from <current-branch> \
     --output-branch <new-local-branch> \
     --agent codex \
     --task-file <task-file> \
     --validate <validation-command>
   ```

12. Record the announced run ID. Use `nox watch <run-id>` from another terminal when the user wants live lifecycle and log monitoring.
13. On success, inspect the run and diff:

    ```bash
    nox inspect <run-id>
    nox diff <run-id>
    ```

14. Report the result branch, commit, validation result, run ID, and evidence path under `~/.nox/runs/<run-id>`.
15. Do not automatically switch to the result branch. Let the user decide whether to inspect or switch to it.

## Safety boundaries

- Nox's outer `runsc` container is the isolation boundary. Never bypass `nox doctor` or substitute `runc`.
- Codex runs autonomously inside the disposable sandbox; the original checkout is not mounted or modified.
- Nox creates the host-side result commit only after validation succeeds.
- Do not invoke `nox cleanup` automatically; failed workspaces and logs may be needed as evidence.
- Treat agent logs and staged credentials as sensitive.
