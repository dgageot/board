package board

import (
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
)

// Board holds the application state.
type Board struct {
	config   Config
	store    Store
	sessions SessionManager
	poller   *Poller
	mu       sync.RWMutex
	clients  map[chan struct{}]struct{}
}

func newBoard(ctx context.Context, cfg Config, store Store, sessions SessionManager) *Board {
	b := &Board{
		config:   cfg,
		store:    store,
		sessions: sessions,
		clients:  make(map[chan struct{}]struct{}),
	}

	b.poller = newPoller(store, sessions, b.broadcast)
	go b.poller.Run(ctx)

	return b
}

// broadcast wakes all connected SSE clients so they emit a refresh frame.
func (b *Board) broadcast() {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// --- Column helpers ---

func nextColumn(cols []Column, current string) string {
	if i := columnIndex(cols, current); i >= 0 && i+1 < len(cols) {
		return cols[i+1].ID
	}
	return ""
}

func columnPrompt(cols []Column, colID string) string {
	if i := columnIndex(cols, colID); i >= 0 {
		return cols[i].Prompt
	}
	return ""
}

func columnIndex(cols []Column, colID string) int {
	return slices.IndexFunc(cols, func(c Column) bool { return c.ID == colID })
}

// --- SSE handler ---

func (b *Board) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan struct{}, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
	}()

	// Send initial event
	_, _ = io.WriteString(w, "data: refresh\n\n")
	flusher.Flush()

	for {
		select {
		case <-ch:
			_, _ = io.WriteString(w, "data: refresh\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
