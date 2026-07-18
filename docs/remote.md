# Self-hosted remote execution

Remote mode runs a trusted Nox API on one private Linux VM. Each request creates an ephemeral Docker/gVisor (`runsc`) sandbox, runs Codex and validation, then creates a GitHub pull request for validated changes.

```text
private client
  → nox serve on a private VM
  → Docker/runsc sandbox
  → Codex + validation
  → trusted branch push
  → GitHub pull request
  → teardown
```

Remote mode does not expose raw agent logs or patch download endpoints. Operators can inspect the VM-local run state and system journal when a run fails.

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

The installer verifies Docker/runsc, builds `nox-runner:v0`, installs `/usr/local/bin/nox` and the systemd unit, and creates the service/config/state directories. It does not enable or start the service, and it does not install Colima or the Codex skill.

## Configuration

The installer creates `/etc/nox/nox.env` and preserves it on subsequent runs. Edit it as root:

```text
NOX_API_TOKEN=<private-api-token>
NOX_GITHUB_TOKEN=<github-fine-grained-token>
NOX_LISTEN_ADDR=0.0.0.0:8080
NOX_STATE_ROOT=/var/lib/nox
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

When the user invokes `$nox`, the skill runs `nox submit`, which verifies the current GitHub branch, sends the execution contract, polls the worker, and reports the pull request URL. The client does not require local Docker or `nox doctor`.

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

A successful status includes the generated `nox/<run-id>` branch, result commit, and `pullRequestUrl`. Failed, cancelled, timed-out, and no-change runs do not create pull requests.

The first version accepts one active run. A second submission returns `429`; there is no durable queue or restart recovery.

## Operator diagnostics

Remote users receive concise status only. Operators can inspect details on the VM:

```bash
sudo journalctl -u nox -f
sudo find /var/lib/nox/runs -maxdepth 2 -type f -print
```

Do not expose the run directory or system journal through the API.
