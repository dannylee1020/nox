(() => {
  "use strict";

  const {
    displayState,
    filterRuns,
    humanize,
    lifecycleView,
    reconcileSelection,
    stageSummary,
    stateTone
  } = window.NoxUIState;
  const maxLogCharacters = 2 * 1024 * 1024;

  const state = {
    snapshot: null,
    selectedId: "",
    filter: "active",
    logKind: "setup",
    logState: new Map(),
    stopped: false
  };

  const elements = {
    warning: document.querySelector("#warning"),
    runList: document.querySelector("#run-list"),
    runCount: document.querySelector("#run-count"),
    detailContent: document.querySelector("#detail-content"),
    logSize: document.querySelector("#toggle-log-size"),
    follow: document.querySelector("#follow-log"),
    logOutput: document.querySelector("#log-output code")
  };

  const countElements = {
    active: document.querySelector("#count-active"),
    validating: document.querySelector("#count-validating"),
    completed: document.querySelector("#count-completed"),
    failed: document.querySelector("#count-failed")
  };

  const filterButtons = Array.from(document.querySelectorAll("[data-filter]"));
  filterButtons.forEach((button) => {
    button.addEventListener("click", () => {
      void selectFilter(button.dataset.filter);
    });
  });

  elements.logSize.addEventListener("click", () => {
    const expanded = elements.detailContent.classList.toggle("logs-expanded");
    elements.logSize.setAttribute("aria-expanded", String(expanded));
    elements.logSize.textContent = expanded ? "Collapse logs" : "Expand logs";
  });

  const logTabs = Array.from(document.querySelectorAll("[data-log]"));
  logTabs.forEach((button, index) => {
    button.addEventListener("click", () => {
      activateLogTab(button);
    });
    button.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const direction = event.key === "ArrowRight" ? 1 : -1;
      const next = logTabs[(index + direction + logTabs.length) % logTabs.length];
      next.focus();
      activateLogTab(next);
    });
  });

  function activateLogTab(button) {
    state.logKind = button.dataset.log;
    logTabs.forEach((item) => {
      const selected = item === button;
      item.setAttribute("aria-selected", String(selected));
      item.tabIndex = selected ? 0 : -1;
    });
    renderStoredLog();
    void refreshLog();
  }

  async function selectFilter(filter) {
    const next = reconcileSelection(state.snapshot ? state.snapshot.runs : [], filter, state.selectedId);
    state.filter = next.filter;
    syncFilterButtons();
    setSelectedId(next.selectedId);
    renderRunList();
    if (!state.selectedId) {
      renderEmptyDetail();
      return;
    }
    await loadSelectedRun(state.selectedId);
  }

  function syncFilterButtons() {
    filterButtons.forEach((button) => {
      button.setAttribute("aria-pressed", String(button.dataset.filter === state.filter));
    });
  }

  function setSelectedId(runId) {
    if (runId === state.selectedId) return;
    state.selectedId = runId;
    state.logKind = "setup";
    logTabs.forEach((item) => {
      const selected = item.dataset.log === "setup";
      item.setAttribute("aria-selected", String(selected));
      item.tabIndex = selected ? 0 : -1;
    });
  }

  async function requestJSON(path) {
    const response = await fetch(path, {cache: "no-store", headers: {Accept: "application/json"}});
    if (!response.ok) {
      throw new Error(`Request failed with status ${response.status}`);
    }
    return response.json();
  }

  async function refresh() {
    try {
      const snapshot = await requestJSON("/api/runs");
      state.snapshot = snapshot;
      renderSummary();
      chooseSelection();
      renderRunList();
      if (state.selectedId) {
        const selectedId = state.selectedId;
        const detail = await requestJSON(`/api/runs/${encodeURIComponent(selectedId)}`);
        if (state.selectedId === selectedId) {
          renderDetail(detail);
          await refreshLog();
        }
      } else {
        renderEmptyDetail();
      }
    } catch (error) {
      showWarning("The console cannot read current run evidence. Existing information remains visible.");
    } finally {
      if (!state.stopped) {
        window.setTimeout(refresh, 1000);
      }
    }
  }

  function renderSummary() {
    Object.entries(countElements).forEach(([key, element]) => {
      element.textContent = String(state.snapshot.counts[key] || 0);
    });
    if (state.snapshot.warnings && state.snapshot.warnings.length) {
      showWarning(state.snapshot.warnings.join(" "));
    } else {
      elements.warning.hidden = true;
    }
  }

  function showWarning(message) {
    elements.warning.textContent = message;
    elements.warning.hidden = false;
  }

  function chooseSelection() {
    const runs = state.snapshot.runs || [];
    const next = reconcileSelection(runs, state.filter, state.selectedId);
    state.filter = next.filter;
    syncFilterButtons();
    setSelectedId(next.selectedId);
  }

  function filteredRuns() {
    return filterRuns(state.snapshot ? state.snapshot.runs : [], state.filter);
  }

  function renderRunList() {
    const runs = filteredRuns();
    elements.runList.replaceChildren();
    elements.runList.setAttribute("aria-busy", "false");
    elements.runCount.textContent = `${runs.length} ${runs.length === 1 ? "run" : "runs"}`;
    if (!runs.length) {
      const empty = document.createElement("div");
      empty.className = "list-empty";
      const title = document.createElement("strong");
      title.textContent = state.filter === "active" ? "No active runs" : state.filter === "failed" ? "No recent failures" : "No recent runs";
      const copy = document.createElement("span");
      copy.textContent = state.filter === "active" ? "New work appears here when Nox starts it." : "Terminal run evidence appears here.";
      empty.append(title, copy);
      elements.runList.append(empty);
      return;
    }
    runs.forEach((run) => elements.runList.append(createRunRow(run)));
  }

  function createRunRow(run) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "run-row";
    button.setAttribute("aria-current", String(run.runId === state.selectedId));
    button.addEventListener("click", () => selectRun(run.runId));

    const repository = document.createElement("span");
    repository.className = "run-repository";
    const repositoryValue = run.repository || "Unknown repository";
    repository.textContent = repositoryName(repositoryValue);
    repository.title = repositoryValue;

    const identifier = document.createElement("span");
    identifier.className = "run-row-id";
    identifier.textContent = run.runId;

    const elapsed = document.createElement("span");
    elapsed.className = "run-elapsed";
    elapsed.textContent = formatDuration(run.elapsedSeconds);

    const summary = stageSummary(run);
    const progress = document.createElement("span");
    progress.className = "run-progress";
    progress.dataset.tone = summary.tone;
    const macro = document.createElement("strong");
    macro.textContent = summary.macro;
    const substage = document.createElement("span");
    substage.textContent = summary.detail;
    progress.append(macro, substage);

    const source = document.createElement("span");
    source.className = "run-row-source";
    source.textContent = run.source === "remote" ? "REMOTE" : "LOCAL";
    button.dataset.tone = summary.tone;
    button.setAttribute("aria-label", `${repositoryValue}, run ${run.runId}, ${summary.macro}, ${summary.detail}, ${run.source === "remote" ? "remote" : "local"}, ${formatDuration(run.elapsedSeconds)}`);
    button.append(repository, identifier, elapsed, progress, source);
    return button;
  }

  async function selectRun(runId) {
    setSelectedId(runId);
    renderRunList();
    await loadSelectedRun(runId);
  }

  async function loadSelectedRun(runId) {
    try {
      const detail = await requestJSON(`/api/runs/${encodeURIComponent(runId)}`);
      if (state.selectedId === runId) {
        renderDetail(detail);
        renderStoredLog();
        await refreshLog();
      }
    } catch (error) {
      showWarning("The selected run is no longer available.");
    }
  }

  function renderEmptyDetail() {
    elements.detailContent.hidden = true;
  }

  function renderDetail(run) {
    elements.detailContent.hidden = false;
    const repositoryValue = run.repository || "Unknown repository";
    setText("#repository", repositoryName(repositoryValue));
    document.querySelector("#repository").title = repositoryValue;
    setText("#run-id", run.runId);
    setText("#source-badge", run.source === "remote" ? "REMOTE" : "LOCAL");
    const summary = stageSummary(run);
    setText("#run-substage", summary.detail);
    const badge = document.querySelector("#state-badge");
    badge.textContent = displayState(run.state);
    badge.dataset.tone = stateTone(run.state);
    setText("#elapsed", formatDuration(run.elapsedSeconds));
    setText("#validation", humanize(run.validation));
    setText("#base-ref", run.baseRef || "Not recorded");
    setText("#output-branch", run.outputBranch || "Pending");
    setText("#result-commit", run.resultCommit ? run.resultCommit.slice(0, 12) : "Pending");
    renderPublication(run);
    renderLifecycle(run);
    renderMessage(run);
  }

  function renderPublication(run) {
    const target = document.querySelector("#publication");
    target.replaceChildren();
    if (run.pullRequestUrl && safeHTTPURL(run.pullRequestUrl)) {
      const link = document.createElement("a");
      link.href = run.pullRequestUrl;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = "Open pull request";
      target.append(link);
      return;
    }
    target.textContent = run.state === "no_changes" ? "No changes" : run.outputBranch ? "Branch ready" : "Pending";
  }

  function renderLifecycle(run) {
    const list = document.querySelector("#lifecycle");
    const panel = document.querySelector(".lifecycle-panel");
    list.replaceChildren();
    const view = lifecycleView(run);
    const currentStep = view.complete ? 3 : (typeof view.current === "number" ? view.current : 0);
    panel.style.setProperty("--current-step", String(currentStep));
    view.labels.forEach((label, index) => {
      const item = document.createElement("li");
      const text = document.createElement("span");
      text.textContent = label;
      item.dataset.step = String(index + 1);
      item.append(text);
      if (view.complete || (typeof view.current === "number" && index < view.current)) {
        item.className = "complete";
        item.setAttribute("aria-label", `${label}: complete`);
      } else if (typeof view.current === "number" && index === view.current) {
        item.className = "current";
        item.setAttribute("aria-current", "step");
        item.setAttribute("aria-label", `${label}: current`);
      } else {
        item.setAttribute("aria-label", `${label}: pending`);
      }
      list.append(item);
    });
  }

  function renderMessage(run) {
    const message = document.querySelector("#run-message");
    const text = run.error || run.warning || (run.state === "interrupted" ? "The coordinator restarted before this run reached a terminal state." : "");
    message.hidden = !text;
    message.textContent = text;
    message.classList.toggle("is-warning", !run.error);
  }

  function logKey() {
    return `${state.selectedId}:${state.logKind}`;
  }

  function renderStoredLog() {
    const saved = state.logState.get(logKey());
    elements.logOutput.textContent = saved && saved.text ? saved.text : "Waiting for log output…";
  }

  async function refreshLog() {
    if (!state.selectedId) return;
    const runId = state.selectedId;
    const kind = state.logKind;
    const key = `${runId}:${kind}`;
    const saved = state.logState.get(key) || {offset: 0, text: ""};
    let loops = 0;
    let done = false;
    try {
      while (!done && loops < 8) {
        const chunk = await requestJSON(`/api/runs/${encodeURIComponent(runId)}/logs/${kind}?offset=${saved.offset}`);
        if (chunk.reset) saved.text = "";
        if (chunk.data) saved.text += chunk.data;
        if (saved.text.length > maxLogCharacters) {
          saved.text = `[Earlier output omitted]\n${saved.text.slice(-maxLogCharacters)}`;
        }
        saved.offset = chunk.nextOffset;
        done = chunk.eof;
        loops += 1;
      }
    } catch (error) {
      if (!saved.text) saved.text = "Log output is not available.";
    }
    state.logState.set(key, saved);
    if (state.selectedId === runId && state.logKind === kind) {
      renderStoredLog();
    }
    if (elements.follow.checked && state.selectedId === runId && state.logKind === kind) {
      const pre = elements.logOutput.parentElement;
      pre.scrollTop = pre.scrollHeight;
    }
  }

  function setText(selector, value) {
    document.querySelector(selector).textContent = value;
  }

  function repositoryName(value) {
    const parts = value.replaceAll("\\", "/").split("/").filter(Boolean);
    return parts.length ? parts[parts.length - 1] : value;
  }

  function formatDuration(seconds) {
    const total = Math.max(0, Number(seconds) || 0);
    if (total < 3600) {
      const minutes = Math.floor(total / 60);
      const secondsPart = total % 60;
      return `${String(minutes).padStart(2, "0")}:${String(secondsPart).padStart(2, "0")}`;
    }
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secondsPart = total % 60;
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(secondsPart).padStart(2, "0")}`;
  }

  function safeHTTPURL(value) {
    try {
      const url = new URL(value);
      return url.protocol === "https:" || url.protocol === "http:";
    } catch (error) {
      return false;
    }
  }

  window.addEventListener("beforeunload", () => { state.stopped = true; });
  void refresh();
})();
