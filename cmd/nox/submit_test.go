package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nox-dev/nox/internal/remote"
)

func TestSubmitUsesRemoteClientWithoutLocalDoctor(t *testing.T) {
	root := t.TempDir()
	fakeGit := filepath.Join(root, "git")
	gitScript := fmt.Sprintf(`#!/bin/sh
case "$*" in
  "rev-parse --show-toplevel") printf '%%s\n' %q ;;
  "symbolic-ref --quiet --short HEAD") printf 'main\n' ;;
  "status --porcelain --untracked-files=all") ;;
  "config --get remote.origin.url") printf 'git@github.com:acme/demo.git\n' ;;
  "ls-remote --heads origin refs/heads/release") printf '%%s\trefs/heads/release\n' %q ;;
  *) echo "unexpected git command: $*" >&2; exit 1 ;;
esac
`, root, strings.Repeat("b", 40))
	if err := os.WriteFile(fakeGit, []byte(gitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	oldURL := os.Getenv("NOX_REMOTE_URL")
	oldToken := os.Getenv("NOX_API_TOKEN")
	defer func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("NOX_REMOTE_URL", oldURL)
		_ = os.Setenv("NOX_API_TOKEN", oldToken)
	}()
	_ = os.Setenv("PATH", root+string(os.PathListSeparator)+oldPath)

	var submitted remote.RunRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			writeSubmitTestJSON(w, http.StatusAccepted, map[string]any{"runId": "run-1", "state": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-1":
			writeSubmitTestJSON(w, http.StatusOK, map[string]any{
				"runId": "run-1", "state": "completed", "branch": "nox/run-1",
				"commit": "b", "pullRequestUrl": "https://github.com/acme/demo/pull/1",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_ = os.Setenv("NOX_REMOTE_URL", server.URL)
	_ = os.Setenv("NOX_API_TOKEN", "api-secret")

	if err := submit([]string{
		"--repo", root,
		"--from", "release",
		"--title", "remote change",
		"--task", "task",
		"--validate", "go test ./...",
		"--poll-interval", "1ms",
	}); err != nil {
		t.Fatal(err)
	}
	if submitted.Repository != "acme/demo" || submitted.BaseBranch != "release" || submitted.BaseCommit != strings.Repeat("b", 40) {
		t.Fatalf("submitted request = %#v", submitted)
	}
}

func writeSubmitTestJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
