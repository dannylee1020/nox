package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nox-dev/nox/internal/remote"
	"github.com/nox-dev/nox/internal/store"
)

const maxLogChunk = 64 * 1024

type Config struct {
	RunsRoot         string
	RemoteStatusRoot string
	Recent           int
	Now              func() time.Time
}

type Model struct {
	runsRoot         string
	remoteStatusRoot string
	recent           int
	now              func() time.Time
}

type Run struct {
	RunID           string    `json:"runId"`
	Mode            string    `json:"mode,omitempty"`
	Source          string    `json:"source"`
	Repository      string    `json:"repository"`
	Title           string    `json:"title,omitempty"`
	BaseRef         string    `json:"baseRef,omitempty"`
	State           string    `json:"state"`
	Stage           string    `json:"stage,omitempty"`
	Validation      string    `json:"validation"`
	SourceIntegrity string    `json:"sourceIntegrity,omitempty"`
	OutputBranch    string    `json:"outputBranch,omitempty"`
	ResultCommit    string    `json:"resultCommit,omitempty"`
	PullRequestURL  string    `json:"pullRequestUrl,omitempty"`
	Error           string    `json:"error,omitempty"`
	Warning         string    `json:"warning,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	CompletedAt     time.Time `json:"completedAt,omitzero"`
	ElapsedSeconds  int64     `json:"elapsedSeconds"`
}

type Counts struct {
	Active     int `json:"active"`
	Validating int `json:"validating"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
}

type Snapshot struct {
	Runs     []Run     `json:"runs"`
	Counts   Counts    `json:"counts"`
	Warnings []string  `json:"warnings,omitempty"`
	AsOf     time.Time `json:"asOf"`
}

type LogChunk struct {
	Data       string `json:"data"`
	NextOffset int64  `json:"nextOffset"`
	Reset      bool   `json:"reset"`
	EOF        bool   `json:"eof"`
}

func NewModel(config Config) (*Model, error) {
	if strings.TrimSpace(config.RunsRoot) == "" {
		return nil, errors.New("runs root is required")
	}
	if config.Recent <= 0 {
		return nil, errors.New("recent run count must be positive")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Model{
		runsRoot: config.RunsRoot, remoteStatusRoot: config.RemoteStatusRoot,
		recent: config.Recent, now: config.Now,
	}, nil
}

func (m *Model) Snapshot() Snapshot {
	runs, warnings := m.loadRuns()
	active := make([]Run, 0, len(runs))
	terminal := make([]Run, 0, len(runs))
	counts := Counts{}
	for _, current := range runs {
		if isTerminal(current.State) {
			terminal = append(terminal, current)
			continue
		}
		active = append(active, current)
		counts.Active++
		if current.State == string(store.StateValidating) {
			counts.Validating++
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].StartedAt.After(active[j].StartedAt) })
	sort.SliceStable(terminal, func(i, j int) bool { return runSortTime(terminal[i]).After(runSortTime(terminal[j])) })
	if len(terminal) > m.recent {
		terminal = terminal[:m.recent]
	}
	for _, current := range terminal {
		switch current.State {
		case remote.StateCompleted, remote.StateNoChanges:
			counts.Completed++
		case remote.StateFailed, remote.StateCancelled, "interrupted":
			counts.Failed++
		}
	}
	return Snapshot{
		Runs: append(active, terminal...), Counts: counts, Warnings: warnings, AsOf: m.now().UTC(),
	}
}

func (m *Model) Run(id string) (Run, bool) {
	if !validRunID(id) {
		return Run{}, false
	}
	runs, _ := m.loadRuns()
	for _, current := range runs {
		if current.RunID == id {
			return current, true
		}
	}
	return Run{}, false
}

func (m *Model) loadRuns() ([]Run, []string) {
	now := m.now().UTC()
	byID := make(map[string]Run)
	warnings := make([]string, 0)
	st := store.New(m.runsRoot)
	ids, err := st.ListIDs()
	if err != nil {
		warnings = append(warnings, "Run evidence could not be listed.")
	}
	for _, id := range ids {
		metadata, err := st.ReadMetadata(id)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Run %s metadata could not be read.", id))
			continue
		}
		current := runFromMetadata(metadata, now)
		byID[id] = current
	}
	if m.remoteStatusRoot != "" {
		records, marker, remoteWarnings := remote.LoadJobRecords(m.remoteStatusRoot)
		for range remoteWarnings {
			warnings = append(warnings, "A remote status record could not be read.")
		}
		for _, record := range records {
			current, exists := byID[record.RunID]
			byID[record.RunID] = mergeRemote(current, exists, record, marker, now)
		}
	}
	runs := make([]Run, 0, len(byID))
	for _, current := range byID {
		runs = append(runs, current)
	}
	return runs, warnings
}

func runFromMetadata(metadata store.Metadata, now time.Time) Run {
	repository := metadata.RepositoryLabel
	if repository == "" {
		repository = metadata.Repo
	}
	updated := metadata.UpdatedAt
	if updated.IsZero() {
		updated = metadata.StartedAt
	}
	current := Run{
		RunID: metadata.RunID, Mode: metadata.Intent, Source: "local", Repository: repository,
		BaseRef: metadata.From, State: string(metadata.State), Stage: string(metadata.State),
		Validation: validationState(string(metadata.State)), SourceIntegrity: metadata.SourceIntegrity, OutputBranch: metadata.OutputBranch,
		ResultCommit: metadata.ResultSHA, Error: metadata.Error, Warning: metadata.Warning,
		StartedAt: metadata.StartedAt, UpdatedAt: updated, CompletedAt: metadata.CompletedAt,
	}
	current.ElapsedSeconds = elapsedSeconds(current, now)
	return current
}

func mergeRemote(current Run, hasMetadata bool, record remote.JobRecord, marker remote.InstanceMarker, now time.Time) Run {
	state := record.State
	stage := record.Stage
	stale := marker.ServerInstanceID != "" && record.ServerInstanceID != marker.ServerInstanceID && !isTerminal(record.State)
	if stale {
		state = "interrupted"
		stage = "coordinator_restarted"
	} else if hasMetadata && !isTerminal(record.State) {
		switch current.State {
		case string(store.StateCompleted):
			state = remote.StatePublishing
			stage = "pull_request"
		default:
			state = current.State
			stage = current.Stage
		}
	}
	current.RunID = record.RunID
	current.Mode = record.Mode
	current.Source = "remote"
	current.Repository = record.Repository
	current.Title = record.Title
	current.BaseRef = record.BaseBranch
	current.State = state
	current.Stage = stage
	current.Validation = validationState(state)
	current.SourceIntegrity = record.SourceIntegrity
	current.PullRequestURL = record.PullRequestURL
	if record.Branch != "" {
		current.OutputBranch = record.Branch
	}
	if record.Commit != "" {
		current.ResultCommit = record.Commit
	}
	if record.Error != "" {
		current.Error = record.Error
	}
	if current.StartedAt.IsZero() {
		current.StartedAt = record.StartedAt
	}
	current.UpdatedAt = record.UpdatedAt
	if !record.CompletedAt.IsZero() {
		current.CompletedAt = record.CompletedAt
	}
	current.ElapsedSeconds = elapsedSeconds(current, now)
	return current
}

func validationState(state string) string {
	switch state {
	case string(store.StateValidating), string(store.StateChecking):
		return "running"
	case string(store.StatePublishing), string(store.StateCompleted), remote.StateNoChanges:
		return "passed"
	case string(store.StateFailed), string(store.StateCancelled), "interrupted":
		return "not_published"
	default:
		return "pending"
	}
}

func isTerminal(state string) bool {
	switch state {
	case string(store.StateCompleted), string(store.StateFailed), string(store.StateCancelled), remote.StateNoChanges, "interrupted":
		return true
	default:
		return false
	}
}

func runSortTime(current Run) time.Time {
	if !current.CompletedAt.IsZero() {
		return current.CompletedAt
	}
	return current.UpdatedAt
}

func elapsedSeconds(current Run, now time.Time) int64 {
	end := now
	if !current.CompletedAt.IsZero() {
		end = current.CompletedAt
	}
	if current.StartedAt.IsZero() || end.Before(current.StartedAt) {
		return 0
	}
	return int64(end.Sub(current.StartedAt).Seconds())
}

func (m *Model) ReadLog(id, kind, offsetValue string) (LogChunk, int, error) {
	if !validRunID(id) {
		return LogChunk{}, 404, errors.New("run not found")
	}
	fileNames := map[string]string{"setup": "setup.log", "agent": "agent.log", "validation": "validation.log"}
	name, ok := fileNames[kind]
	if !ok {
		return LogChunk{}, 404, errors.New("log not found")
	}
	offset := int64(0)
	if offsetValue != "" {
		parsed, err := strconv.ParseInt(offsetValue, 10, 64)
		if err != nil || parsed < 0 {
			return LogChunk{}, 400, errors.New("offset must be a non-negative integer")
		}
		offset = parsed
	}
	runDir := filepath.Join(m.runsRoot, id)
	if info, err := os.Lstat(runDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if _, exists := m.Run(id); exists {
			return LogChunk{NextOffset: 0, EOF: true}, 200, nil
		}
		return LogChunk{}, 404, errors.New("run not found")
	}
	logPath := filepath.Join(runDir, name)
	logInfo, err := os.Lstat(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return LogChunk{NextOffset: 0, EOF: true}, 200, nil
	}
	if err != nil || !logInfo.Mode().IsRegular() || logInfo.Mode()&os.ModeSymlink != 0 {
		return LogChunk{}, 404, errors.New("log not found")
	}
	file, err := os.Open(logPath)
	if err != nil {
		return LogChunk{}, 500, errors.New("read log")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return LogChunk{}, 500, errors.New("read log")
	}
	reset := false
	if offset > info.Size() {
		offset = 0
		reset = true
	}
	remaining := info.Size() - offset
	if remaining > maxLogChunk {
		remaining = maxLogChunk
	}
	data := make([]byte, remaining)
	if len(data) > 0 {
		if _, err := file.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
			return LogChunk{}, 500, errors.New("read log")
		}
	}
	next := offset + int64(len(data))
	return LogChunk{Data: string(data), NextOffset: next, Reset: reset, EOF: next >= info.Size()}, 200, nil
}

func validRunID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
