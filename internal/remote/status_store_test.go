package remote

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusStoreWritesAtomicPrivateRecords(t *testing.T) {
	root := t.TempDir()
	statusStore, err := NewStatusStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := JobRecord{
		RunID: "run123", Repository: "acme/demo", BaseBranch: "main",
		BaseCommit: "0123456789abcdef0123456789abcdef01234567", Title: "test run",
		State: StateQueued, StartedAt: time.Now().UTC(),
	}
	if err := statusStore.Write(record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "run123.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("record mode = %o, want 600", got)
	}
	records, marker, warnings := LoadJobRecords(root)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if marker.ServerInstanceID != statusStore.InstanceID {
		t.Fatalf("instance = %q, want %q", marker.ServerInstanceID, statusStore.InstanceID)
	}
	if len(records) != 1 || records[0].Repository != "acme/demo" || records[0].SchemaVersion != JobRecordSchemaVersion {
		t.Fatalf("records = %#v", records)
	}
}

func TestLoadJobRecordsSkipsCorruptRecords(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, _, warnings := LoadJobRecords(root)
	if len(records) != 0 || len(warnings) != 1 {
		t.Fatalf("records = %v, warnings = %v", records, warnings)
	}
}
