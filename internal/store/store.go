package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type State string

const (
	StateInitializing State = "initializing"
	StateCloning      State = "cloning"
	StateStarting     State = "starting"
	StateAgentRunning State = "agent_running"
	StateValidating   State = "validating"
	StatePublishing   State = "publishing"
	StateCompleted    State = "completed"
	StateFailed       State = "failed"
	StateCancelled    State = "cancelled"
	StateTeardown     State = "teardown"
)

type Metadata struct {
	RunID          string    `json:"runId"`
	Repo           string    `json:"repo"`
	From           string    `json:"from"`
	BaseSHA        string    `json:"baseSha"`
	OutputBranch   string    `json:"outputBranch"`
	ResultSHA      string    `json:"resultSha,omitempty"`
	Agent          string    `json:"agent"`
	Validation     string    `json:"validation"`
	Network        string    `json:"network"`
	Image          string    `json:"image"`
	ContainerID    string    `json:"containerId,omitempty"`
	State          State     `json:"state"`
	ExitCode       int       `json:"exitCode,omitempty"`
	ValidationCode int       `json:"validationCode,omitempty"`
	Error          string    `json:"error,omitempty"`
	Warning        string    `json:"warning,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
	CompletedAt    time.Time `json:"completedAt,omitempty"`
	Workspace      string    `json:"workspace"`
	Retained       bool      `json:"retained"`
}

type Store struct {
	Root string
}

func New(root string) Store { return Store{Root: root} }

func (s Store) RunDir(id string) string { return filepath.Join(s.Root, id) }

func (s Store) Ensure(id string) (string, error) {
	dir := s.RunDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create run directory: %w", err)
	}
	return dir, nil
}

func (s Store) WriteMetadata(metadata Metadata) error {
	dir, err := s.Ensure(metadata.RunID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "metadata.json"), append(data, '\n'), 0o600)
}

func (s Store) ReadMetadata(id string) (Metadata, error) {
	data, err := os.ReadFile(filepath.Join(s.RunDir(id), "metadata.json"))
	if err != nil {
		return Metadata{}, fmt.Errorf("read run metadata: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("parse run metadata: %w", err)
	}
	return metadata, nil
}

func (s Store) WriteText(id, name, text string) error {
	dir, err := s.Ensure(id)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600)
}

func (s Store) OpenLog(id, name string) (*os.File, error) {
	dir, err := s.Ensure(id)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

func (s Store) RemoveWorkspace(id string) error {
	return os.RemoveAll(filepath.Join(s.RunDir(id), "workspace"))
}

func (s Store) RemoveRun(id string) error {
	return os.RemoveAll(s.RunDir(id))
}

func (s Store) ListIDs() ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}
