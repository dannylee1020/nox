# Run monitoring

`nox ui` serves a read-only console for active and recent Nox runs. It reads stored run metadata and logs; it does not submit, cancel, retry, clean up, or otherwise change a run.

## Local

Start the console on the machine that runs Nox:

```bash
nox ui
```

Open `http://127.0.0.1:8081`. By default the console reads `~/.nox/runs`, shows every active run, and keeps the newest 20 terminal runs.

Useful options:

```text
--listen <loopback-address>
--runs-root <directory>
--remote-status-root <directory>
--recent <count>
```

The listener rejects non-loopback addresses.

## Remote worker

The remote profile installs `nox-ui.service` alongside the execution API. It reads sandbox evidence from `/var/lib/nox/runs` and safe lifecycle summaries from `/var/lib/nox/jobs`.

Start it on the worker:

```bash
sudo systemctl enable --now nox-ui
```

Reach it through an SSH tunnel:

```bash
ssh -L 8081:127.0.0.1:8081 <worker-host>
```

Then open `http://127.0.0.1:8081` on the operator machine. The UI process needs no Nox API token or GitHub credential.

## Evidence and security

The console exposes repository labels, lifecycle state, timing, output branches, result commits, pull request links, errors, and setup, agent, and validation logs. It intentionally omits container IDs, workspace paths, volume names, credential paths, and task contracts.

Logs are rendered as plain text, but may still contain sensitive output produced by repository tools. Loopback binding and host access through SSH are the security boundary.

Remote lifecycle files support monitoring, not recovery. If the coordinator restarts, nonterminal records from its previous instance appear as interrupted.
