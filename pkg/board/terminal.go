package board

import (
	"context"
	"io"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/sorenisanerd/gotty/backend/localcommand"
	"github.com/sorenisanerd/gotty/webtty"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	Subprotocols:    webtty.Protocols,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// handleTerminalWS upgrades the request to a WebSocket and bridges it
// to a tmux attach session using gotty's webtty protocol.
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

	if err := processTerminalConn(r.Context(), conn, sessionName); err != nil {
		log.Printf("terminal session %s: %v", sessionName, err)
	}
}

func processTerminalConn(ctx context.Context, conn *websocket.Conn, sessionName string) error {
	// Read the initial auth/init message (gotty protocol).
	typ, _, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	if typ != websocket.TextMessage {
		return err
	}

	// Create the local command: tmux -2 attach -t <session>
	// Use -2 for 256-color and set TERM for proper TUI support.
	slave, err := localcommand.New(
		"tmux",
		[]string{"-2", "attach", "-t", sessionName},
		nil,
	)
	if err != nil {
		return err
	}
	defer func() { _ = slave.Close() }()

	tty, err := webtty.New(
		&wsConn{conn},
		slave,
		webtty.WithPermitWrite(),
	)
	if err != nil {
		return err
	}

	return tty.Run(ctx)
}

// wsConn wraps a gorilla/websocket.Conn to implement io.ReadWriter
// compatible with webtty.Master.
type wsConn struct {
	*websocket.Conn
}

func (w *wsConn) Write(p []byte) (int, error) {
	writer, err := w.NextWriter(websocket.TextMessage)
	if err != nil {
		return 0, err
	}
	defer func() { _ = writer.Close() }()
	return writer.Write(p)
}

func (w *wsConn) Read(p []byte) (int, error) {
	for {
		msgType, reader, err := w.NextReader()
		if err != nil {
			return 0, err
		}
		if msgType != websocket.TextMessage {
			continue
		}
		b, err := io.ReadAll(reader)
		if len(b) > len(p) {
			return 0, err
		}
		n := copy(p, b)
		return n, err
	}
}
