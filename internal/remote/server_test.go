package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeExecutor struct {
	block <-chan struct{}
}

func (f fakeExecutor) Execute(ctx context.Context, runID string, request RunRequest) (Publication, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Publication{}, ctx.Err()
		}
	}
	return Publication{Branch: "nox/" + runID, Commit: "abc123", PullRequestURL: "https://github.com/acme/demo/pull/1"}, nil
}

func validRequest() RunRequest {
	return RunRequest{
		Repository: "acme/demo", BaseBranch: "main",
		BaseCommit: "0123456789abcdef0123456789abcdef01234567",
		Title:      "remote change", Task: "# Nox execution contract v1", Validation: "go test ./...",
		Network: "none", TimeoutSeconds: 10,
	}
}

func TestServerHealthDoesNotRequireAuthentication(t *testing.T) {
	server, err := NewServer(ServerConfig{APIToken: "secret", Executor: fakeExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	record := httptest.NewRecorder()
	server.Handler().ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", record.Code, http.StatusOK)
	}
}

func TestServerRequiresBearerTokenAndStartsRun(t *testing.T) {
	server, err := NewServer(ServerConfig{APIToken: "secret", Executor: fakeExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(validRequest())
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(string(payload))))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(string(payload)))
	request.Header.Set("Authorization", "Bearer secret")
	record := httptest.NewRecorder()
	server.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", record.Code, record.Body.String())
	}
	var status RunStatus
	if err := json.Unmarshal(record.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID == "" || status.State != StateQueued {
		t.Fatalf("status = %#v", status)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.mu.RLock()
		current := server.jobs[status.RunID].status
		server.mu.RUnlock()
		if current.State == StateCompleted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run did not complete")
}

func TestServerRejectsSecondActiveRun(t *testing.T) {
	block := make(chan struct{})
	server, err := NewServer(ServerConfig{APIToken: "secret", Executor: fakeExecutor{block: block}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(validRequest())
	makeRequest := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(string(payload)))
		request.Header.Set("Authorization", "Bearer secret")
		record := httptest.NewRecorder()
		server.Handler().ServeHTTP(record, request)
		return record
	}
	if status := makeRequest().Code; status != http.StatusAccepted {
		t.Fatalf("first status = %d", status)
	}
	if status := makeRequest().Code; status != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", status, http.StatusTooManyRequests)
	}
	close(block)
}

func TestServerCancellationCancelsRun(t *testing.T) {
	block := make(chan struct{})
	server, err := NewServer(ServerConfig{APIToken: "secret", Executor: fakeExecutor{block: block}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(validRequest())
	request := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(string(payload)))
	request.Header.Set("Authorization", "Bearer secret")
	record := httptest.NewRecorder()
	server.Handler().ServeHTTP(record, request)
	var status RunStatus
	_ = json.Unmarshal(record.Body.Bytes(), &status)
	cancel := httptest.NewRecorder()
	cancelRequest := httptest.NewRequest(http.MethodPost, "/v1/runs/"+status.RunID+"/cancel", nil)
	cancelRequest.Header.Set("Authorization", "Bearer secret")
	server.Handler().ServeHTTP(cancel, cancelRequest)
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d", cancel.Code)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.mu.RLock()
		current := server.jobs[status.RunID].status
		server.mu.RUnlock()
		if current.State == StateCancelled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run was not cancelled")
}

func TestValidateRequestRejectsUnsafeValues(t *testing.T) {
	request := validRequest()
	request.BaseCommit = "not-a-sha"
	if err := validateRequest(request); err == nil {
		t.Fatal("expected invalid SHA error")
	}
	request = validRequest()
	request.BaseBranch = "main..evil"
	if err := validateRequest(request); err == nil {
		t.Fatal("expected invalid branch error")
	}
}

func TestServerConcurrentStatusAccess(t *testing.T) {
	server, err := NewServer(ServerConfig{APIToken: "secret", Executor: fakeExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			server.mu.RLock()
			_ = len(server.jobs)
			server.mu.RUnlock()
		}()
	}
	group.Wait()
}
