package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/store"
)

func TestWatchFollowsLifecycleAndLogs(t *testing.T) {
	st := store.New(t.TempDir())
	metadata := store.Metadata{RunID: "watch123", State: store.StateSettingUp, StartedAt: time.Now()}
	if err := st.WriteMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = st.WriteText(metadata.RunID, "setup.log", "setup output\n")
		metadata.State = store.StateAgentRunning
		_ = st.WriteMetadata(metadata)
		time.Sleep(20 * time.Millisecond)
		_ = st.WriteText(metadata.RunID, "agent.log", "agent output\n")
		metadata.State = store.StateValidating
		_ = st.WriteMetadata(metadata)
		time.Sleep(20 * time.Millisecond)
		_ = st.WriteText(metadata.RunID, "validation.log", "validation output\n")
		metadata.State = store.StateTeardown
		_ = st.WriteMetadata(metadata)
		time.Sleep(20 * time.Millisecond)
		metadata.State = store.StateCompleted
		_ = st.WriteMetadata(metadata)
	}()

	var output, status bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := watchRun(ctx, st, metadata.RunID, 5*time.Millisecond, &output, &status); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"setup output", "agent output", "validation output"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("watch output missing %q: %s", want, output.String())
		}
	}
	for _, want := range []string{"setting_up", "agent_running", "validating", "teardown", "completed"} {
		if !strings.Contains(status.String(), want) {
			t.Errorf("watch status missing %q: %s", want, status.String())
		}
	}
}

func TestWatchReturnsFailedRunError(t *testing.T) {
	st := store.New(t.TempDir())
	metadata := store.Metadata{RunID: "failed123", State: store.StateFailed, Error: "agent failed", StartedAt: time.Now()}
	if err := st.WriteMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	var output, status bytes.Buffer
	err := watchRun(context.Background(), st, metadata.RunID, time.Millisecond, &output, &status)
	if err == nil || !strings.Contains(err.Error(), "agent failed") {
		t.Fatalf("watch error = %v", err)
	}
	if !strings.Contains(status.String(), "error: agent failed") {
		t.Fatalf("watch status = %q", status.String())
	}
}

func TestCollectActiveRunsNewestFirstAndSkipsTerminalAndUnreadable(t *testing.T) {
	st := store.New(t.TempDir())
	now := time.Now()
	fixtures := []store.Metadata{
		{RunID: "older", State: store.StateAgentRunning, StartedAt: now.Add(-2 * time.Minute)},
		{RunID: "newer", State: store.StateValidating, StartedAt: now.Add(-time.Minute)},
		{RunID: "teardown", State: store.StateTeardown, StartedAt: now.Add(-30 * time.Second)},
		{RunID: "completed", State: store.StateCompleted, StartedAt: now},
		{RunID: "failed", State: store.StateFailed, StartedAt: now},
		{RunID: "cancelled", State: store.StateCancelled, StartedAt: now},
	}
	for _, metadata := range fixtures {
		if err := st.WriteMetadata(metadata); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Ensure("unreadable"); err != nil {
		t.Fatal(err)
	}

	var warnings bytes.Buffer
	runs, err := collectActiveRuns(st, &warnings)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runIDs(runs), []string{"teardown", "newer", "older"}; !equalStrings(got, want) {
		t.Fatalf("run IDs = %v, want %v", got, want)
	}
	if !strings.Contains(warnings.String(), "warning: skipping run unreadable") {
		t.Fatalf("warnings = %q", warnings.String())
	}
}

func TestRenderActiveRunsShowsStableFieldsAndValidationStatus(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	runs := []activeRun{
		{id: "pending", metadata: store.Metadata{State: store.StateAgentRunning, StartedAt: now.Add(-time.Minute), OutputBranch: "nox/pending"}},
		{id: "running", metadata: store.Metadata{State: store.StateValidating, StartedAt: now.Add(-2 * time.Minute), OutputBranch: "nox/running"}},
		{id: "passed", metadata: store.Metadata{State: store.StatePublishing, StartedAt: now.Add(-3 * time.Minute), OutputBranch: "nox/passed"}},
		{id: "finalizing", metadata: store.Metadata{State: store.StateTeardown, StartedAt: now.Add(-4 * time.Minute)}},
	}
	var output bytes.Buffer
	if err := renderActiveRuns(&output, runs, now); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"Active local runs: 4", "RUN ID", "STATE", "ELAPSED", "VALIDATION", "OUTPUT BRANCH",
		"pending", "agent_running", "1m0s", "nox/pending",
		"running", "validating", "2m0s", "nox/running",
		"passed", "publishing", "3m0s", "nox/passed",
		"finalizing", "teardown", "4m0s", "-",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("table missing %q:\n%s", want, text)
		}
	}
}

func TestSelectActiveRunRepromptsAndReturnsEOF(t *testing.T) {
	var output bytes.Buffer
	selection, err := selectActiveRun(strings.NewReader("invalid\n0\n3\n2\n"), &output, 2)
	if err != nil {
		t.Fatal(err)
	}
	if selection != 1 {
		t.Fatalf("selection = %d, want 1", selection)
	}
	if got := strings.Count(output.String(), "Invalid selection"); got != 3 {
		t.Fatalf("invalid selection messages = %d, output = %q", got, output.String())
	}

	_, err = selectActiveRun(strings.NewReader(""), io.Discard, 2)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("EOF error = %v", err)
	}
}

func TestWatchActiveRunsNonInteractivePrintsCommandsAndReturns(t *testing.T) {
	st := store.New(t.TempDir())
	metadata := store.Metadata{
		RunID: "active123", State: store.StateAgentRunning,
		StartedAt: time.Now(), OutputBranch: "nox/active",
	}
	if err := st.WriteMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	var output, status bytes.Buffer
	if err := watchActiveRuns(context.Background(), st, time.Hour, strings.NewReader(""), &output, &status, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "nox watch active123") {
		t.Fatalf("output = %q", output.String())
	}
	if strings.Contains(output.String(), "Select a run") {
		t.Fatalf("non-interactive output prompted for input: %q", output.String())
	}
}

func TestWatchActiveRunsInteractiveSelectionFollowsChosenRun(t *testing.T) {
	st := store.New(t.TempDir())
	now := time.Now()
	newer := store.Metadata{RunID: "newer", State: store.StateAgentRunning, StartedAt: now}
	chosen := store.Metadata{RunID: "chosen", State: store.StateAgentRunning, StartedAt: now.Add(-time.Minute)}
	for _, metadata := range []store.Metadata{newer, chosen} {
		if err := st.WriteMetadata(metadata); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{
		"setup.log": "chosen setup\n", "agent.log": "chosen agent\n", "validation.log": "chosen validation\n",
	} {
		if err := st.WriteText(chosen.RunID, name, contents); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		chosen.State = store.StateCompleted
		_ = st.WriteMetadata(chosen)
	}()

	var output, status bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := watchActiveRuns(ctx, st, 5*time.Millisecond, strings.NewReader("2\n"), &output, &status, true, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chosen setup", "chosen agent", "chosen validation"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("selected watch output missing %q: %s", want, output.String())
		}
	}
	if !strings.Contains(status.String(), "run chosen:") {
		t.Fatalf("status = %q", status.String())
	}
}

func TestWatchActiveRunsEmptyState(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.WriteMetadata(store.Metadata{RunID: "done", State: store.StateCompleted, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := watchActiveRuns(context.Background(), st, time.Second, strings.NewReader(""), &output, io.Discard, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "No active local runs.\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestWatchRemoteRequiresRunID(t *testing.T) {
	err := watch([]string{"--remote"})
	if err == nil || !strings.Contains(err.Error(), "remote watch requires a run ID") {
		t.Fatalf("error = %v", err)
	}
}

func TestDevNullIsNotInteractive(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if stdinIsInteractive(file) {
		t.Fatal("os.DevNull must not be treated as an interactive terminal")
	}
}

func runIDs(runs []activeRun) []string {
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.id
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
