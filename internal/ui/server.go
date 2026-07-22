package ui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets/index.html assets/app.css assets/app-state.js assets/app.js
var assets embed.FS

type Server struct {
	model *Model
	files http.Handler
}

func New(config Config) (*Server, error) {
	model, err := NewModel(config)
	if err != nil {
		return nil, err
	}
	public, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	return &Server{model: model, files: http.FileServer(http.FS(public))}, nil
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		switch {
		case r.URL.Path == "/healthz":
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case r.URL.Path == "/api/runs":
			writeJSON(w, http.StatusOK, s.model.Snapshot())
		case strings.HasPrefix(r.URL.Path, "/api/runs/"):
			s.runAPI(w, r)
		case r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/app.css" || r.URL.Path == "/app-state.js" || r.URL.Path == "/app.js":
			s.files.ServeHTTP(w, r)
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	})
}

func (s *Server) runAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		current, ok := s.model.Run(parts[0])
		if !ok {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		writeJSON(w, http.StatusOK, current)
		return
	}
	if len(parts) == 3 && parts[1] == "logs" {
		chunk, status, err := s.model.ReadLog(parts[0], parts[2], r.URL.Query().Get("offset"))
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, chunk)
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
