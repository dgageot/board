package board

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dgageot/board/pkg/credentials"
	"github.com/dgageot/board/pkg/loops"
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
			return fmt.Errorf("seed columns: %w", err)
		}
	}

	sessions := tmux.Sessions{}
	if cfg.SharedCredentials {
		dir := filepath.Join(os.TempDir(), "board-credentials-"+strconv.Itoa(os.Getuid()))
		source := os.Getenv("BOARD_SSH_AUTH_SOCK")
		if source == "" && strings.Contains(os.Getenv("SSH_AUTH_SOCK"), "1password") {
			source = os.Getenv("SSH_AUTH_SOCK")
		}
		helper, err := credentials.Start(ctx, dir, source, os.Environ())
		if err != nil {
			return fmt.Errorf("start credential helper: %w", err)
		}
		defer helper.Close()
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate Board executable: %w", err)
		}
		launcher, err := helper.InstallLauncher(executable)
		if err != nil {
			return err
		}
		sessions = tmux.Sessions{CredentialSocket: helper.Socket, Executable: launcher}
	}

	b, err := newBoard(ctx, cfg, store, sessions)
	if err != nil {
		return err
	}

	loopDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	b.loopService, err = loops.NewService(ctx, b.loops, &loops.ProcessRunner{Directory: filepath.Join(loopDir, "board", "loops", "attempts")}, cmp.Or(os.Getenv("BOARD_LOOPS_MODEL"), "auto"), b.broadcast)
	if err != nil {
		return err
	}
	defer func() { cancel(); b.loopService.Close() }()
	stopLearning := b.startRunLearning(ctx)
	defer stopLearning()

	handler, err := buildMux(b)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler: handler,
		// Bound header reads so idle half-open connections cannot pile up
		// (slowloris). Body/write timeouts stay unset: SSE and terminal
		// WebSockets are long-lived by design.
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	stopInitialRuns := b.startInitialRuns(ctx)
	defer stopInitialRuns()
	fmt.Printf("Board running at http://%s\n", cfg.ListenAddr)

	return serve(ctx, srv, ln)
}

// serve runs srv on ln until ctx is canceled, then shuts it down and waits
// (bounded by shutdownTimeout) for in-flight requests to finish before
// returning, so callers can safely release resources handlers depend on.
func serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	fmt.Println("\nShutting down...")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	// Connections still open past the timeout are dropped when the process
	// exits, as before.
	_ = srv.Shutdown(shutdownCtx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// buildMux registers all routes for the board.
func buildMux(b *Board) (http.Handler, error) {
	mux := http.NewServeMux()

	loops.Register(mux, b.loops, b.broadcast, b.loopService)
	mux.HandleFunc("DELETE /api/loops/{id}", b.handleDeleteLoop)
	mux.HandleFunc("GET /api/loops/{id}/tasks", b.handleLoopProjectTasks)
	mux.HandleFunc("POST /api/loops/{id}/tasks", b.handleRunLoopProjectTask)
	mux.HandleFunc("POST /api/loops/{id}/tasks/{task}/close", b.handleCloseLoopProjectTask)
	mux.HandleFunc("POST /api/loops/{id}/tasks/{task}/prepare", b.handleRetryRunLearning)
	mux.HandleFunc("GET /api/loops/{id}/tasks/{task}/evidence", b.handleRunEvidence)

	// API routes
	mux.HandleFunc("GET /api/projects", b.handleListProjects)
	mux.HandleFunc("POST /api/projects", b.handleCreateProject)
	mux.HandleFunc("PUT /api/projects/order", b.handleReorderProjects)
	mux.HandleFunc("PUT /api/projects/{id}", b.handleUpdateProject)
	mux.HandleFunc("DELETE /api/projects/{id}", b.handleDeleteProject)
	mux.HandleFunc("GET /api/agents", b.handleListAgents)
	mux.HandleFunc("GET /api/browse", b.handleBrowse)
	mux.HandleFunc("GET /api/columns", b.handleListColumns)
	mux.HandleFunc("PUT /api/columns", b.handleUpdateColumns)
	mux.HandleFunc("GET /api/cards", b.handleListCards)
	mux.HandleFunc("POST /api/cards", b.handleCreateCard)
	mux.HandleFunc("POST /api/cards/{id}/move", b.handleMoveCard)
	mux.HandleFunc("POST /api/cards/{id}/jump", b.handleJumpCard)
	mux.HandleFunc("POST /api/cards/{id}/coach", b.handleCoachCard)
	mux.HandleFunc("GET /api/cards/{id}/diff", b.handleDiffCard)
	mux.HandleFunc("GET /api/cards/{id}/commits", b.handleCardCommits)
	mux.HandleFunc("GET /api/cards/{id}/pr", b.handlePRStatus)
	mux.HandleFunc("DELETE /api/cards/{id}", b.handleDeleteCard)
	mux.HandleFunc("POST /api/cards/{id}/vscode", b.handleOpenVSCode)
	mux.HandleFunc("POST /api/columns/{column}/clear", b.handleClearColumn)
	mux.HandleFunc("GET /api/events", b.handleSSE)
	mux.HandleFunc("GET /api/terminal/{session}", b.handleTerminalWS)

	// Schedule API
	mux.HandleFunc("POST /api/schedule", b.handleScheduleCard)
	mux.HandleFunc("GET /api/schedule", b.handleListSchedule)

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
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // status already written, nothing to report
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
