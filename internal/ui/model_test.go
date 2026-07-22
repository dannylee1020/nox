package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/remote"
	"github.com/nox-dev/nox/internal/store"
)

func TestSnapshotOrdersActiveThenRecentAndLimitsTerminalRuns(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	st := store.New(root)
	fixtures := []store.Metadata{
		{RunID: "active-old", Repo: "/repo/old", State: store.StateAgentRunning, StartedAt: now.Add(-4 * time.Minute)},
		{RunID: "active-new", Repo: "/repo/new", State: store.StateValidating, StartedAt: now.Add(-2 * time.Minute)},
		{RunID: "done-old", Repo: "/repo/a", State: store.StateCompleted, StartedAt: now.Add(-10 * time.Minute), CompletedAt: now.Add(-8 * time.Minute)},
		{RunID: "done-new", Repo: "/repo/b", State: store.StateCompleted, StartedAt: now.Add(-5 * time.Minute), CompletedAt: now.Add(-3 * time.Minute)},
		{RunID: "failed", Repo: "/repo/c", State: store.StateFailed, StartedAt: now.Add(-4 * time.Minute), CompletedAt: now.Add(-time.Minute)},
	}
	for _, metadata := range fixtures {
		if err := st.WriteMetadata(metadata); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Ensure("corrupt"); err != nil {
		t.Fatal(err)
	}
	model, err := NewModel(Config{RunsRoot: root, Recent: 2, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot()
	want := []string{"active-new", "active-old", "failed", "done-new"}
	if len(snapshot.Runs) != len(want) {
		t.Fatalf("runs = %#v", snapshot.Runs)
	}
	for index, id := range want {
		if snapshot.Runs[index].RunID != id {
			t.Fatalf("run %d = %q, want %q", index, snapshot.Runs[index].RunID, id)
		}
	}
	if snapshot.Counts.Active != 2 || snapshot.Counts.Validating != 1 || snapshot.Counts.Completed != 1 || snapshot.Counts.Failed != 1 {
		t.Fatalf("counts = %#v", snapshot.Counts)
	}
	if len(snapshot.Warnings) != 1 || strings.Contains(snapshot.Warnings[0], root) {
		t.Fatalf("warnings = %#v", snapshot.Warnings)
	}
}

func TestRemoteRecordMergesPublicationAndDetectsStaleInstance(t *testing.T) {
	runsRoot := t.TempDir()
	statusRoot := t.TempDir()
	first, err := remote.NewStatusStore(statusRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := first.Write(remote.JobRecord{
		RunID: "remote1", Repository: "acme/demo", BaseBranch: "main", State: remote.StateRunning,
		StartedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.NewStatusStore(statusRoot); err != nil {
		t.Fatal(err)
	}
	model, err := NewModel(Config{RunsRoot: runsRoot, RemoteStatusRoot: statusRoot, Recent: 20, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot()
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].State != "interrupted" || snapshot.Runs[0].Repository != "acme/demo" {
		t.Fatalf("runs = %#v", snapshot.Runs)
	}
	chunk, status, err := model.ReadLog("remote1", "agent", "0")
	if err != nil || status != 200 || !chunk.EOF || chunk.Data != "" {
		t.Fatalf("queued remote log = %#v, status = %d, err = %v", chunk, status, err)
	}
}

func TestViewModelDoesNotExposeSandboxInternals(t *testing.T) {
	root := t.TempDir()
	st := store.New(root)
	if err := st.WriteMetadata(store.Metadata{
		RunID: "safe1", Repo: "/private/ephemeral/source", RepositoryLabel: "acme/demo",
		ContainerID: "secret-container", Workspace: "/private/workspace", WorkspaceVolume: "secret-volume",
		CodexVolume: "secret-codex", State: store.StateAgentRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	model, _ := NewModel(Config{RunsRoot: root, Recent: 20})
	payload, _ := json.Marshal(model.Snapshot())
	text := string(payload)
	for _, forbidden := range []string{"secret-container", "secret-volume", "secret-codex", "/private/ephemeral/source", "/private/workspace", "containerId", "workspaceVolume"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("payload exposed %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "acme/demo") {
		t.Fatalf("payload missing repository label: %s", text)
	}
}

func TestReadLogChunksAndResetsAfterTruncation(t *testing.T) {
	root := t.TempDir()
	st := store.New(root)
	if err := st.WriteMetadata(store.Metadata{RunID: "logs1", State: store.StateAgentRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	data := strings.Repeat("x", maxLogChunk+10)
	if err := os.WriteFile(filepath.Join(st.RunDir("logs1"), "agent.log"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	model, _ := NewModel(Config{RunsRoot: root, Recent: 20})
	first, status, err := model.ReadLog("logs1", "agent", "0")
	if err != nil || status != 200 || len(first.Data) != maxLogChunk || first.EOF {
		t.Fatalf("first = %#v, status = %d, err = %v", first, status, err)
	}
	second, _, err := model.ReadLog("logs1", "agent", "999999")
	if err != nil || !second.Reset || second.NextOffset != maxLogChunk {
		t.Fatalf("second = %#v, err = %v", second, err)
	}
	if _, status, err := model.ReadLog("../escape", "agent", "0"); err == nil || status != 404 {
		t.Fatalf("traversal status = %d, err = %v", status, err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("do not expose"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(st.RunDir("logs1"), "setup.log")); err != nil {
		t.Fatal(err)
	}
	if _, status, err := model.ReadLog("logs1", "setup", "0"); err == nil || status != 404 {
		t.Fatalf("symlink status = %d, err = %v", status, err)
	}
}
