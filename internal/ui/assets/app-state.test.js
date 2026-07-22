"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const ui = require("./app-state.js");

test("active filter falls back to recent and keeps detail visible", () => {
  const runs = [{runId: "done", state: "completed"}];
  assert.deepEqual(ui.reconcileSelection(runs, "active", ""), {
    filter: "recent",
    selectedId: "done"
  });
});

test("selection always belongs to the effective filter", () => {
  const runs = [
    {runId: "active", state: "agent_running"},
    {runId: "failed", state: "failed"},
    {runId: "done", state: "completed"}
  ];
  assert.deepEqual(ui.reconcileSelection(runs, "failed", "active"), {
    filter: "failed",
    selectedId: "failed"
  });
  assert.deepEqual(ui.reconcileSelection(runs, "active", "active"), {
    filter: "active",
    selectedId: "active"
  });
});

test("live substages preserve the four-stage lifecycle", () => {
  assert.deepEqual(ui.stageSummary({state: "cloning", stage: "cloning"}), {
    macro: "Setup",
    detail: "Cloning repository",
    tone: "neutral"
  });
  assert.deepEqual(ui.stageSummary({state: "validating", stage: "validating"}), {
    macro: "Validation",
    detail: "Running validation",
    tone: "info"
  });
  assert.deepEqual(ui.stageSummary({state: "running", stage: "execution"}), {
    macro: "Agent",
    detail: "Agent working",
    tone: "active"
  });
  assert.deepEqual(ui.lifecycleView({state: "no_changes", stage: "completed"}), {
    labels: ["Setup", "Agent", "Validation", "No changes"],
    complete: true,
    current: undefined
  });
});
