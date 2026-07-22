package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestCancelRunCallsRemoteEndpoint(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run-1/cancel" && r.Header.Get("Authorization") == "Bearer secret" {
			called.Store(true)
			writeCancelJSON(w, http.StatusAccepted, map[string]string{"state": "cancellation_requested"})
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

func writeCancelJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
