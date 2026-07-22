package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nox-dev/nox/internal/run"
)

const JobRecordSchemaVersion = 1

type InstanceMarker struct {
	ServerInstanceID string    `json:"serverInstanceId"`
	StartedAt        time.Time `json:"startedAt"`
}

type JobRecord struct {
	SchemaVersion    int       `json:"schemaVersion"`
	ServerInstanceID string    `json:"serverInstanceId"`
	RunID            string    `json:"runId"`
	Repository       string    `json:"repository"`
	BaseBranch       string    `json:"baseBranch"`
	BaseCommit       string    `json:"baseCommit"`
	Title            string    `json:"title"`
	State            string    `json:"state"`
	Stage            string    `json:"stage,omitempty"`
	Error            string    `json:"error,omitempty"`
	Branch           string    `json:"branch,omitempty"`
	Commit           string    `json:"commit,omitempty"`
	PullRequestURL   string    `json:"pullRequestUrl,omitempty"`
	StartedAt        time.Time `json:"startedAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	CompletedAt      time.Time `json:"completedAt,omitzero"`
}

type StatusStore struct {
	Root       string
	InstanceID string
}

func NewStatusStore(root string) (*StatusStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("remote status root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create remote status root: %w", err)
	}
	instanceID := run.NewID()
	store := &StatusStore{Root: root, InstanceID: instanceID}
	marker := InstanceMarker{ServerInstanceID: instanceID, StartedAt: time.Now().UTC()}
	if err := store.writeJSON("instance.json", marker); err != nil {
		return nil, fmt.Errorf("write remote instance marker: %w", err)
	}
	return store, nil
}

func (s *StatusStore) Write(record JobRecord) error {
	if s == nil {
		return nil
	}
	record.SchemaVersion = JobRecordSchemaVersion
	record.ServerInstanceID = s.InstanceID
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeJSON(record.RunID+".json", record); err != nil {
		return fmt.Errorf("write remote job %s: %w", record.RunID, err)
	}
	return nil
}

func (s *StatusStore) writeJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.Root, ".status-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filepath.Join(s.Root, name))
}

func LoadJobRecords(root string) ([]JobRecord, InstanceMarker, []error) {
	var marker InstanceMarker
	var warnings []error
	if strings.TrimSpace(root) == "" {
		return nil, marker, nil
	}
	if data, err := os.ReadFile(filepath.Join(root, "instance.json")); err == nil {
		if err := json.Unmarshal(data, &marker); err != nil {
			warnings = append(warnings, fmt.Errorf("parse remote instance marker: %w", err))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, fmt.Errorf("read remote instance marker: %w", err))
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, marker, warnings
	}
	if err != nil {
		return nil, marker, append(warnings, fmt.Errorf("list remote jobs: %w", err))
	}
	records := make([]JobRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "instance.json" || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			warnings = append(warnings, fmt.Errorf("read remote job %s: %w", entry.Name(), err))
			continue
		}
		var record JobRecord
		if err := json.Unmarshal(data, &record); err != nil {
			warnings = append(warnings, fmt.Errorf("parse remote job %s: %w", entry.Name(), err))
			continue
		}
		if record.SchemaVersion != JobRecordSchemaVersion || strings.TrimSpace(record.RunID) == "" {
			warnings = append(warnings, fmt.Errorf("remote job %s has an unsupported record", entry.Name()))
			continue
		}
		records = append(records, record)
	}
	return records, marker, warnings
}
