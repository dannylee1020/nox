(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
    return;
  }
  root.NoxUIState = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const terminalStates = new Set(["completed", "no_changes", "failed", "cancelled", "interrupted"]);
  const failedStates = new Set(["failed", "cancelled", "interrupted"]);
  const lifecycleLabels = ["Setup", "Agent", "Validation", "Published"];
  const lifecyclePosition = {
    queued: 0,
    initializing: 0,
    cloning: 0,
    starting: 0,
    setting_up: 0,
    agent_running: 1,
    running: 1,
    execution: 1,
    validating: 2,
    publishing: 3,
    pull_request: 3,
    teardown: 3
  };

  const substageLabels = {
    queued: "Waiting to start",
    initializing: "Preparing run",
    cloning: "Cloning repository",
    starting: "Starting sandbox",
    setting_up: "Preparing environment",
    agent_running: "Agent working",
    running: "Agent working",
    execution: "Agent working",
    validating: "Running validation",
    publishing: "Publishing result",
    pull_request: "Pull request ready",
    teardown: "Cleaning up sandbox",
    completed: "Branch ready",
    no_changes: "Nothing to publish",
    failed: "Run stopped",
    cancelled: "Cancelled by request",
    interrupted: "Coordinator restarted"
  };

  function filterRuns(runs, filter) {
    if (filter === "active") {
      return runs.filter((run) => !terminalStates.has(run.state));
    }
    if (filter === "failed") {
      return runs.filter((run) => failedStates.has(run.state));
    }
    return runs.filter((run) => terminalStates.has(run.state));
  }

  function reconcileSelection(runs, filter, selectedId) {
    let effectiveFilter = filter;
    let visibleRuns = filterRuns(runs, effectiveFilter);
    if (effectiveFilter === "active" && visibleRuns.length === 0) {
      const recentRuns = filterRuns(runs, "recent");
      if (recentRuns.length > 0) {
        effectiveFilter = "recent";
        visibleRuns = recentRuns;
      }
    }
    const selectedIsVisible = visibleRuns.some((run) => run.runId === selectedId);
    return {
      filter: effectiveFilter,
      selectedId: selectedIsVisible ? selectedId : (visibleRuns[0] ? visibleRuns[0].runId : "")
    };
  }

  function displayState(value) {
    if (value === "agent_running" || value === "running") return "Running";
    if (value === "setting_up") return "Setting up";
    if (value === "no_changes") return "No changes";
    return humanize(value);
  }

  function stageSummary(run) {
    const state = run.state || "";
    const stage = run.stage && run.stage !== state ? run.stage : state;
    let macro = "Setup";
    if (state === "agent_running" || state === "running") macro = "Agent";
    if (state === "validating") macro = "Validation";
    if (state === "publishing" || state === "teardown" || state === "completed") macro = "Published";
    if (state === "no_changes") macro = "No changes";
    if (failedStates.has(state)) macro = displayState(state);
    return {
      macro,
      detail: substageLabels[stage] || substageLabels[state] || displayState(stage || state),
      tone: stateTone(state)
    };
  }

  function lifecycleView(run) {
    const noChanges = run.state === "no_changes";
    const labels = noChanges ? ["Setup", "Agent", "Validation", "No changes"] : lifecycleLabels.slice();
    const complete = run.state === "completed" || noChanges;
    const current = lifecyclePosition[run.state] ?? lifecyclePosition[run.stage];
    return {labels, complete, current};
  }

  function stateTone(value) {
    if (failedStates.has(value)) return "danger";
    if (value === "queued" || value === "initializing") return "warning";
    if (value === "validating" || value === "publishing" || value === "teardown") return "info";
    if (value === "completed" || value === "no_changes" || value === "agent_running" || value === "running" || value === "setting_up") return "active";
    return "neutral";
  }

  function humanize(value) {
    if (!value) return "Unknown";
    return value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  return {
    displayState,
    failedStates,
    filterRuns,
    humanize,
    lifecycleView,
    reconcileSelection,
    stageSummary,
    stateTone,
    terminalStates
  };
});
