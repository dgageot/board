package board

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dgageot/board/pkg/tmux"
)

// shutdownTimeout bounds how long an HTTP shutdown may take after a signal.
const shutdownTimeout = 10 * time.Second

//go:embed static
var staticFiles embed.FS

// Run starts the board server.
func Run() error {
	cfg := DefaultConfig()

	// Worktree and control-plane socket paths are derived from the home
	// directory (git.WorktreeDir, socketPath), which ignore lookup errors for
	// convenience. Fail fast here instead of producing broken relative paths
	// later.
	if _, err := os.UserHomeDir(); err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// Seed default columns if the table is empty.
	cols, err := store.ListColumns()
	if err != nil {
		return fmt.Errorf("list columns: %w", err)
	}
	if len(cols) == 0 {
		if err := store.SeedColumns(defaultColumns); err != nil {
			log.Printf("seed columns: %v", err)
		}
	}

	board := newBoard(ctx, cfg, store, tmux.Sessions{})

	mux, err := buildMux(board)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
		// Bound header reads so idle half-open connections cannot pile up
		// (slowloris). Body/write timeouts stay unset: SSE and terminal
		// WebSockets are long-lived by design.
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown
	context.AfterFunc(ctx, func() {
		fmt.Println("\nShutting down...")
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancelShutdown()
		_ = srv.Shutdown(shutdownCtx)
	})

	fmt.Printf("Board running at http://%s\n", cfg.ListenAddr)

	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// buildMux registers all routes for the board.
func buildMux(board *Board) (http.Handler, error) {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/projects", board.handleListProjects)
	mux.HandleFunc("POST /api/projects", board.handleCreateProject)
	mux.HandleFunc("PUT /api/projects/order", board.handleReorderProjects)
	mux.HandleFunc("DELETE /api/projects/{id}", board.handleDeleteProject)
	mux.HandleFunc("GET /api/agents", board.handleListAgents)
	mux.HandleFunc("GET /api/browse", board.handleBrowse)
	mux.HandleFunc("GET /api/columns", board.handleListColumns)
	mux.HandleFunc("PUT /api/columns", board.handleUpdateColumns)
	mux.HandleFunc("GET /api/cards", board.handleListCards)
	mux.HandleFunc("POST /api/cards", board.handleCreateCard)
	mux.HandleFunc("POST /api/cards/{id}/move", board.handleMoveCard)
	mux.HandleFunc("POST /api/cards/{id}/jump", board.handleJumpCard)
	mux.HandleFunc("GET /api/cards/{id}/diff", board.handleDiffCard)
	mux.HandleFunc("DELETE /api/cards/{id}", board.handleDeleteCard)
	mux.HandleFunc("POST /api/cards/{id}/vscode", board.handleOpenVSCode)
	mux.HandleFunc("POST /api/columns/{column}/clear", board.handleClearColumn)
	mux.HandleFunc("GET /api/events", board.handleSSE)
	mux.HandleFunc("GET /api/terminal/{session}", board.handleTerminalWS)

	// Schedule API
	mux.HandleFunc("POST /api/schedule", board.handleScheduleCard)
	mux.HandleFunc("GET /api/schedule", board.handleListSchedule)

	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("static files: %w", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticFS)))

	return csrfProtect(mux), nil
}

// csrfProtect rejects state-changing cross-origin requests. The board serves
// a local, unauthenticated API: without this check any web page the user
// visits could POST to localhost and create cards, send prompts to agents, or
// delete data. Browsers attach an Origin header to cross-origin (and all
// non-GET) requests; a mismatch with the target host is rejected. Requests
// without an Origin come from non-browser clients and are allowed.
func csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// Safe methods; cross-origin reads are already blocked by CORS.
		default:
			if !sameOrigin(r) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

// writeJSONStatus writes a JSON response with the given status code. The
// Content-Type header is set before the status is written, as required by
// net/http.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON parses the request body and writes a 400 on failure.
// Returns true on success, false if the caller should abort.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return false
	}
	return true
}
