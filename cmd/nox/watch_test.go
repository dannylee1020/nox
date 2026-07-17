package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/store"
)

func TestWatchFollowsLifecycleAndLogs(t *testing.T) {
	st := store.New(t.TempDir())
	metadata := store.Metadata{RunID: "watch123", State: store.StateAgentRunning, StartedAt: time.Now()}
	if err := st.WriteMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	go func() {
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
	for _, want := range []string{"agent output", "validation output"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("watch output missing %q: %s", want, output.String())
		}
	}
	for _, want := range []string{"agent_running", "validating", "teardown", "completed"} {
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
