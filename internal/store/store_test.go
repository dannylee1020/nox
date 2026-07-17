package store

import (
	"testing"
	"time"
)

func TestMetadataRoundTripPreservesWorkspaceResources(t *testing.T) {
	st := New(t.TempDir())
	want := Metadata{
		RunID:             "abc123",
		Workspace:         "/tmp/run/workspace",
		WorkspaceVolume:   "nox-abc123-workspace",
		WorkspaceExported: true,
		CodexVolume:       "nox-abc123-codex",
		State:             StateCompleted,
		StartedAt:         time.Now().UTC().Truncate(time.Second),
		Retained:          false,
	}
	if err := st.WriteMetadata(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadMetadata(want.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceVolume != want.WorkspaceVolume || !got.WorkspaceExported || got.CodexVolume != want.CodexVolume {
		t.Fatalf("workspace resources = %#v", got)
	}
}
