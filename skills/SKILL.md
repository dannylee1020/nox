---
name: nox
description: Use only when the user explicitly invokes $nox or asks to delegate a coding task through Nox. Run the task in an isolated Docker/gVisor sandbox, validate it, and publish successful changes to a new local branch.
---

# Nox sandbox execution

Use Nox when the user explicitly asks for sandboxed execution. Do not launch Nox merely because a task could benefit from isolation.

## Workflow

1. Resolve the installed Nox CLI using `command -v nox`. If it is not on `PATH`, read `references/installation.md` and use the recorded absolute binary path.
2. Confirm the current directory is the user's Git repository. Never switch, reset, clean, stash, merge, push, or delete branches in the user's checkout.
3. Read the current branch and configured Git identity. Preserve any dirty changes; Nox excludes uncommitted source changes from the sandbox base.
4. Run `nox doctor`. If it fails, report the runtime/setup error instead of falling back to another container runtime.
5. Convert the user's explicit request into a precise task. Include scope, acceptance criteria, and files or behavior that must not change.
6. Use the user's validation command when provided. If it is omitted, inspect repository-native validation commands and choose one only when the correct command is unambiguous; otherwise ask the user.
7. Choose a unique result branch such as `nox/<short-task-slug>-<timestamp>`.
8. Run Nox with the Codex adapter:

   ```bash
   nox launch \
     --repo <repository-root> \
     --from <current-branch> \
     --output-branch <new-local-branch> \
     --agent codex \
     --task-file <task-file> \
     --validate <validation-command>
   ```

9. Record the announced run ID. Use `nox watch <run-id>` from another terminal when the user wants live lifecycle and log monitoring.
10. On success, inspect the run and diff:

    ```bash
    nox inspect <run-id>
    nox diff <run-id>
    ```

11. Report the result branch, commit, validation result, run ID, and evidence path under `~/.nox/runs/<run-id>`.
12. Do not automatically switch to the result branch. Let the user decide whether to inspect or switch to it.

## Safety boundaries

- Nox's outer `runsc` container is the isolation boundary. Never bypass `nox doctor` or substitute `runc`.
- Codex runs autonomously inside the disposable sandbox; the original checkout is not mounted or modified.
- Nox creates the host-side result commit only after validation succeeds.
- Do not invoke `nox cleanup` automatically; failed workspaces and logs may be needed as evidence.
- Treat agent logs and staged credentials as sensitive.
