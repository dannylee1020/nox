package remote

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nox-dev/nox/internal/run"
)

type ServerConfig struct {
	APIToken     string
	Executor     Executor
	MaxBodyBytes int64
}

type Server struct {
	apiToken     string
	executor     Executor
	maxBodyBytes int64
	mu           sync.RWMutex
	jobs         map[string]*job
	active       string
}

type job struct {
	status RunStatus
	cancel context.CancelFunc
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
	return &Server{apiToken: config.APIToken, executor: config.Executor, maxBodyBytes: config.MaxBodyBytes, jobs: make(map[string]*job)}, nil
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
	if request.TimeoutSeconds == 0 {
		request.TimeoutSeconds = 7200
	}

	id := run.NewID()
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.active != "" {
		s.mu.Unlock()
		cancel()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "another run is active"})
		return
	}
	status := RunStatus{RunID: id, State: StateQueued, StartedAt: time.Now().UTC()}
	s.jobs[id] = &job{status: status, cancel: cancel}
	s.active = id
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
	publication, err := s.executor.Execute(ctx, id, request)
	s.mu.Lock()
	current, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	defer s.mu.Unlock()
	current.status.CompletedAt = time.Now().UTC()
	s.active = ""
	if ctx.Err() != nil {
		current.status.State = StateCancelled
		current.status.Stage = "cancelled"
		current.status.Error = "run cancelled"
		return
	}
	if err != nil {
		current.status.State = StateFailed
		current.status.Stage = "execution"
		current.status.Error = err.Error()
		return
	}
	current.status.Branch = publication.Branch
	current.status.Commit = publication.Commit
	current.status.PullRequestURL = publication.PullRequestURL
	if publication.NoChanges {
		current.status.State = StateNoChanges
		current.status.Stage = "completed"
		return
	}
	current.status.State = StateCompleted
	current.status.Stage = "pull_request"
}

func (s *Server) setState(id, state, stage, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.jobs[id]; ok {
		current.status.State = state
		current.status.Stage = stage
		current.status.Error = message
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
	if strings.TrimSpace(request.Title) == "" || len(request.Title) > 200 {
		return fmt.Errorf("title is required and must be at most 200 characters")
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
