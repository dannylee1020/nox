package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/remote"
)

func TestWatchRemoteReportsPullRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run-1" {
			http.NotFound(w, r)
			return
		}
		writeRemoteWatchJSON(w, http.StatusOK, remote.RunStatus{
			RunID: "run-1", State: remote.StateCompleted,
			PullRequestURL: "https://github.com/acme/demo/pull/1",
		})
	}))
	defer server.Close()
	client, err := remote.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var output, status bytes.Buffer
	if err := watchRemoteRun(context.Background(), client, "run-1", time.Millisecond, &output, &status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "https://github.com/acme/demo/pull/1") || !strings.Contains(status.String(), "completed") {
		t.Fatalf("output = %q, status = %q", output.String(), status.String())
	}
}

func TestWatchRemoteDoesNotCancelWhenStopped(t *testing.T) {
	var cancelCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			cancelCalled.Store(true)
			http.Error(w, "unexpected cancel", http.StatusInternalServerError)
			return
		}
		writeRemoteWatchJSON(w, http.StatusOK, remote.RunStatus{RunID: "run-1", State: remote.StateRunning})
	}))
	defer server.Close()
	client, err := remote.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	var output, status bytes.Buffer
	go func() {
		time.Sleep(10 * time.Millisecond)
		stop()
	}()
	if err := watchRemoteRun(ctx, client, "run-1", time.Second, &output, &status); err != nil {
		t.Fatal(err)
	}
	if cancelCalled.Load() || !strings.Contains(status.String(), "run continues") {
		t.Fatalf("cancel called = %v, status = %q", cancelCalled.Load(), status.String())
	}
}

func TestCancelRunCallsRemoteEndpoint(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run-1/cancel" && r.Header.Get("Authorization") == "Bearer secret" {
			called.Store(true)
			writeRemoteWatchJSON(w, http.StatusAccepted, map[string]string{"state": "cancellation_requested"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	oldURL, oldToken := os.Getenv("NOX_REMOTE_URL"), os.Getenv("NOX_API_TOKEN")
	defer func() {
		_ = os.Setenv("NOX_REMOTE_URL", oldURL)
		_ = os.Setenv("NOX_API_TOKEN", oldToken)
	}()
	_ = os.Setenv("NOX_REMOTE_URL", server.URL)
	_ = os.Setenv("NOX_API_TOKEN", "secret")
	if err := cancelRun([]string{"--remote", "run-1"}); err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("remote cancellation endpoint was not called")
	}
}

func writeRemoteWatchJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
