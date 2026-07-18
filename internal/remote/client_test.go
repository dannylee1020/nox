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

func TestNewClientRequiresAbsoluteURLAndToken(t *testing.T) {
	for _, input := range [][2]string{{"", "secret"}, {"nox.internal", "secret"}, {"https://nox.internal", ""}} {
		if _, err := NewClient(input[0], input[1], nil); err == nil {
			t.Errorf("NewClient(%q, %q) succeeded", input[0], input[1])
		}
	}
}

func TestClientSubmitsPollsAndCancels(t *testing.T) {
	const token = "secret"
	var mu sync.Mutex
	var submitted RunRequest
	var statusCalls int
	var cancelCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			writeClientJSON(w, http.StatusAccepted, RunStatus{RunID: "run-1", State: StateQueued})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-1":
			mu.Lock()
			statusCalls++
			calls := statusCalls
			mu.Unlock()
			state := StateRunning
			if calls > 1 {
				state = StateCompleted
			}
			writeClientJSON(w, http.StatusOK, RunStatus{RunID: "run-1", State: state, PullRequestURL: "https://github.com/acme/demo/pull/1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run-1/cancel":
			mu.Lock()
			cancelCalled = true
			mu.Unlock()
			writeClientJSON(w, http.StatusAccepted, map[string]string{"state": "cancellation_requested"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := RunRequest{Repository: "acme/demo", BaseBranch: "main", BaseCommit: strings.Repeat("a", 40), Title: "change", Task: "task", Validation: "go test ./..."}
	initial, err := client.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Repository != request.Repository || initial.RunID != "run-1" {
		t.Fatalf("submitted = %#v, initial = %#v", submitted, initial)
	}
	var updates []string
	final, err := client.Wait(context.Background(), initial, time.Millisecond, func(status RunStatus) {
		updates = append(updates, status.State)
	})
	if err != nil || final.State != StateCompleted || len(updates) < 2 {
		t.Fatalf("final = %#v, updates = %#v, err = %v", final, updates, err)
	}
	if err := client.Cancel(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !cancelCalled {
		t.Fatal("cancel request was not sent")
	}
}

func TestClientRedactsTokenFromHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeClientJSON(w, http.StatusUnauthorized, map[string]string{"error": "secret is not authorized"})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Status(context.Background(), "run-1")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v", err)
	}
}

func writeClientJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
