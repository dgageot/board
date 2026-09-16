package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloseIdleConnectionsReleasesUnixConnection(t *testing.T) {
	// Keep the socket path within Darwin's 104-byte limit.
	t.Setenv("TMPDIR", "/tmp")
	socket := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	closed := make(chan struct{}, 1)
	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"title":"ok"}`) }),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				closed <- struct{}{}
			}
		},
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	client := NewClient(socket, "session")
	t.Cleanup(client.CloseIdleConnections)
	ctx, cancel := context.WithCancel(t.Context())
	snap, err := client.Snapshot(ctx)
	cancel()
	require.NoError(t, err)
	require.Equal(t, "ok", snap.Title)
	client.CloseIdleConnections()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("idle connection remained open")
	}
}
