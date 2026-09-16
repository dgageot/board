package board

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleSSEEndsOnShutdown checks that an SSE stream terminates when the
// board's context is canceled. Without this, graceful shutdown would wait the
// full shutdownTimeout for every open browser tab.
func TestHandleSSEEndsOnShutdown(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	b, err := newBoard(ctx, Config{ListenAddr: ":0"}, store, noopSessionManager{})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(b.handleSSE))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	// Initial refresh frame.
	r := bufio.NewReader(resp.Body)
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: refresh\n", line)

	cancel()

	eof := make(chan error, 1)
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				eof <- err
				return
			}
		}
	}()
	select {
	case err := <-eof:
		require.ErrorIs(t, err, io.EOF)
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream still open 3s after the board context was canceled")
	}
}

// TestServeWaitsForInFlightRequests checks that serve does not return until
// an in-flight request has completed after the context is canceled. Run()
// closes the store right after serve returns, so returning early would pull
// the database out from under active handlers.
func TestServeWaitsForInFlightRequests(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseRequest := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseRequest)
	var finished atomic.Bool
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			started <- struct{}{}
			<-release
			finished.Store(true)
			w.WriteHeader(http.StatusOK)
		}),
		ReadHeaderTimeout: time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String(), http.NoBody)
	require.NoError(t, err)
	got := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- 0
			return
		}
		_ = resp.Body.Close()
		got <- resp.StatusCode
	}()

	// Trigger shutdown while the handler is blocked on release.
	<-started
	cancel()

	select {
	case err := <-served:
		t.Fatalf("serve returned (%v) while a request was still in flight", err)
	case <-time.After(300 * time.Millisecond):
	}

	releaseRequest()
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return after the in-flight request completed")
	}
	assert.True(t, finished.Load())
	assert.Equal(t, http.StatusOK, <-got)
}

// TestServeReturnsListenerErrors checks a failing Serve surfaces its error
// instead of blocking until the context is canceled.
func TestServeReturnsListenerErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, ln.Close())

	err = serve(t.Context(), &http.Server{ReadHeaderTimeout: time.Second}, ln)
	require.Error(t, err)
}

func TestServeWithCanceledContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, serve(ctx, &http.Server{ReadHeaderTimeout: time.Second}, ln))
}
