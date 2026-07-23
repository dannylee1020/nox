package store

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMetadataRoundTripPreservesWorkspaceResources(t *testing.T) {
	st := New(t.TempDir())
	want := Metadata{
		RunID:             "abc123",
		Intent:            "test",
		SourceIntegrity:   "passed",
		BaselineVolume:    "nox-abc123-baseline",
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
	if got.WorkspaceVolume != want.WorkspaceVolume || !got.WorkspaceExported || got.CodexVolume != want.CodexVolume || got.Intent != "test" || got.SourceIntegrity != "passed" || got.BaselineVolume == "" {
		t.Fatalf("workspace resources = %#v", got)
	}
}

func TestMetadataWritesAreAtomic(t *testing.T) {
	st := New(t.TempDir())
	metadata := Metadata{RunID: "atomic123", State: StateInitializing, StartedAt: time.Now()}
	if err := st.WriteMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	const writes = 500
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			metadata.State = State(fmt.Sprintf("state-%d", i))
			if err := st.WriteMetadata(metadata); err != nil {
				t.Errorf("write metadata: %v", err)
				return
			}
		}
	}()
	for i := 0; i < writes*2; i++ {
		if _, err := st.ReadMetadata(metadata.RunID); err != nil {
			t.Fatalf("read metadata during writes: %v", err)
		}
	}
	wg.Wait()
}
