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
func buildMux(board *Board) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/projects", board.handleListProjects)
	mux.HandleFunc("POST /api/projects", board.handleCreateProject)
	mux.HandleFunc("DELETE /api/projects/{id}", board.handleDeleteProject)
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

	return mux, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
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
