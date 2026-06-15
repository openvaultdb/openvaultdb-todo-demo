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

// Server holds the in-memory session (the single scoped OVDB connection) and the
// OVDB client. A real app would key sessions per user; for the demo a single
// process-wide connection is sufficient.
type Server struct {
	ovdb       *ovdb.Client
	connectURL string // OVDB Connect endpoint to route vault selection through

	mu        sync.RWMutex
	conn      ovdb.Conn       // scoped vault connection; zero value until connected
	pendingMu sync.Mutex      // guards pending
	pending   map[string]bool // valid CSRF state values awaiting callback
}

// New constructs a Server. connectURL is the OVDB Connect endpoint the connect
// flow routes through (e.g. https://openvaultdb.com/connect).
func New(connectURL string) *Server {
	return &Server{
		ovdb:       ovdb.New(),
		connectURL: connectURL,
		pending:    make(map[string]bool),
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
	fmt.Printf("todo-backend listening on http://localhost:%d (OVDB Connect: %s)\n", port, s.connectURL)
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
	// Route through OVDB Connect (not straight to a vault server) — Connect lets
	// the user pick a registered vault or paste a connection string.
	http.Redirect(w, r, ovdb.ConnectURL(s.connectURL, state), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	iss := r.URL.Query().Get("iss")

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
	// `iss` (RFC 9207) names the vault server that issued the code — chosen by the
	// user in OVDB Connect. We exchange the code and run record CRUD against it.
	if iss == "" {
		http.Error(w, "missing iss (issuing vault server)", http.StatusBadRequest)
		return
	}

	tok, err := s.ovdb.ExchangeCode(r.Context(), iss, code)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.mu.Lock()
	s.conn = ovdb.Conn{BaseURL: iss, VaultID: tok.Vault, Token: tok.AccessToken}
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

func (s *Server) currentConn() (ovdb.Conn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn, s.conn.Token != ""
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	_, connected := s.currentConn()
	writeJSON(w, http.StatusOK, map[string]bool{"connected": connected})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.currentConn()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not connected")
		return
	}
	recs, err := s.ovdb.ListRecords(r.Context(), conn)
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
	conn, ok := s.currentConn()
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
	rec, err := s.ovdb.CreateRecord(r.Context(), conn, ovdb.Record{
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
	conn, ok := s.currentConn()
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
	rec, err := s.ovdb.UpdateRecord(r.Context(), conn, id, patch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, recordToTask(rec))
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.currentConn()
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not connected")
		return
	}
	id := r.PathValue("id")
	if err := s.ovdb.DeleteRecord(r.Context(), conn, id); err != nil {
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
