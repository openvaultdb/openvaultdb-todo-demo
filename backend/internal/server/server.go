// Package server implements the demo backend: an OVDB connect-flow proxy plus a
// small REST API the TypeScript frontend calls. The frontend never talks to the
// OVDB server directly — all task reads/writes flow frontend → here → OVDB.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/openvaultdb/openvaultdb-todo-demo/internal/ovdb"
)

// DefaultPort is the demo backend's listen port (per INTEGRATION.md).
const DefaultPort = 5180

// Server holds the in-memory session (the single scoped OVDB token) and the
// OVDB client. A real app would key sessions per user; for the demo a single
// process-wide token is sufficient.
type Server struct {
	ovdb *ovdb.Client

	mu        sync.RWMutex
	token     string          // scoped OVDB app token, "" until connected
	pendingMu sync.Mutex      // guards pending
	pending   map[string]bool // valid CSRF state values awaiting callback
}

// New constructs a Server.
func New() *Server {
	return &Server{
		ovdb:    ovdb.New(),
		pending: make(map[string]bool),
	}
}

// Listen starts the HTTP server on the given port and blocks until ctx is done.
func (s *Server) Listen(ctx context.Context, port int) error {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: s.routes(),
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Printf("todo-backend listening on http://localhost:%d (OVDB at %s)\n", port, ovdb.BaseURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /connect", s.handleConnect)
	mux.HandleFunc("GET /callback", s.handleCallback)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.handleUpdateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)
	return cors(mux)
}

// --- connect flow ---------------------------------------------------------

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	state := randomHex(16)
	s.pendingMu.Lock()
	s.pending[state] = true
	s.pendingMu.Unlock()
	http.Redirect(w, r, ovdb.AuthorizeURL(state), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	s.pendingMu.Lock()
	ok := s.pending[state]
	delete(s.pending, state)
	s.pendingMu.Unlock()
	if state == "" || !ok {
		http.Error(w, "invalid or unknown state", http.StatusBadRequest)
		return
	}
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	tok, err := s.ovdb.ExchangeCode(r.Context(), code)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.mu.Lock()
	s.token = tok.AccessToken
	s.mu.Unlock()

	http.Redirect(w, r, ovdb.FrontendURL, http.StatusFound)
}

// --- REST API -------------------------------------------------------------

// Task is the frontend-facing shape, mapped 1:1 to a `tasks` record.
type Task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	CreatedAt string `json:"createdAt"`
}

func (s *Server) currentToken() (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token, s.token != ""
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	_, connected := s.currentToken()
	writeJSON(w, http.StatusOK, map[string]bool{"connected": connected})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	token, ok := s.currentToken()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not connected")
		return
	}
	recs, err := s.ovdb.ListRecords(r.Context(), token)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	tasks := make([]Task, 0, len(recs))
	for _, rec := range recs {
		tasks = append(tasks, recordToTask(rec))
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	token, ok := s.currentToken()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not connected")
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeErr(w, http.StatusBadRequest, "title is required")
		return
	}
	rec, err := s.ovdb.CreateRecord(r.Context(), token, ovdb.Record{
		"title": body.Title,
		"done":  false,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, recordToTask(rec))
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	token, ok := s.currentToken()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not connected")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Done  *bool   `json:"done"`
		Title *string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	patch := ovdb.Record{}
	if body.Done != nil {
		patch["done"] = *body.Done
	}
	if body.Title != nil {
		patch["title"] = *body.Title
	}
	if len(patch) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	rec, err := s.ovdb.UpdateRecord(r.Context(), token, id, patch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recordToTask(rec))
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	token, ok := s.currentToken()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not connected")
		return
	}
	id := r.PathValue("id")
	if err := s.ovdb.DeleteRecord(r.Context(), token, id); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers --------------------------------------------------------------

// recordToTask maps an opaque OVDB record onto the frontend Task shape, coping
// with JSON's untyped values.
func recordToTask(rec ovdb.Record) Task {
	t := Task{}
	if v, ok := rec["id"].(string); ok {
		t.ID = v
	}
	if v, ok := rec["title"].(string); ok {
		t.Title = v
	}
	if v, ok := rec["done"].(bool); ok {
		t.Done = v
	}
	if v, ok := rec["createdAt"].(string); ok {
		t.CreatedAt = v
	}
	return t
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", ovdb.FrontendURL)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
