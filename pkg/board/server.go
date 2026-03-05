package board

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os/signal"
	"syscall"
)

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

	board := newBoard(ctx, cfg, store, tmuxSessionManager{})

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/projects", board.handleListProjects)
	mux.HandleFunc("POST /api/projects", board.handleCreateProject)
	mux.HandleFunc("DELETE /api/projects/{id}", board.handleDeleteProject)
	mux.HandleFunc("GET /api/columns", board.handleListColumns)
	mux.HandleFunc("PUT /api/columns", board.handleUpdateColumns)
	mux.HandleFunc("GET /api/cards", board.handleListCards)
	mux.HandleFunc("POST /api/cards", board.handleCreateCard)
	mux.HandleFunc("POST /api/cards/{id}/next", board.handleNextCard)
	mux.HandleFunc("POST /api/cards/{id}/move", board.handleMoveCard)
	mux.HandleFunc("POST /api/cards/{id}/jump", board.handleJumpCard)
	mux.HandleFunc("GET /api/cards/{id}/diff", board.handleDiffCard)
	mux.HandleFunc("POST /api/cards/{id}/auto", board.handleToggleAutoCard)
	mux.HandleFunc("DELETE /api/cards/{id}", board.handleDeleteCard)
	mux.HandleFunc("POST /api/columns/{column}/clear", board.handleClearColumn)
	mux.HandleFunc("GET /api/events", board.handleSSE)
	mux.HandleFunc("GET /api/terminal/{session}", board.handleTerminalWS)

	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("static files: %w", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticFS)))

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		fmt.Println("\nShutting down...")
		_ = srv.Shutdown(context.Background())
	}()

	fmt.Printf("Board running at http://localhost%s\n", cfg.ListenAddr)

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}

	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
