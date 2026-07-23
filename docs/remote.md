# Self-hosted remote execution

Remote mode runs a trusted Nox API on one private Linux VM. Each request creates an ephemeral Docker/gVisor (`runsc`) sandbox, runs the repository's optional `.nox/setup.sh`, Codex, and validation, then either creates a GitHub pull request for implementation changes or completes as an evidence-only test run.

```text
private client
  → nox serve on a private VM
  → Docker/runsc sandbox
  → Codex tester + validation
  → implementation: trusted branch push → GitHub pull request
  → test: validation evidence only
  → teardown
```

Remote mode does not expose raw agent logs or patch download endpoints through the execution API. Operators can inspect VM-local evidence through the loopback-only monitoring UI or the system journal.

## Prerequisites

- A private Linux VM with Docker and `runsc` registered.
- A GitHub fine-grained token with repository Contents and Pull requests write access.
- Network controls that keep the API private and block sandbox access to the API port, Docker daemon, and cloud metadata endpoint `169.254.169.254`.

Install gVisor and register the Docker runtime using the [gVisor installation guide](https://gvisor.dev/docs/user_guide/install/).

Install the remote profile from a checkout or the published installer:

```bash
curl -fsSL https://raw.githubusercontent.com/nox-dev/nox/main/install.sh | bash -s -- remote
# or:
sudo ./install.sh remote
```

The installer verifies Docker/runsc, builds `nox-runner:v0`, installs `/usr/local/bin/nox` and both systemd units, and creates the service, configuration, and state directories. It does not enable or start either service, and it does not install Colima or the Codex skill.

## Configuration

The installer creates `/etc/nox/nox.env` and preserves it on subsequent runs. Edit it as root:

```text
NOX_API_TOKEN=<private-api-token>
NOX_GITHUB_TOKEN=<github-fine-grained-token>
NOX_LISTEN_ADDR=0.0.0.0:8080
NOX_STATE_ROOT=/var/lib/nox
NOX_MAX_CONCURRENT_RUNS=5
CODEX_HOME=/var/lib/nox/codex
NOX_GIT_NAME=Nox Worker
NOX_GIT_EMAIL=nox@localhost
```

Authenticate a ChatGPT account on the headless worker with device authorization:

```bash
sudo -u nox -H env \
  HOME=/var/lib/nox \
  CODEX_HOME=/var/lib/nox/codex \
  codex login --device-auth
```

Open the printed URL on another machine, enter the one-time code, then verify with the same environment:

```bash
sudo -u nox -H env \
  HOME=/var/lib/nox \
  CODEX_HOME=/var/lib/nox/codex \
  codex login status
```

For fully unattended, usage-based authentication, pipe a dedicated OpenAI API key into `codex login --with-api-key` instead. Store either credential as a secret.

Tokens stay in the trusted API process. They are not put in task contracts, workspace files, Git URLs, command arguments, or sandbox environment variables.

## Service

After configuring tokens and Codex authentication, load and start the unit:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now nox
sudo systemctl status nox
```

The installer intentionally does not run these service lifecycle commands.

The installer also creates a credential-free `/etc/nox/nox-ui.env`:

```text
NOX_UI_LISTEN_ADDR=127.0.0.1:8081
NOX_UI_RUNS_ROOT=/var/lib/nox/runs
NOX_UI_REMOTE_STATUS_ROOT=/var/lib/nox/jobs
NOX_UI_RECENT_RUNS=20
```

The monitoring UI is a separate read-only process and does not receive the API or GitHub tokens. Start it independently:

```bash
sudo systemctl enable --now nox-ui
sudo systemctl status nox-ui
```

Keep it bound to loopback. From an operator machine, create a tunnel and open `http://127.0.0.1:8081` locally:

```bash
ssh -L 8081:127.0.0.1:8081 <worker-host>
```

Check readiness:

```bash
curl http://127.0.0.1:8080/healthz
```

## Codex client

For normal use, configure the machine running Codex instead of submitting requests manually:

```bash
export NOX_REMOTE_URL=https://nox.internal.example
export NOX_API_TOKEN=<private-api-token>
```

When the user invokes `$nox`, the skill runs `nox submit --detach`, returns the run ID, and starts a background worker that polls `nox inspect --remote`. Implementation workers report the pull request URL; test workers report validation findings and evidence-only completion. The client does not require local Docker or `nox doctor`.

Cancel an active remote run explicitly with:

```bash
nox cancel --remote <run-id>
```

## Direct API

Use the API directly for diagnostics or integrations. Submit a run:

```bash
curl -fsS -X POST http://127.0.0.1:8080/v1/runs \
  -H "Authorization: Bearer $NOX_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "repository": "owner/repository",
    "baseBranch": "main",
    "baseCommit": "0123456789abcdef0123456789abcdef01234567",
    "mode": "feat",
    "title": "Implement the requested change",
    "task": "# Nox execution contract v1\n...",
    "validation": "go test ./...",
    "network": "online",
    "timeoutSeconds": 7200
  }'
```

The response is `202 Accepted` with a run ID. Poll it:

```bash
curl -fsS \
  -H "Authorization: Bearer $NOX_API_TOKEN" \
  http://127.0.0.1:8080/v1/runs/<run-id>
```

Cancel it:

```bash
curl -fsS -X POST \
  -H "Authorization: Bearer $NOX_API_TOKEN" \
  http://127.0.0.1:8080/v1/runs/<run-id>/cancel
```

A successful implementation status includes the generated `nox/<run-id>` branch, result commit, and `pullRequestUrl`. A successful test status has `mode: "test"`, validation/source-integrity evidence, and no publication fields. Failed, cancelled, timed-out, and no-change runs do not create pull requests. Test runs retain metadata and logs only; they do not expose arbitrary artifacts or workspaces.

The server accepts up to `NOX_MAX_CONCURRENT_RUNS` active runs (default `5`). `nox serve --max-concurrent-runs` overrides the environment value. Submissions above the limit return `429`; there is no durable queue or restart recovery.

## Operator diagnostics

Remote users receive concise status only. Operators can inspect details on the VM:

```bash
sudo journalctl -u nox -f
sudo journalctl -u nox-ui -f
sudo find /var/lib/nox/runs -maxdepth 2 -type f -print
```

Do not expose the run directory, monitoring UI, or system journal on a public interface. Logs may contain sensitive repository or tool output.
