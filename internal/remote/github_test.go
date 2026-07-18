package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRepository(t *testing.T) {
	owner, name, err := ParseRepository("acme/demo")
	if err != nil || owner != "acme" || name != "demo" {
		t.Fatalf("parsed = %q/%q, err = %v", owner, name, err)
	}
	for _, value := range []string{"", "acme", "acme/demo/extra", "../demo", "acme/demo?token=secret"} {
		if _, _, err := ParseRepository(value); err == nil {
			t.Errorf("ParseRepository(%q) succeeded", value)
		}
	}
}

func TestGitHubClientCreatesPullRequestWithoutLeakingToken(t *testing.T) {
	const token = "secret-token"
	var request *http.Request
	var body map[string]string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = r
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/acme/demo/pull/7"}`))
	}))
	defer api.Close()

	client := NewGitHubClient(api.URL, token, api.Client())
	pullURL, err := client.CreatePullRequest(context.Background(), "acme", "demo", "main", "nox/run-1", "Remote change", "validated")
	if err != nil {
		t.Fatal(err)
	}
	if pullURL != "https://github.com/acme/demo/pull/7" {
		t.Fatalf("pull URL = %q", pullURL)
	}
	if request.Method != http.MethodPost || request.URL.Path != "/repos/acme/demo/pulls" {
		t.Fatalf("request = %s %s", request.Method, request.URL.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("authorization = %q", got)
	}
	if body["head"] != "nox/run-1" || body["base"] != "main" || body["title"] != "Remote change" {
		t.Fatalf("body = %#v", body)
	}
	serialized, _ := json.Marshal(body)
	if strings.Contains(string(serialized), token) {
		t.Fatal("token leaked into pull request body")
	}
	if err := client.DeleteBranch(context.Background(), "acme", "demo", "nox/run-1"); err != nil {
		t.Fatal(err)
	}
}
