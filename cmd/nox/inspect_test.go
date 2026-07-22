package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nox-dev/nox/internal/remote"
)

func TestInspectRemotePrintsOneStatusSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run-1" || r.Header.Get("Authorization") != "Bearer secret" {
			http.NotFound(w, r)
			return
		}
		writeInspectJSON(w, http.StatusOK, remote.RunStatus{
			RunID: "run-1", State: remote.StateCompleted,
			PullRequestURL: "https://github.com/acme/demo/pull/1",
		})
	}))
	defer server.Close()
	client, err := remote.NewClient(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := inspectRemote(context.Background(), client, "run-1", &output); err != nil {
		t.Fatal(err)
	}
	var status remote.RunStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID != "run-1" || status.State != remote.StateCompleted || status.PullRequestURL == "" {
		t.Fatalf("status = %#v", status)
	}
}

func writeInspectJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
