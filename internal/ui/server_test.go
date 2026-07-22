package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerIsGETOnlyAndSetsSecurityHeaders(t *testing.T) {
	server, err := New(Config{RunsRoot: t.TempDir(), Recent: 20})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/runs", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST status = %d, headers = %v", post.Code, post.Header())
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Run summary") {
		t.Fatalf("page status = %d, body = %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), "<title>Nox</title>") || !strings.Contains(page.Body.String(), "<h1>Nox</h1>") || strings.Contains(page.Body.String(), "Nox runs") {
		t.Fatalf("page does not use the Nox product title: %s", page.Body.String())
	}
	if !strings.Contains(page.Body.String(), "app-state.js") || !strings.Contains(page.Body.String(), "run-substage") || !strings.Contains(page.Body.String(), "run-facts") || !strings.Contains(page.Body.String(), "metadata-grid") {
		t.Fatalf("page is missing progress state assets: %s", page.Body.String())
	}
	if !strings.Contains(page.Body.String(), "toggle-log-size") || !strings.Contains(page.Body.String(), ">Logs</button>") || strings.Contains(page.Body.String(), "connection-label") || strings.Contains(page.Body.String(), "id=\"as-of\"") {
		t.Fatalf("page does not expose the expected log controls: %s", page.Body.String())
	}
	if strings.Contains(page.Body.String(), "id=\"run-title\"") {
		t.Fatalf("page still exposes a task title: %s", page.Body.String())
	}
	stateAsset := httptest.NewRecorder()
	handler.ServeHTTP(stateAsset, httptest.NewRequest(http.MethodGet, "/app-state.js", nil))
	if stateAsset.Code != http.StatusOK || !strings.Contains(stateAsset.Body.String(), "reconcileSelection") {
		t.Fatalf("state asset status = %d, body = %s", stateAsset.Code, stateAsset.Body.String())
	}
	for _, header := range []string{"Content-Security-Policy", "Cache-Control", "X-Content-Type-Options"} {
		if page.Header().Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
}

func TestServerRejectsUnknownRunAndLogRoutes(t *testing.T) {
	server, _ := New(Config{RunsRoot: t.TempDir(), Recent: 20})
	for _, path := range []string{"/api/runs/../secret", "/api/runs/missing", "/api/runs/missing/logs/system", "/unknown"} {
		record := httptest.NewRecorder()
		server.Handler().ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d", path, record.Code)
		}
	}
}
