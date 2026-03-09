package board

import (
	"cmp"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"sync"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// resizeMsg is the JSON message sent by the terminal client on resize.
type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleTerminalWS upgrades the request to a WebSocket and bridges it
// to a tmux attach session using raw PTY I/O.
func (b *Board) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("session")
	if sessionName == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))

	cmd := exec.Command("tmux", "-2", "attach", "-t", sessionName)
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cmp.Or(cols, 80)),
		Rows: uint16(cmp.Or(rows, 24)),
	})
	if err != nil {
		log.Printf("terminal session %s: %v", sessionName, err)
		return
	}
	defer func() { _ = ptmx.Close() }()

	var wg sync.WaitGroup

	// PTY → WebSocket
	wg.Go(func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.TextMessage, buf[:n]); writeErr != nil {
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
		defer func() { _ = conn.Close() }()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
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
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"))
	wg.Wait()
}
