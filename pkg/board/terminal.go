package board

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/dgageot/board/pkg/tmux"
)

// Default terminal dimensions when the client does not advertise its size.
const (
	defaultCols = 80
	defaultRows = 24
)

// resizeMsg is the JSON message sent by the terminal client on resize.
type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// wsUpgrader is a shared WebSocket upgrader. Terminals are only attached from
// pages the board itself serves, so cross-origin upgrades are rejected: a
// malicious web page must not be able to drive a local agent terminal.
var wsUpgrader = websocket.Upgrader{CheckOrigin: sameOrigin}

// sameOrigin reports whether the request's Origin header, when present,
// matches the host the request was sent to. Requests without an Origin
// (non-browser clients) are allowed.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// terminalDim parses a terminal dimension query parameter, falling back to
// def when missing or out of the uint16 range.
func terminalDim(s string, def uint16) uint16 {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > math.MaxUint16 {
		return def
	}
	return uint16(n)
}

// handleTerminalWS upgrades the request to a WebSocket and bridges it
// to a tmux attach session using raw PTY I/O.
func (b *Board) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("session")
	if sessionName == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	cols := terminalDim(r.URL.Query().Get("cols"), defaultCols)
	rows := terminalDim(r.URL.Query().Get("rows"), defaultRows)

	// Attach on the board's private tmux socket, not the user's default server.
	cmd := exec.Command("tmux", "-S", tmux.SocketPath(), "-2", "attach", "-t", sessionName)
	cmd.Env = append(cmd.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=en_US.UTF-8",
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		log.Printf("terminal session %s: %v", sessionName, err)
		return
	}
	closePTY := sync.OnceFunc(func() { _ = ptmx.Close() })
	defer closePTY()

	// Protect WebSocket writes from concurrent access.
	var wsMu sync.Mutex

	var wg sync.WaitGroup

	// PTY → WebSocket
	wg.Go(func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				wsMu.Lock()
				writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n])
				wsMu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	})

	// WebSocket → PTY
	wg.Go(func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				// Close the PTY so the reader goroutine and cmd.Wait() unblock.
				closePTY()
				return
			}

			if len(data) > 0 && data[0] == '{' {
				var msg resizeMsg
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
					continue
				}
			}

			if _, err := ptmx.Write(data); err != nil {
				return
			}
		}
	})

	_ = cmd.Wait()
	// Close the PTY to unblock the reader goroutine.
	closePTY()
	wg.Wait()

	wsMu.Lock()
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"))
	wsMu.Unlock()
}
