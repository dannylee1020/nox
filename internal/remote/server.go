package remote

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nox-dev/nox/internal/run"
)

type ServerConfig struct {
	APIToken          string
	Executor          Executor
	MaxBodyBytes      int64
	MaxConcurrentRuns int
	StatusRoot        string
}

const DefaultMaxConcurrentRuns = 5

type Server struct {
	apiToken          string
	executor          Executor
	maxBodyBytes      int64
	mu                sync.RWMutex
	jobs              map[string]*job
	active            map[string]struct{}
	maxConcurrentRuns int
	statusStore       *StatusStore
}

type job struct {
	status  RunStatus
	request RunRequest
	cancel  context.CancelFunc
}

func NewServer(config ServerConfig) (*Server, error) {
	if strings.TrimSpace(config.APIToken) == "" {
		return nil, fmt.Errorf("NOX_API_TOKEN is required")
	}
	if config.Executor == nil {
		return nil, fmt.Errorf("remote executor is required")
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = 2 << 20
	}
	if config.MaxConcurrentRuns < 0 {
		return nil, fmt.Errorf("max concurrent runs must be positive")
	}
	if config.MaxConcurrentRuns == 0 {
		config.MaxConcurrentRuns = DefaultMaxConcurrentRuns
	}
	var statusStore *StatusStore
	if config.StatusRoot != "" {
		var err error
		statusStore, err = NewStatusStore(config.StatusRoot)
		if err != nil {
			return nil, err
		}
	}
	return &Server{
		apiToken: config.APIToken, executor: config.Executor, maxBodyBytes: config.MaxBodyBytes,
		jobs: make(map[string]*job), active: make(map[string]struct{}), maxConcurrentRuns: config.MaxConcurrentRuns,
		statusStore: statusStore,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/v1/runs", s.runs)
	mux.HandleFunc("/v1/runs/", s.runByID)
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request RunRequest
	if err := decodeJSON(w, r, s.maxBodyBytes, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateRequest(request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	request.Network = normalizedNetwork(request.Network)
	intent, _ := run.ParseIntent(request.Mode)
	request.Mode = string(intent)
	if request.TimeoutSeconds == 0 {
		request.TimeoutSeconds = 7200
	}

	id := run.NewID()
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if len(s.active) >= s.maxConcurrentRuns {
		s.mu.Unlock()
		cancel()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "maximum concurrent runs reached"})
		return
	}
	status := RunStatus{RunID: id, Mode: request.Mode, State: StateQueued, StartedAt: time.Now().UTC()}
	current := &job{status: status, request: request, cancel: cancel}
	if err := s.writeJob(current); err != nil {
		s.mu.Unlock()
		cancel()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist remote run status"})
		return
	}
	s.jobs[id] = current
	s.active[id] = struct{}{}
	s.mu.Unlock()

	go s.execute(ctx, id, request)
	writeJSON(w, http.StatusAccepted, status)
}

func (s *Server) runByID(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "cancel") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	id := parts[0]
	if len(parts) == 2 {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		s.cancelRun(w, id)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	s.mu.RLock()
	current, ok := s.jobs[id]
	if ok {
		status := current.status
		s.mu.RUnlock()
		writeJSON(w, http.StatusOK, status)
		return
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
}

func (s *Server) cancelRun(w http.ResponseWriter, id string) {
	s.mu.RLock()
	current, ok := s.jobs[id]
	if ok {
		state := current.status.State
		cancel := current.cancel
		s.mu.RUnlock()
		if state == StateCompleted || state == StateNoChanges || state == StateFailed || state == StateCancelled {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "run is already finished"})
			return
		}
		cancel()
		writeJSON(w, http.StatusAccepted, map[string]string{"runId": id, "state": "cancellation_requested"})
		return
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
}

func (s *Server) execute(ctx context.Context, id string, request RunRequest) {
	s.setState(id, StateRunning, "execution", "")
	outcome, err := s.executor.Execute(ctx, id, request)
	s.mu.Lock()
	current, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	defer s.mu.Unlock()
	current.status.CompletedAt = time.Now().UTC()
	delete(s.active, id)
	if ctx.Err() != nil {
		current.status.State = StateCancelled
		current.status.Stage = "cancelled"
		current.status.Error = "run cancelled"
		s.persistJob(current)
		return
	}
	if err != nil {
		current.status.State = StateFailed
		current.status.Stage = "execution"
		current.status.Error = err.Error()
		s.persistJob(current)
		return
	}
	if outcome.Mode != "" {
		current.status.Mode = outcome.Mode
	}
	current.status.SourceIntegrity = outcome.SourceIntegrity
	current.status.ValidationCode = outcome.ValidationCode
	if outcome.Publication != nil {
		current.status.Branch = outcome.Publication.Branch
		current.status.Commit = outcome.Publication.Commit
		current.status.PullRequestURL = outcome.Publication.PullRequestURL
		if outcome.Publication.NoChanges {
			current.status.State = StateNoChanges
			current.status.Stage = "completed"
			s.persistJob(current)
			return
		}
	}
	if current.status.Mode == string(run.IntentTest) {
		current.status.State = StateCompleted
		current.status.Stage = "evidence"
		s.persistJob(current)
		return
	}
	if outcome.Publication == nil {
		current.status.State = StateFailed
		current.status.Stage = "execution"
		current.status.Error = "feature run completed without publication"
		s.persistJob(current)
		return
	}
	current.status.State = StateCompleted
	current.status.Stage = "pull_request"
	s.persistJob(current)
}

func (s *Server) setState(id, state, stage, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.jobs[id]; ok {
		current.status.State = state
		current.status.Stage = stage
		current.status.Error = message
		s.persistJob(current)
	}
}

func (s *Server) writeJob(current *job) error {
	if s.statusStore == nil {
		return nil
	}
	status := current.status
	return s.statusStore.Write(JobRecord{
		RunID: status.RunID, Mode: current.request.Mode, Repository: current.request.Repository,
		BaseBranch: current.request.BaseBranch, BaseCommit: current.request.BaseCommit, Title: current.request.Title,
		State: status.State, Stage: status.Stage, Error: status.Error,
		SourceIntegrity: status.SourceIntegrity, ValidationCode: status.ValidationCode,
		Branch: status.Branch, Commit: status.Commit, PullRequestURL: status.PullRequestURL,
		StartedAt: status.StartedAt, CompletedAt: status.CompletedAt,
	})
}

func (s *Server) persistJob(current *job) {
	if err := s.writeJob(current); err != nil {
		log.Printf("nox: persist remote status: %v", err)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	return len(token) == len(s.apiToken) && subtle.ConstantTimeCompare([]byte(token), []byte(s.apiToken)) == 1
}

func validateRequest(request RunRequest) error {
	intent, err := run.ParseIntent(request.Mode)
	if err != nil {
		return err
	}
	if _, _, err := ParseRepository(request.Repository); err != nil {
		return err
	}
	if len(request.BaseCommit) != 40 {
		return fmt.Errorf("baseCommit must be a 40-character SHA")
	}
	if _, err := hex.DecodeString(request.BaseCommit); err != nil {
		return fmt.Errorf("baseCommit must be hexadecimal")
	}
	if !validBranch(request.BaseBranch) {
		return fmt.Errorf("baseBranch is invalid")
	}
	if len(request.Title) > 200 || (intent == run.IntentFeat && strings.TrimSpace(request.Title) == "") {
		return fmt.Errorf("title is required for feat mode and must be at most 200 characters")
	}
	if strings.TrimSpace(request.Task) == "" || strings.TrimSpace(request.Validation) == "" {
		return fmt.Errorf("task and validation are required")
	}
	if request.Network != "" && request.Network != "online" && request.Network != "none" {
		return fmt.Errorf("network must be online or none")
	}
	if request.TimeoutSeconds < 0 || request.TimeoutSeconds > 24*60*60 {
		return fmt.Errorf("timeoutSeconds must be between 0 and 86400")
	}
	return nil
}

func validBranch(branch string) bool {
	if branch == "" || strings.IndexFunc(branch, unicode.IsSpace) >= 0 || strings.ContainsAny(branch, "~^:?*[\\\\") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	return !strings.HasPrefix(branch, "/") && !strings.HasSuffix(branch, "/")
}

func normalizedNetwork(network string) string {
	if network == "" {
		return "online"
	}
	return network
}

func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request must contain one JSON object")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}
